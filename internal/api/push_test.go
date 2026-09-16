package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/tano/cqu-netprobe-gateway/internal/latest"
	"github.com/tano/cqu-netprobe-gateway/internal/metrics"
	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
	"github.com/tano/cqu-netprobe-gateway/internal/token"
)

type harness struct {
	server *Server
	store  *store.Store
	latest *latest.Store
	self   *metrics.Self
	probe  *store.Probe
	tok    string
	clock  time.Time // the server's clock; tests advance this
}

// baseClock is the instant the harness clock starts at. It is deliberately
// different from goodBody's "timestamp" so that a handler reading the client
// timestamp instead of the server clock is caught.
var baseClock = time.Unix(1789490000, 0).UTC()

func newHarness(t *testing.T) *harness { return newHarnessWith(t, true) }

// newHarnessWithoutSelf builds a server whose Deps.Self is nil, exercising the
// documented contract that self-metrics are optional (Routes only installs
// RouteMetrics when Self is set).
func newHarnessWithoutSelf(t *testing.T) *harness { return newHarnessWith(t, false) }

func newHarnessWith(t *testing.T, withSelf bool) *harness {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	tok, err := token.Generate()
	if err != nil {
		t.Fatalf("token.Generate() error = %v", err)
	}

	p := &store.Probe{
		ProbeID:           "hx-sy01-a83f21",
		TokenHash:         token.Hash(tok),
		CampusCode:        "hx",
		CampusName:        "虎溪",
		BuildingGroupCode: "sy",
		BuildingGroupName: "松园",
		BuildingCode:      "sy01",
		BuildingName:      "松园一栋",
		NetworkType:       "wired",
		Enabled:           true,
	}
	if err := st.CreateProbe(p); err != nil {
		t.Fatalf("CreateProbe() error = %v", err)
	}

	var self *metrics.Self
	if withSelf {
		self = metrics.NewSelf(prometheus.NewRegistry())
	}

	// The harness must exist before the Server so the Now closure can read the
	// mutable clock field instead of capturing a copy of a local variable: a
	// frozen clock makes the "advance the clock" tests vacuous.
	h := &harness{
		store:  st,
		latest: latest.New(),
		self:   self,
		probe:  p,
		tok:    tok,
		clock:  baseClock,
	}
	h.server = NewServer(Deps{
		Store:   st,
		Latest:  h.latest,
		Self:    self,
		Limiter: NewLimiter(5*time.Second, 3),
		Now:     func() time.Time { return h.clock },
	})
	return h
}

func (h *harness) do(t *testing.T, method, body, contentType, auth string) *httptest.ResponseRecorder {
	t.Helper()
	return h.doFrom(t, defaultTestIP, method, body, contentType, auth)
}

// defaultTestIP is the RemoteAddr host httptest.NewRequest sets, so every
// request the plain do helper makes shares one source address.
const defaultTestIP = "192.0.2.1"

func (h *harness) doFrom(t *testing.T, ip, method, body, contentType, auth string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/push", strings.NewReader(body))
	req.RemoteAddr = net.JoinHostPort(ip, "12345")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)
	return rec
}

const goodBody = `{
  "version": 1,
  "timestamp": 1700000000,
  "probe_version": "0.1.0",
  "results": {
    "aliyun_dns": {"icmp": {"success": true, "sent": 5, "received": 5, "loss_ratio": 0.0,
      "min_rtt_ms": 10.2, "avg_rtt_ms": 12.3, "max_rtt_ms": 15.8, "jitter_ms": 1.4}},
    "campus_dns": {"dns": {"success": true, "duration_ms": 8.4}},
    "cqu_mirror": {"http": {"success": true, "status_code": 200, "duration_ms": 51.2}}
  }
}`

func (h *harness) bearer() string { return "Bearer " + h.tok }

func TestPushAcceptsValidRequest(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, goodBody, "application/json", h.bearer())

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("204 response must have no body, got %q", rec.Body.String())
	}

	entry, ok := h.latest.Get("hx-sy01-a83f21")
	if !ok {
		t.Fatal("latest measurement was not stored")
	}
	// last_seen comes from the server clock, never from the body's "timestamp"
	// (Protocol v1 §23). goodBody's timestamp differs from the clock, so a
	// handler that echoed it would fail here.
	if !entry.ServerReceivedAt.Equal(h.clock) {
		t.Errorf("ServerReceivedAt = %v, want server time %v", entry.ServerReceivedAt, h.clock)
	}
}

