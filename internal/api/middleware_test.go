package api

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/tano/cqu-netprobe-gateway/internal/metrics"
)

func TestWriteErrorShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusBadRequest, "invalid_payload", "invalid measurement payload")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body.Error.Code != "invalid_payload" {
		t.Errorf("code = %q", body.Error.Code)
	}
	if body.Error.Message == "" {
		t.Error("message is empty")
	}
}

func TestWriteErrorNeverLeaks(t *testing.T) {
	// Any string that looks like a secret must not survive into a response.
	const secret = "cqu_probe_SUPERSECRETTOKENVALUE"
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusInternalServerError, "internal_error", errInternal)

	if strings.Contains(rec.Body.String(), secret) {
		t.Fatal("error response contains a token")
	}
	if strings.Contains(rec.Body.String(), "SELECT") || strings.Contains(rec.Body.String(), "goroutine") {
		t.Fatal("error response contains internal detail")
	}
}

func TestBodyLimitAcceptsSmallBody(t *testing.T) {
	var got string
	h := BodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/push", strings.NewReader("hello"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got != "hello" {
		t.Errorf("body = %q", got)
	}
}

func TestBodyLimitRejectsOversizedBody(t *testing.T) {
	h := BodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request", "request body too large")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	big := strings.Repeat("x", MaxBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push", strings.NewReader(big))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestBodyLimitAcceptsExactlyMaxBody(t *testing.T) {
	h := BodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request", "request body too large")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	exact := strings.Repeat("x", MaxBodyBytes)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push", strings.NewReader(exact))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 for a body of exactly 64 KiB", rec.Code)
	}
}

func TestRequireJSON(t *testing.T) {
	ok := RequireJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	cases := []struct {
		contentType string
		want        int
	}{
		{"application/json", http.StatusNoContent},
		{"application/json; charset=utf-8", http.StatusNoContent},
		{"application/json;charset=utf-8", http.StatusNoContent},
		{"APPLICATION/JSON", http.StatusNoContent},
		{"text/plain", http.StatusUnsupportedMediaType},
		{"application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{"", http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		t.Run(tc.contentType, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/push", nil)
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			ok.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestRouteMetricsUsesRoutePatternNotRawPath(t *testing.T) {
	reg := prometheus.NewRegistry()
	self := metrics.NewSelf(reg)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/push", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := RouteMetrics(self, mux)

	// A scanner probing random paths must not create one series per path.
	for _, p := range []string{"/wp-admin", "/.env", "/etc/passwd"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/push", nil))

	// Exact label values are asserted, so a raw path leaking into the label is
	// a failure rather than merely a higher series count.
	want := `
# HELP cqu_netprobe_gateway_http_requests_total HTTP requests by method, route pattern and status.
# TYPE cqu_netprobe_gateway_http_requests_total counter
cqu_netprobe_gateway_http_requests_total{method="GET",path="unmatched",status="404"} 3
cqu_netprobe_gateway_http_requests_total{method="POST",path="/api/v1/push",status="204"} 1
`
	if err := testutil.CollectAndCompare(self.HTTPRequests, strings.NewReader(want)); err != nil {
		t.Fatal(err)
	}
}

func TestCIDRAllowlistEmptyPermitsLoopbackOnly(t *testing.T) {
	called := false
	h := CIDRAllowlist(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		remote string
		want   int
	}{
		{"127.0.0.1:1234", http.StatusOK},
		{"[::1]:1234", http.StatusOK},
		{"10.1.2.3:1234", http.StatusForbidden},
		{"192.168.1.1:1234", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.remote, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			req.RemoteAddr = tc.remote
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if called != (rec.Code == http.StatusOK) {
				t.Errorf("next handler called = %v, status = %d", called, rec.Code)
			}
		})
	}
}

func TestCIDRAllowlistWithNetworks(t *testing.T) {
	_, n, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	h := CIDRAllowlist([]*net.IPNet{n}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		remote string
		want   int
	}{
		{"10.1.2.3:1234", http.StatusOK},
		{"127.0.0.1:1234", http.StatusOK}, // loopback stays permitted
		{"192.168.1.1:1234", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.remote, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			req.RemoteAddr = tc.remote
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestClientIPStripsPort(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.1.2.3:4567"
	if got := ClientIP(req); got != "10.1.2.3" {
		t.Errorf("ClientIP() = %q, want 10.1.2.3", got)
	}
	req.RemoteAddr = "[2001:db8::1]:4567"
	if got := ClientIP(req); got != "2001:db8::1" {
		t.Errorf("ClientIP() = %q, want 2001:db8::1", got)
	}
}
