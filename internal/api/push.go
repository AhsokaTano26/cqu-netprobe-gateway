package api

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tano/cqu-netprobe-gateway/internal/clientip"
	"github.com/tano/cqu-netprobe-gateway/internal/latest"
	"github.com/tano/cqu-netprobe-gateway/internal/metrics"
	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
	"github.com/tano/cqu-netprobe-gateway/internal/token"
)

// bearerPrefix is the fixed Authorization scheme (Protocol v1 §3). It is
// case-sensitive: the protocol specifies "Bearer" exactly.
const bearerPrefix = "Bearer "

// Deps are the push server's collaborators.
type Deps struct {
	Store   *store.Store
	Latest  *latest.Store
	Self    *metrics.Self
	Limiter *Limiter
	Logger  *slog.Logger
	Now     func() time.Time
	// TrustedProxies are the networks whose X-Forwarded-For is believed when
	// attributing a request for the token-guessing throttle. Empty means the
	// header is ignored, which is the correct setting with no proxy in front.
	TrustedProxies []*net.IPNet
}

// Server handles the Protocol v1 push endpoint.
type Server struct {
	store          *store.Store
	latest         *latest.Store
	self           *metrics.Self
	limiter        *Limiter
	logger         *slog.Logger
	now            func() time.Time
	trustedProxies []*net.IPNet
}

// NewServer builds a push server.
func NewServer(d Deps) *Server {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Server{
		store:          d.Store,
		latest:         d.Latest,
		self:           d.Self,
		limiter:        d.Limiter,
		logger:         logger,
		now:            now,
		trustedProxies: d.TrustedProxies,
	}
}

// Routes returns the push API mux. The middleware order is deliberate and is
// documented in the design doc §7:
//
//	route metrics -> body limit -> content type -> handler
//
// Method checking is handled by the mux pattern itself, and authentication,
// rate limiting and validation all live inside the handler so their relative
// order is explicit and testable.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	push := BodyLimit(RequireJSON(http.HandlerFunc(s.handlePush)))
	if s.self != nil {
		push = RouteMetrics(s.self, push)
	}
	mux.Handle("POST /api/v1/push", push)

	// Any other method on the push path is a 405, not a 404 (Protocol v1 §16).
	// It is wrapped in RouteMetrics like the push route: a 405 is still traffic
	// against this route, and leaving it outside the counter would hide every
	// wrong-method request from http_requests_total.
	var notAllowed http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, protocol.CodeInvalidRequest, msgMethod)
	})
	if s.self != nil {
		notAllowed = RouteMetrics(s.self, notAllowed)
	}
	mux.Handle("/api/v1/push", notAllowed)

	// The target list a probe measures from. Additive to the push contract: a
	// probe that never calls it behaves exactly as before it existed.
	var targets http.Handler = http.HandlerFunc(s.handleTargets)
	if s.self != nil {
		targets = RouteMetrics(s.self, targets)
	}
	mux.Handle("GET /api/v1/targets", targets)
	mux.Handle("/api/v1/targets", notAllowed)

	return mux
}

// handlePush runs the full Protocol v1 acceptance chain.
func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	// 1. Authenticate. This must precede rate limiting: otherwise an
	// unauthenticated caller could drain any probe's bucket.
	probe, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	// 2. Authorise.
	if !probe.Enabled {
		s.reject(protocol.CodeProbeDisabled, probe.ProbeID)
		writeError(w, http.StatusForbidden, protocol.CodeProbeDisabled, msgDisabled)
		return
	}

	// 3. Rate limit, before parsing: a throttled request should be cheap.
	// The bucket is keyed on the authenticated probe_id from the database row,
	// never on anything the client supplied.
	if s.limiter != nil && !s.limiter.Allow(probe.ProbeID) {
		s.reject(protocol.CodeRateLimited, probe.ProbeID)
		writeError(w, http.StatusTooManyRequests, protocol.CodeRateLimited, msgRateLimited)
		return
	}

	// 4. Read the body. MaxBytesReader surfaces here, and 413 must win over
	// invalid_json so an oversized malformed body is reported per §20.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.reject(protocol.CodeInvalidRequest, probe.ProbeID)
			writeError(w, http.StatusRequestEntityTooLarge, protocol.CodeInvalidRequest, msgTooLarge)
			return
		}
		s.reject(protocol.CodeInvalidRequest, probe.ProbeID)
		writeError(w, http.StatusBadRequest, protocol.CodeInvalidRequest, msgBadRequest)
		return
	}

	// 5. Decode.
	req, err := protocol.Decode(body)
	if err != nil {
		s.writeProtocolError(w, err, probe.ProbeID)
		return
	}

	// 6. Validate against the current allowlist.
	allowlist, err := s.store.Allowlist()
	if err != nil {
		s.logger.Error("failed to load target allowlist", "error", err)
		s.reject(protocol.CodeInternalError, probe.ProbeID)
		writeError(w, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, msgUnavailable)
		return
	}
	if err := req.Validate(allowlist); err != nil {
		s.writeProtocolError(w, err, probe.ProbeID)
		return
	}

	// 7. Accept. Only now does last_seen advance (Protocol v1 §23).
	s.latest.Put(probe.ProbeID, req.Results, s.now().UTC())
	if s.self != nil {
		s.self.PushTotal.Inc()
	}
	s.logger.Debug("accepted push", "probe_id", probe.ProbeID, "targets", len(req.Results))

	w.WriteHeader(http.StatusNoContent)
}

