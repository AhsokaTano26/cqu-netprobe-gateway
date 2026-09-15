package api

import (
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
	"github.com/tano/cqu-netprobe-gateway/internal/store"
	"github.com/tano/cqu-netprobe-gateway/internal/token"
)

type harness struct {
	server *Server
	store  *store.Store
	latest *latest.Store
	self   *metrics.Self
	now    time.Time
	probe  *store.Probe
	tok    string
}

func newHarness(t *testing.T) *harness {
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

	l := latest.New()
	self := metrics.NewSelf(prometheus.NewRegistry())
	now := time.Unix(1789490000, 0).UTC()

	srv := NewServer(Deps{
		Store:   st,
		Latest:  l,
		Self:    self,
		Limiter: NewLimiter(5*time.Second, 3),
		Now:     func() time.Time { return now },
	})

	return &harness{server: srv, store: st, latest: l, self: self, now: now, probe: p, tok: tok}
}

func (h *harness) do(t *testing.T, method, body, contentType, auth string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/push", strings.NewReader(body))
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
  "timestamp": 1789490000,
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
	if !entry.ServerReceivedAt.Equal(h.now) {
		t.Errorf("ServerReceivedAt = %v, want server time %v", entry.ServerReceivedAt, h.now)
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
	h.now = h.now.Add(60 * time.Second)
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

	h.now = h.now.Add(60 * time.Second)
	if rec := h.do(t, http.MethodPost, goodBody, "application/json", "Bearer bogus"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad-token status = %d, want 401", rec.Code)
	}

	after, _ := h.latest.Get("hx-sy01-a83f21")
	if !after.ServerReceivedAt.Equal(base.ServerReceivedAt) {
		t.Fatal("an unauthenticated push refreshed last_seen")
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
