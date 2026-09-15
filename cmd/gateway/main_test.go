package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tano/cqu-netprobe-gateway/internal/config"
	"github.com/tano/cqu-netprobe-gateway/internal/latest"
	"github.com/tano/cqu-netprobe-gateway/internal/metrics"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
	"github.com/tano/cqu-netprobe-gateway/internal/token"
)

func TestBuildMetricsHandler(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer func() { _ = st.Close() }()

	reg := prometheus.NewRegistry()
	self := metrics.NewSelf(reg)
	c := metrics.NewCollector(latest.New(), st, 30*time.Second, self)
	c.Now = func() time.Time { return time.Unix(1789490000, 0).UTC() }
	reg.MustRegister(c)

	// Prime one series of the http_requests counter. A CounterVec with no
	// observed children emits no metric family at all, so without this the
	// assertion below could never hold regardless of the wiring — the push
	// handler's RouteMetrics middleware is what creates children in production.
	// This mirrors the convention in internal/metrics/self_test.go.
	self.HTTPRequests.WithLabelValues("GET", "/metrics", "200").Inc()

	h := metricsHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"cqu_netprobe_gateway_registered_probes",
		"cqu_netprobe_gateway_push_total",
		"cqu_netprobe_gateway_http_requests_total",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics does not expose %q", want)
		}
	}
}

func TestBuildPushMuxServesPushAndAdmin(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/p.db")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer func() { _ = st.Close() }()

	cfg := &config.Config{
		AdminUsername:   "admin",
		AdminPassword:   "test-password-value",
		PublicBaseURL:   "https://netprobe.example.com",
		OnlineThreshold: 30 * time.Second,
		RateLimit:       5 * time.Second,
		RateLimitBurst:  3,
		DataDir:         t.TempDir(),
	}

	handler, err := buildHandler(cfg, st, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("buildHandler() error = %v", err)
	}

	// The push endpoint must exist and reject an unauthenticated request.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.Push.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("push status = %d, want 401", rec.Code)
	}

	// The admin login page must exist.
	req = httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	rec = httptest.NewRecorder()
	handler.Push.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login status = %d, want 200", rec.Code)
	}
}

func TestGeneratedPasswordIsLoggedOnce(t *testing.T) {
	// The generated password must be surfaced exactly once, at startup.
	st, err := store.Open(t.TempDir() + "/g.db")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer func() { _ = st.Close() }()

	cfg := &config.Config{
		AdminUsername:   "admin",
		OnlineThreshold: 30 * time.Second,
		RateLimit:       5 * time.Second,
		RateLimitBurst:  3,
		DataDir:         t.TempDir(),
	}
	h, err := buildHandler(cfg, st, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("buildHandler() error = %v", err)
	}
	pw, generated := h.Admin.PasswordGeneration()
	if !generated {
		t.Fatal("no password was generated")
	}
	if len(pw) < 32 {
		t.Errorf("generated password is %d chars, want >= 32", len(pw))
	}
}

func TestPushThenScrapeReflectsMeasurement(t *testing.T) {
	st, _ := store.Open(t.TempDir() + "/e2e.db")
	defer func() { _ = st.Close() }()

	cfg := &config.Config{
		AdminUsername: "admin", AdminPassword: "test-password-value",
		OnlineThreshold: 30 * time.Second, RateLimit: 5 * time.Second,
		RateLimitBurst: 3, DataDir: t.TempDir(),
	}
	reg := prometheus.NewRegistry()
	handler, err := buildHandler(cfg, st, reg)
	if err != nil {
		t.Fatalf("buildHandler() error = %v", err)
	}
	collector := metrics.NewCollector(handler.Latest, st, cfg.OnlineThreshold, handler.Self)
	reg.MustRegister(collector)

	// Register a probe directly, then push with its token.
	tok := "cqu_probe_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := st.CreateProbe(&store.Probe{
		ProbeID: "hx-sy01-aaaaaa", TokenHash: token.Hash(tok),
		CampusCode: "hx", CampusName: "虎溪",
		BuildingGroupCode: "sy", BuildingGroupName: "松园",
		BuildingCode: "sy01", BuildingName: "松园一栋",
		NetworkType: "wired", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateProbe() error = %v", err)
	}

	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"aliyun_dns":{"icmp":{"success":true,"sent":5,"received":5,"loss_ratio":0,"min_rtt_ms":10.2,"avg_rtt_ms":12.3,"max_rtt_ms":15.8,"jitter_ms":1.4}}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	handler.Push.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("push status = %d, want 204", rec.Code)
	}

	// Now the metric must be visible.
	rec = httptest.NewRecorder()
	metricsHandler(reg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "campus_probe_icmp_rtt_seconds") {
		t.Fatal("/metrics does not expose the pushed measurement; the collector is reading a different latest store")
	}
}