func TestPushAcceptsCharsetParameter(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, goodBody, "application/json; charset=utf-8", h.bearer())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestPushRejectsWrongMethod(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodGet, "", "application/json", h.bearer())
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_request") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestPushRejectsWrongContentType(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, goodBody, "text/plain", h.bearer())
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
}

func TestPushRejectsOversizedBody(t *testing.T) {
	h := newHarness(t)
	big := `{"version":1,"pad":"` + strings.Repeat("x", MaxBodyBytes) + `"}`
	rec := h.do(t, http.MethodPost, big, "application/json", h.bearer())
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_request") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

// TestPushRejectsOversizedBodyViaLazyPath covers the MaxBytesReader path rather
// than BodyLimit's eager Content-Length pre-check. The body is malformed JSON,
// so if the handler's *http.MaxBytesError branch were removed (or ordered after
// the decode) this request would answer 400 invalid_json instead of 413 —
// Protocol v1 §20 requires 413 to win.
func TestPushRejectsOversizedBodyViaLazyPath(t *testing.T) {
	h := newHarness(t)
	big := `{"version":1,"pad":"` + strings.Repeat("x", MaxBodyBytes) + `"`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", h.bearer())
	// httptest.NewRequest fills ContentLength in from the *strings.Reader, which
	// would let BodyLimit short-circuit on the header without ever installing
	// http.MaxBytesReader. -1 forces the lazy path.
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", rec.Code, rec.Body.String())
	}
}

