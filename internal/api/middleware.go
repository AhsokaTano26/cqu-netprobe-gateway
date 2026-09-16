package api

import (
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/tano/cqu-netprobe-gateway/internal/clientip"
	"github.com/tano/cqu-netprobe-gateway/internal/metrics"
)

// BodyLimit caps the request body at MaxBodyBytes. The limit is enforced
// lazily: http.MaxBytesReader only fails once the handler actually reads, and
// it surfaces *http.MaxBytesError. The push handler maps that to 413 ahead of
// any JSON error, which is what Protocol v1 §20 requires.
func BodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > MaxBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request", msgTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// RequireJSON rejects anything that is not application/json, with or without a
// charset parameter (Protocol v1 §4).
func RequireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if ct == "" {
			writeError(w, http.StatusUnsupportedMediaType, "invalid_request", msgMediaType)
			return
		}
		mediaType, _, err := mime.ParseMediaType(ct)
		if err != nil || mediaType != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "invalid_request", msgMediaType)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response status for metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap exposes the underlying ResponseWriter so http.ResponseController and
// http.MaxBytesReader can reach it.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// routeLabel is the path label for a request: the path portion of the ServeMux
// pattern that matched, or "unmatched" when nothing matched.
//
// A pattern may be method-qualified ("POST /api/v1/push"); the method already
// has its own label, so only the path part is used.
func routeLabel(pattern string) string {
	if pattern == "" {
		return "unmatched"
	}
	if _, path, ok := strings.Cut(pattern, " "); ok {
		return path
	}
	return pattern
}

// RouteMetrics counts requests by method, route pattern and status.
//
// The path label comes from r.Pattern — a registered route — not r.URL.Path.
// Using the raw path would let an internet scanner create unbounded series by
// requesting /wp-admin, /.env and similar.
func RouteMetrics(self *metrics.Self, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// r.Pattern is set by the mux during next.ServeHTTP, so it is read
		// afterwards. Its values are bounded by the route table.
		self.HTTPRequests.WithLabelValues(r.Method, routeLabel(r.Pattern), strconv.Itoa(rec.status)).Inc()
	})
}

// CIDRAllowlist restricts access to loopback plus the configured networks.
//
// An empty allowlist means "loopback only" rather than "everyone": forgetting
// to configure it must fail closed. See the design doc §12.2.
//
// trustedProxies is passed through to clientip.From, so a gateway behind a
// reverse proxy allowlists the scraper's real address rather than the proxy's.
func CIDRAllowlist(nets, trustedProxies []*net.IPNet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := net.ParseIP(clientip.From(r, trustedProxies))
		if ip == nil {
			writeError(w, http.StatusForbidden, "invalid_request", msgBadRequest)
			return
		}
		if ip.IsLoopback() {
			next.ServeHTTP(w, r)
			return
		}
		for _, n := range nets {
			if n.Contains(ip) {
				next.ServeHTTP(w, r)
				return
			}
		}
		writeError(w, http.StatusForbidden, "invalid_request", msgBadRequest)
	})
}