// authenticate extracts and resolves the bearer token. On failure it writes the
// response and returns false.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*store.Probe, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, bearerPrefix) {
		s.authFailed("missing_or_malformed")
		writeError(w, http.StatusUnauthorized, protocol.CodeUnauthorized, msgUnauthorized)
		return nil, false
	}
	raw := strings.TrimPrefix(header, bearerPrefix)
	if raw == "" {
		s.authFailed("missing_or_malformed")
		writeError(w, http.StatusUnauthorized, protocol.CodeUnauthorized, msgUnauthorized)
		return nil, false
	}

	// An address that has already exhausted its failure budget is answered
	// before the store lookup, so a guessing flood past the budget costs no
	// database work. This is a read-only peek (AuthFailureBlocked), not a
	// charge: it does not consume an allowance, so it cannot make an
	// authenticated caller's own traffic throttle itself.
	//
	// The deliberate consequence is that once an IP has crossed the budget the
	// peek rejects ALL of that address's requests, including well-formed ones.
	// That is what §10.2 specifies (该 IP 的请求快速返回 429), and it is only
	// reachable once something behind that address has been guessing.
	//
	// The status is 429, not 401, even though the underlying cause is failed
	// authentication. §16 defines 401 as "Token 缺失、格式错误或无效", and this
	// response is none of those: a request carrying a perfectly valid token gets
	// it too, because the address itself is blocked. Answering 401 would tell a
	// probe its credential is bad — the exact harm the 503-instead-of-401 split
	// below exists to avoid, and it would make an operator rotate a healthy
	// token. 429 says what is actually true: back off and retry next cycle.
	if s.limiter != nil && s.limiter.AuthFailureBlocked(clientip.From(r, s.trustedProxies)) {
		s.reject(protocol.CodeRateLimited, "")
		writeError(w, http.StatusTooManyRequests, protocol.CodeRateLimited, msgRateLimited)
		return nil, false
	}

	probe, err := s.store.ProbeByTokenHash(token.Hash(raw))
	if errors.Is(err, store.ErrNotFound) {
		// The per-IP throttle is consulted here and ONLY here, on the
		// authentication failure path (design doc §10.2). Campus probes share NAT
		// addresses, so an IP-scoped limit that also counted successful pushes
		// would penalise an entire campus for one noisy neighbour. Its only job is
		// to slow token guessing.
		//
		// That is why the check sits after the lookup rather than before it: a
		// request can only be known to be a failure once its token has failed to
		// resolve, and charging the IP's budget up front would throttle traffic
		// that authenticates perfectly well.
		// Charge the address's failure budget. An exhausted budget answers 429
		// for the same reason the peek above does — the credential may well be
		// fine, it is the address that is out of allowance — so both checks
		// report the same status for the same condition. The ordinary case (a
		// bad token, budget still available) is a genuine credential failure and
		// stays 401.
		if s.limiter != nil && !s.limiter.AllowAuthFailure(clientip.From(r, s.trustedProxies)) {
			// The credential was evaluated and is bad, so this counts as an auth
			// failure; the *response* is a throttle, so it counts as a rejected
			// push too. Two counters for two different questions.
			s.authFailed("invalid_token")
			s.reject(protocol.CodeRateLimited, "")
			writeError(w, http.StatusTooManyRequests, protocol.CodeRateLimited, msgRateLimited)
			return nil, false
		}
		s.authFailed("invalid_token")
		writeError(w, http.StatusUnauthorized, protocol.CodeUnauthorized, msgUnauthorized)
		return nil, false
	}
	if err != nil {
		// A database failure must not be reported as a bad credential, or a
		// probe would silently retry forever with a token that is actually fine.
		s.logger.Error("token lookup failed", "error", err)
		s.reject(protocol.CodeInternalError, "")
		writeError(w, http.StatusServiceUnavailable, protocol.CodeServiceUnavailable, msgUnavailable)
		return nil, false
	}
	return probe, true
}

func (s *Server) authFailed(reason string) {
	if s.self != nil {
		s.self.AuthFailed.WithLabelValues(reason).Inc()
	}
}

func (s *Server) reject(code, probeID string) {
	if s.self != nil {
		s.self.PushRejected.WithLabelValues(code).Inc()
	}
	// Info, not Debug: the default LOG_LEVEL is info, and a rejected push is
	// exactly the signal an operator needs. A probe fleet misconfigured with a
	// bad version, an unknown target or a disabled probe would otherwise produce
	// zero log lines, and the per-probe_id line naming the offender would never
	// appear. Accepted pushes stay at Debug, which is the anti-flood decision.
	//
	// probeID is the public identifier, never the token or its hash.
	s.logger.Info("rejected push", "reason", code, "probe_id", probeID)
}

// writeProtocolError renders a validation failure and records the metric.
func (s *Server) writeProtocolError(w http.ResponseWriter, err error, probeID string) {
	var pe *protocol.Error
	if !errors.As(err, &pe) {
		s.logger.Error("unexpected validation error", "error", err)
		s.reject(protocol.CodeInternalError, probeID)
		writeError(w, http.StatusInternalServerError, protocol.CodeInternalError, errInternal)
		return
	}
	s.reject(pe.Code, probeID)
	writeProtocolError(w, pe)
}