func TestPushRejectsMissingToken(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, goodBody, "application/json", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unauthorized") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestPushRejectsMalformedAuthorization(t *testing.T) {
	h := newHarness(t)
	for _, auth := range []string{
		h.tok,             // no scheme
		"Basic " + h.tok,  // wrong scheme
		"Bearer",          // no credential
		"Bearer ",         // empty credential
		"bearer " + h.tok, // scheme is case-sensitive per Protocol v1
	} {
		t.Run(auth, func(t *testing.T) {
			rec := h.do(t, http.MethodPost, goodBody, "application/json", auth)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestPushRejectsUnknownToken(t *testing.T) {
	h := newHarness(t)
	other, _ := token.Generate()
	rec := h.do(t, http.MethodPost, goodBody, "application/json", "Bearer "+other)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPushRejectsDisabledProbe(t *testing.T) {
	h := newHarness(t)
	if err := h.store.SetProbeEnabled("hx-sy01-a83f21", false); err != nil {
		t.Fatalf("SetProbeEnabled() error = %v", err)
	}
	rec := h.do(t, http.MethodPost, goodBody, "application/json", h.bearer())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "probe_disabled") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestPushRejectsBadJSON(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, `{"version": 1,`, "application/json", h.bearer())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_json") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestPushRejectsUnsupportedVersion(t *testing.T) {
	h := newHarness(t)
	body := strings.Replace(goodBody, `"version": 1`, `"version": 2`, 1)
	rec := h.do(t, http.MethodPost, body, "application/json", h.bearer())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unsupported_version") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestPushRejectsUnknownTarget(t *testing.T) {
	h := newHarness(t)
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"nope":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	rec := h.do(t, http.MethodPost, body, "application/json", h.bearer())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_target") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestPushRejectsDisallowedProbeType(t *testing.T) {
	h := newHarness(t)
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"cqu_mirror":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	rec := h.do(t, http.MethodPost, body, "application/json", h.bearer())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_probe_type") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestPushRejectsInvalidMeasurement(t *testing.T) {
	h := newHarness(t)
	// received=0 but success=true violates Protocol v1 §7.
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"aliyun_dns":{"icmp":{"success":true,"sent":5,"received":0,"loss_ratio":1.0,"min_rtt_ms":null,"avg_rtt_ms":null,"max_rtt_ms":null,"jitter_ms":null}}}}`
	rec := h.do(t, http.MethodPost, body, "application/json", h.bearer())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_payload") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestPushRejectsEmptyResults(t *testing.T) {
	h := newHarness(t)
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{}}`
	rec := h.do(t, http.MethodPost, body, "application/json", h.bearer())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	// Design doc §6.4/§7.1: an empty results object is a payload error, not a
	// generic bad request.
	if !strings.Contains(rec.Body.String(), "invalid_payload") {
		t.Errorf("body = %s, want invalid_payload", rec.Body.String())
	}
}

func TestPushRateLimited(t *testing.T) {
	h := newHarness(t)
	// burst is 3; the first three succeed and the fourth is throttled.
	for i := 0; i < 3; i++ {
		if rec := h.do(t, http.MethodPost, goodBody, "application/json", h.bearer()); rec.Code != http.StatusNoContent {
			t.Fatalf("push %d status = %d, want 204", i+1, rec.Code)
		}
	}
	rec := h.do(t, http.MethodPost, goodBody, "application/json", h.bearer())
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "rate_limited") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestPushInvalidRequestDoesNotRefreshLastSeen(t *testing.T) {
	h := newHarness(t)

	// A successful push establishes a baseline.
	if rec := h.do(t, http.MethodPost, goodBody, "application/json", h.bearer()); rec.Code != http.StatusNoContent {
		t.Fatalf("baseline push status = %d", rec.Code)
	}
	base, _ := h.latest.Get("hx-sy01-a83f21")

	// Advance the clock, then send an invalid push.
	h.clock = h.clock.Add(60 * time.Second)
	bad := strings.Replace(goodBody, `"sent": 5`, `"sent": 0`, 1)
	if rec := h.do(t, http.MethodPost, bad, "application/json", h.bearer()); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid push status = %d, want 400", rec.Code)
	}

	after, _ := h.latest.Get("hx-sy01-a83f21")
	if !after.ServerReceivedAt.Equal(base.ServerReceivedAt) {
		t.Fatal("an invalid push refreshed last_seen; Protocol v1 §23 forbids this")
	}
}

func TestPushUnauthenticatedRequestDoesNotRefreshLastSeen(t *testing.T) {
	h := newHarness(t)
	if rec := h.do(t, http.MethodPost, goodBody, "application/json", h.bearer()); rec.Code != http.StatusNoContent {
		t.Fatalf("baseline push status = %d", rec.Code)
	}
	base, _ := h.latest.Get("hx-sy01-a83f21")

	h.clock = h.clock.Add(60 * time.Second)
	if rec := h.do(t, http.MethodPost, goodBody, "application/json", "Bearer bogus"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad-token status = %d, want 401", rec.Code)
	}

	after, _ := h.latest.Get("hx-sy01-a83f21")
	if !after.ServerReceivedAt.Equal(base.ServerReceivedAt) {
		t.Fatal("an unauthenticated push refreshed last_seen")
	}
}

// TestPushAuthFailureThrottlePinsBudget drives the per-IP failure budget past
// its burst from a single address. Without it, moving the throttle back above
// the store lookup — or deleting it — would leave every other test green.
func TestPushAuthFailureThrottlePinsBudget(t *testing.T) {
	h := newHarness(t)
	guessed, err := token.Generate()
	if err != nil {
		t.Fatalf("token.Generate() error = %v", err)
	}

	// Guesses inside the budget are ordinary 401 credential failures.
	for i := 1; i <= authFailureBurst; i++ {
		rec := h.do(t, http.MethodPost, goodBody, "application/json", "Bearer "+guessed)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d status = %d, want 401; body = %s", i, rec.Code, rec.Body.String())
		}
	}
	// The guess that exhausts the budget is answered 429, not 401: the address
	// is out of allowance, which is not the same as a malformed credential.
	rec := h.do(t, http.MethodPost, goodBody, "application/json", "Bearer "+guessed)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("guess past the budget: status = %d, want 429; body = %s", rec.Code, rec.Body.String())
	}

	// Guesses inside the budget are ordinary invalid tokens; only the one past
	// the budget is reported as throttled (design doc §10.2).
	if got := testutil.ToFloat64(h.self.AuthFailed.WithLabelValues("invalid_token")); got != authFailureBurst {
		t.Errorf("auth_failed_total{reason=invalid_token} = %v, want %d", got, authFailureBurst)
	}
	// The 429 is a rate-limit rejection, so it lands in push_rejected_total
	// alongside the per-probe bucket's 429s, not in the auth-failure counter.
	if got := testutil.ToFloat64(h.self.PushRejected.WithLabelValues("rate_limited")); got != 1 {
		t.Errorf("push_rejected_total{reason=rate_limited} = %v, want 1", got)
	}

	// §10.2's core promise: the failure budget is per-IP and is never charged by
	// traffic that authenticates. An address with a clean history is unaffected
	// by another address's guessing and is accepted.
	rec = h.doFrom(t, "192.0.2.7", http.MethodPost, goodBody, "application/json", h.bearer())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("valid token from a clean IP: status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
	if got := testutil.ToFloat64(h.self.AuthFailed.WithLabelValues("invalid_token")); got != authFailureBurst {
		t.Errorf("a successful push was charged to the failure budget: invalid_token = %v, want %d", got, authFailureBurst)
	}
}

// TestPushValidTokenIsNeverChargedToTheFailureBudget pins the deliberate
// placement of AllowAuthFailure: it is consulted only once a token has failed
// to resolve, because campus probes share NAT addresses and charging the IP
// budget up front would throttle authenticated traffic. More valid pushes than
// the burst are sent from one address; none may be answered 401 (they are
// answered 204 or 429 by the per-probe bucket, which is keyed on the probe).
func TestPushValidTokenIsNeverChargedToTheFailureBudget(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= authFailureBurst+5; i++ {
		rec := h.do(t, http.MethodPost, goodBody, "application/json", h.bearer())
		if rec.Code == http.StatusUnauthorized {
			t.Fatalf("push %d with a VALID token was rejected as unauthenticated; "+
				"the per-IP failure budget must never be charged by a successful authentication", i)
		}
	}
	if got := testutil.ToFloat64(h.self.AuthFailed.WithLabelValues("rate_limited")); got != 0 {
		t.Errorf("auth_failed_total{reason=rate_limited} = %v, want 0", got)
	}
}

// TestPushBlockedIPFailsFastBeforeLookup covers AuthFailureBlocked: once an
// address has exhausted its budget its requests are answered before the store
// is touched, so a guessing flood costs no database work. The store is closed
// to prove the absence of a lookup — a lookup would answer 503, not 401.
func TestPushBlockedIPFailsFastBeforeLookup(t *testing.T) {
	h := newHarness(t)
	guessed, err := token.Generate()
	if err != nil {
		t.Fatalf("token.Generate() error = %v", err)
	}
	// Guesses inside the budget are ordinary 401 credential failures.
	for i := 1; i <= authFailureBurst; i++ {
		if rec := h.do(t, http.MethodPost, goodBody, "application/json", "Bearer "+guessed); rec.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d status = %d, want 401", i, rec.Code)
		}
	}
	// The guess that exhausts the budget flips the address to rate-limited.
	if rec := h.do(t, http.MethodPost, goodBody, "application/json", "Bearer "+guessed); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("guess past the budget: status = %d, want 429", rec.Code)
	}
	_ = h.store.Close()

	// The blocked address is turned away fast even with a VALID token. The
	// status is 429, not 401: the request authenticates fine, the address is
	// what is blocked. Answering 401 would tell a probe its good credential is
	// bad and make an operator rotate a healthy token.
	rec := h.do(t, http.MethodPost, goodBody, "application/json", h.bearer())
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked IP: status = %d, want 429; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rate_limited") {
		t.Errorf("body = %s, want the rate_limited code", rec.Body.String())
	}

	// A different address still reaches the store, so the block is scoped to
	// the address that did the guessing.
	rec = h.doFrom(t, "192.0.2.9", http.MethodPost, goodBody, "application/json", h.bearer())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unblocked IP: status = %d, want 503 from the closed store", rec.Code)
	}
}

// TestPushWithoutSelfMetricsServesTraffic covers the documented contract that
// Deps.Self is optional: Routes skips RouteMetrics when it is nil, so the
// handler must not dereference it either.
func TestPushWithoutSelfMetricsServesTraffic(t *testing.T) {
	h := newHarnessWithoutSelf(t)

	rec := h.do(t, http.MethodPost, goodBody, "application/json", h.bearer())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("accepted push without Self: status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}

	rec = h.do(t, http.MethodPost, `{`, "application/json", h.bearer())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rejected push without Self: status = %d, want 400", rec.Code)
	}

	other, err := token.Generate()
	if err != nil {
		t.Fatalf("token.Generate() error = %v", err)
	}
	rec = h.do(t, http.MethodPost, goodBody, "application/json", "Bearer "+other)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad-token push without Self: status = %d, want 401", rec.Code)
	}
}

func TestPushErrorBodyNeverContainsToken(t *testing.T) {
	h := newHarness(t)
	// Drive every rejection path and check the token never appears.
	bad := []string{`{`, strings.Replace(goodBody, `"version": 1`, `"version": 9`, 1)}
	for _, body := range bad {
		rec := h.do(t, http.MethodPost, body, "application/json", h.bearer())
		if strings.Contains(rec.Body.String(), h.tok) {
			t.Fatalf("response body leaked the token: %s", rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), token.Hash(h.tok)) {
			t.Fatal("response body leaked the token hash")
		}
	}
}

// TestWriteProtocolErrorIgnoresErrorMessage is the leak guard that
// TestPushErrorBodyNeverContainsToken cannot be: that test only inspects a body
// produced by paths whose error values carry no secret, so it would still pass
// if writeProtocolError interpolated err.Message. This poisons the error with a
// secret and asserts the response body is the fixed code and message only.
func TestWriteProtocolErrorIgnoresErrorMessage(t *testing.T) {
	const secret = "cqu_probe_SUPERSECRETTOKENVALUE"
	rec := httptest.NewRecorder()
	writeProtocolError(rec, &protocol.Error{
		Code:    protocol.CodeInternalError,
		Message: "SELECT * FROM probes WHERE token_hash = '" + secret + "'",
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), "SELECT") {
		t.Fatalf("response leaked the error message: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "internal_error") {
		t.Errorf("body = %s, want the fixed internal_error code", rec.Body.String())
	}
}

// TestPushWrongMethodIsCountedInRouteMetrics pins the 405 fallback's place
// inside RouteMetrics. Registered outside it, every method-not-allowed request
// would be invisible in http_requests_total: an operator would see a probe
// fleet's misconfigured method and a healthy request graph at the same time.
func TestPushWrongMethodIsCountedInRouteMetrics(t *testing.T) {
	h := newHarness(t)
	if rec := h.do(t, http.MethodGet, "", "application/json", h.bearer()); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}

	want := `
# HELP cqu_netprobe_gateway_http_requests_total HTTP requests by method, route pattern and status.
# TYPE cqu_netprobe_gateway_http_requests_total counter
cqu_netprobe_gateway_http_requests_total{method="GET",path="/api/v1/push",status="405"} 1
`
	if err := testutil.CollectAndCompare(h.self.HTTPRequests, strings.NewReader(want)); err != nil {
		t.Fatal(err)
	}
}

func TestPushRecordsSelfMetrics(t *testing.T) {
	h := newHarness(t)

	// Two accepted, then one bad JSON, one bad token.
	for i := 0; i < 2; i++ {
		if rec := h.do(t, http.MethodPost, goodBody, "application/json", h.bearer()); rec.Code != http.StatusNoContent {
			t.Fatalf("push %d status = %d", i+1, rec.Code)
		}
	}
	h.do(t, http.MethodPost, `{`, "application/json", h.bearer())
	h.do(t, http.MethodPost, goodBody, "application/json", "Bearer bogus")

	if got := testutil.ToFloat64(h.self.PushTotal); got != 2 {
		t.Errorf("PushTotal = %v, want 2", got)
	}

	wantRejected := `
# HELP cqu_netprobe_gateway_push_rejected_total Rejected push requests by reason.
# TYPE cqu_netprobe_gateway_push_rejected_total counter
cqu_netprobe_gateway_push_rejected_total{reason="invalid_json"} 1
`
	if err := testutil.CollectAndCompare(h.self.PushRejected, strings.NewReader(wantRejected)); err != nil {
		t.Fatal(err)
	}

	wantAuth := `
# HELP cqu_netprobe_gateway_auth_failed_total Failed authentication attempts by reason.
# TYPE cqu_netprobe_gateway_auth_failed_total counter
cqu_netprobe_gateway_auth_failed_total{reason="invalid_token"} 1
`
	if err := testutil.CollectAndCompare(h.self.AuthFailed, strings.NewReader(wantAuth)); err != nil {
		t.Fatal(err)
	}
}

func TestPushAcceptsContentLengthAtLimit(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push", strings.NewReader(goodBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", h.bearer())
	req.ContentLength = MaxBodyBytes
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; Content-Length equal to the limit must be allowed", rec.Code)
	}
}

func TestPushRejectsContentLengthOverLimit(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push", strings.NewReader(goodBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", h.bearer())
	req.ContentLength = MaxBodyBytes + 1
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestPushReturns503WhenStoreUnavailable(t *testing.T) {
	h := newHarness(t)
	// Closing the database makes every lookup fail. The gateway must answer
	// 503, not 401: telling a probe its token is bad would make it discard a
	// credential that is actually fine.
	_ = h.store.Close()

	rec := h.do(t, http.MethodPost, goodBody, "application/json", h.bearer())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "service_unavailable") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestTargetsRequiresAuth(t *testing.T) {
	h := newHarness(t)

	get := func(bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
		if bearer != "" {
			req.Header.Set("Authorization", bearer)
		}
		rec := httptest.NewRecorder()
		h.server.Routes().ServeHTTP(rec, req)
		return rec
	}

	if rec := get(""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}
	for _, malformed := range []string{h.tok, "Basic " + h.tok, "Bearer "} {
		if rec := get(malformed); rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status = %d, want 401", malformed, rec.Code)
		}
	}
	other, _ := token.Generate()
	if rec := get("Bearer " + other); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token: status = %d, want 401", rec.Code)
	}
}

func TestTargetsRejectsDisabledProbe(t *testing.T) {
	h := newHarness(t)
	if err := h.store.SetProbeEnabled("hx-sy01-a83f21", false); err != nil {
		t.Fatalf("SetProbeEnabled() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
	req.Header.Set("Authorization", "Bearer "+h.tok)
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "probe_disabled") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestTargetsWrongMethod(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/targets", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+h.tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_request") {
		t.Errorf("405 must use the JSON envelope, got %s", rec.Body.String())
	}
}

// TestTargetsShapeAndFiltering checks the whole contract in one place: the
// response shape, that probe types are grouped and sorted, and — most
// importantly — that a target without an address is withheld. An addressless
// target dispatched to a probe would make every probe fail on every cycle
// against something the operator has not filled in yet.
func TestTargetsShapeAndFiltering(t *testing.T) {
	h := newHarness(t)

	// The harness's seeded set: campus_dns has no address, cqu_mirror does.
	// Add one more of each kind to be sure the filtering is not accidental.
	if err := h.store.CreateTarget(&store.Target{
		TargetID: "with_addr", Address: "192.0.2.1", Enabled: true,
		ProbeTypes: []string{"http", "icmp"},
	}); err != nil {
		t.Fatalf("CreateTarget(with_addr) error = %v", err)
	}
	if err := h.store.CreateTarget(&store.Target{
		TargetID: "no_addr", Address: "   ", Enabled: true, ProbeTypes: []string{"icmp"},
	}); err != nil {
		t.Fatalf("CreateTarget(no_addr) error = %v", err)
	}
	if err := h.store.CreateTarget(&store.Target{
		TargetID: "off", Address: "192.0.2.2", Enabled: false, ProbeTypes: []string{"icmp"},
	}); err != nil {
		t.Fatalf("CreateTarget(off) error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
	req.Header.Set("Authorization", "Bearer "+h.tok)
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	var got struct {
		Version int `json:"version"`
		Targets []struct {
			TargetID   string   `json:"target_id"`
			Address    string   `json:"address"`
			ProbeTypes []string `json:"probe_types"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v\n%s", err, rec.Body.String())
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}

	byID := map[string][]string{}
	for _, e := range got.Targets {
		byID[e.TargetID] = e.ProbeTypes
		if e.Address == "" {
			t.Errorf("target %s was dispatched with an empty address", e.TargetID)
		}
	}

	if _, ok := byID["campus_dns"]; ok {
		t.Error("campus_dns has no address and must not be dispatched")
	}
	if _, ok := byID["no_addr"]; ok {
		t.Error("a whitespace-only address must not be dispatched")
	}
	if _, ok := byID["off"]; ok {
		t.Error("a disabled target must not be dispatched")
	}
	if types, ok := byID["cqu_mirror"]; !ok || len(types) != 1 || types[0] != "http" {
		t.Errorf("cqu_mirror = %v, want [http]", types)
	}
	if types, ok := byID["with_addr"]; !ok || len(types) != 2 || types[0] != "http" || types[1] != "icmp" {
		t.Errorf("with_addr = %v, want [http icmp] sorted", types)
	}

	// The body must not leak page-only fields.
	if strings.Contains(rec.Body.String(), "display_name") || strings.Contains(rec.Body.String(), "description") {
		t.Errorf("the response carries page-only fields: %s", rec.Body.String())
	}
}

// TestTargetsSharesTheAuthFailureThrottle pins the reason the endpoint reuses
// authenticate(): without it, a guessing attacker would move here, where the
// per-probe push bucket does not apply, and get unlimited attempts.
// The parameters a probe receives are the ones an administrator saved, not the
// compiled-in constants. Reading them per request rather than caching at startup
// is what makes an edit take effect on the next refresh with nothing restarted,
// and this is the seam where a cache would hide.
func TestTargetsDispatchTheStoredMeasurementConfig(t *testing.T) {
	h := newHarness(t)

	custom := protocol.DefaultMeasurementConfig()
	custom.IntervalMS = 30000
	custom.ICMP.Count = 10
	custom.HTTP.FollowRedirects = false
	custom.DNS.TimeoutMS = 1500
	if err := h.store.SetMeasurementConfig(custom); err != nil {
		t.Fatalf("SetMeasurementConfig() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
	req.Header.Set("Authorization", "Bearer "+h.tok)
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Config protocol.MeasurementConfig `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body.Config != custom {
		t.Errorf("dispatched %+v, want the stored %+v", body.Config, custom)
	}
}

// Behind a reverse proxy every request arrives from the proxy, so an
// address-keyed limit collapses into a single global bucket: one attacker's
// guessing exhausts it and the whole fleet is throttled. Declaring the proxy
// trusted is what restores one bucket per client — and the first half of this
// test is the failure being prevented.
func TestAuthFailureThrottleSeparatesClientsBehindATrustedProxy(t *testing.T) {
	exhaust := func(t *testing.T, h *harness, clientIP string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/push", strings.NewReader(goodBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer cqu_probe_wrong")
		req.RemoteAddr = "127.0.0.1:5000" // the proxy
		req.Header.Set("X-Forwarded-For", clientIP)

		code := 0
		for i := 0; i < authFailureBurst+5; i++ {
			rec := httptest.NewRecorder()
			h.server.Routes().ServeHTTP(rec, req)
			code = rec.Code
			if code == http.StatusTooManyRequests {
				break
			}
		}
		return code
	}
	// A well-formed push from the same proxy but a different client.
	push := func(t *testing.T, h *harness, clientIP string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/push", strings.NewReader(goodBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", h.bearer())
		req.RemoteAddr = "127.0.0.1:5000"
		req.Header.Set("X-Forwarded-For", clientIP)
		rec := httptest.NewRecorder()
		h.server.Routes().ServeHTTP(rec, req)
		return rec.Code
	}

	t.Run("without a trusted proxy the header is ignored", func(t *testing.T) {
		h := newHarness(t)
		if got := exhaust(t, h, "203.0.113.9"); got != http.StatusTooManyRequests {
			t.Fatalf("guessing was never throttled: last status %d", got)
		}
		// A different claimed client is the same peer address, so it inherits
		// the exhausted budget. This is the deployment the header cannot fix.
		if got := push(t, h, "198.51.100.4"); got != http.StatusTooManyRequests {
			t.Errorf("second client = %d, want 429 (one bucket for the proxy)", got)
		}
	})

	t.Run("with a trusted proxy each client keeps its own budget", func(t *testing.T) {
		h := newHarness(t)
		_, proxy, err := net.ParseCIDR("127.0.0.0/8")
		if err != nil {
			t.Fatal(err)
		}
		h.server.trustedProxies = []*net.IPNet{proxy}

		if got := exhaust(t, h, "203.0.113.9"); got != http.StatusTooManyRequests {
			t.Fatalf("guessing was never throttled: last status %d", got)
		}
		if got := push(t, h, "203.0.113.9"); got != http.StatusTooManyRequests {
			t.Errorf("the guessing client = %d, want 429", got)
		}
		if got := push(t, h, "198.51.100.4"); got != http.StatusNoContent {
			t.Errorf("an innocent client behind the same proxy = %d, want 204: "+
				"one client's guessing must not throttle the rest", got)
		}
	})
}

// bodyWithConfig is goodBody with a config_id appended.
func bodyWithConfig(id string) string {
	return strings.Replace(goodBody, `"results"`, `"config_id": "`+id+`", "results"`, 1)
}

// The flow this exists for: an administrator edits the measurement parameters,
// and every probe still holding the old ones is told to come back with the new
// set — instead of having its payload judged against rules it never received.
func TestPushRejectsAStaleConfigAndAcceptsTheNewOne(t *testing.T) {
	h := newHarness(t)

	current := protocol.DefaultMeasurementConfig()
	stale := current
	stale.ICMP.Count = 10

	// A probe that already has the current config is served normally.
	rec := h.do(t, http.MethodPost, bodyWithConfig(current.ID()), "application/json", h.bearer())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("push with the current config = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}

	// The administrator changes the parameters.
	if err := h.store.SetMeasurementConfig(stale); err != nil {
		t.Fatalf("SetMeasurementConfig() error = %v", err)
	}

	// The same probe now holds a stale ID.
	rec = h.do(t, http.MethodPost, bodyWithConfig(current.ID()), "application/json", h.bearer())
	if rec.Code != http.StatusConflict {
		t.Fatalf("push with a stale config = %d, want 409; body = %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != protocol.CodeConfigStale {
		t.Errorf("error code = %q, want %q", code, protocol.CodeConfigStale)
	}

	// And once it re-fetches, it is accepted again. The ID comes from the
	// dispatch response, exactly as a probe would get it.
	rec = h.do(t, http.MethodPost, bodyWithConfig(stale.ID()), "application/json", h.bearer())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("push after re-fetching = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
}

// A rejected push must not look like an accepted one: last_seen is what the
// whole dashboard reads, and a probe measuring with parameters the gateway no
// longer dispatches was not accepted.
func TestStaleConfigDoesNotRefreshLastSeen(t *testing.T) {
	h := newHarness(t)

	stale := protocol.DefaultMeasurementConfig()
	stale.DNS.TimeoutMS = 2500
	if err := h.store.SetMeasurementConfig(stale); err != nil {
		t.Fatalf("SetMeasurementConfig() error = %v", err)
	}

	rec := h.do(t, http.MethodPost, bodyWithConfig(protocol.DefaultMeasurementConfig().ID()),
		"application/json", h.bearer())
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if _, ok := h.latest.Get(h.probe.ProbeID); ok {
		t.Error("a rejected push stored a measurement (Protocol v1 §23)")
	}
}

// A probe that predates config_id sends nothing, and the gateway has no way to
// tell it about a config it never received — so it must keep working.
func TestPushWithoutConfigIDIsUnaffected(t *testing.T) {
	h := newHarness(t)

	custom := protocol.DefaultMeasurementConfig()
	custom.DNS.TimeoutMS = 2500
	if err := h.store.SetMeasurementConfig(custom); err != nil {
		t.Fatalf("SetMeasurementConfig() error = %v", err)
	}

	rec := h.do(t, http.MethodPost, goodBody, "application/json", h.bearer())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("a probe without config_id = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
}

// errorCode pulls error.code out of an error response.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error response is not valid JSON: %v (%s)", err, rec.Body.String())
	}
	return body.Error.Code
}

func TestTargetsSharesTheAuthFailureThrottle(t *testing.T) {
	h := newHarness(t)
	guessed, err := token.Generate()
	if err != nil {
		t.Fatalf("token.Generate() error = %v", err)
	}

	get := func(bearer string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		h.server.Routes().ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 1; i <= authFailureBurst; i++ {
		if got := get(guessed); got != http.StatusUnauthorized {
			t.Fatalf("guess %d = %d, want 401", i, got)
		}
	}
	// The budget is spent, so even the correct token is turned away now.
	if got := get(h.tok); got != http.StatusTooManyRequests {
		t.Fatalf("after the budget: %d, want 429", got)
	}
}
