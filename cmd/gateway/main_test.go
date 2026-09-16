package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"regexp"
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

// adminSessionCookieName mirrors the unexported constant in
// internal/admin/server.go. A rename there fails the login step below loudly
// instead of quietly skipping the authenticated request.
const adminSessionCookieName = "netprobe_admin_session"

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

// The spec is the only place /metrics is described, and it is the one path an
// OpenAPI tool will hand to someone who cannot read the deployment. So the
// documented path has to be the registered one, and the documented 403 has to
// be what a caller outside the allowlist actually gets.
func TestMetricsMuxServesTheDocumentedPath(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi.json")
	if err != nil {
		t.Fatalf("read docs/openapi.json: %v", err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Responses map[string]json.RawMessage `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("docs/openapi.json is not valid JSON: %v", err)
	}
	if len(spec.Paths) == 0 {
		t.Fatal("the spec documents no paths")
	}

	operations := spec.Paths["/metrics"]
	if len(operations) == 0 {
		t.Fatal("the spec documents no /metrics operation")
	}

	// The spec's key is the media-type-facing method name in lower case.
	op, ok := operations["get"]
	if !ok {
		t.Fatalf("/metrics is documented as %v, want get", operations)
	}
	if _, ok := op.Responses["403"]; !ok {
		t.Error("/metrics no longer documents the 403 an allowlisted-out caller receives")
	}

	// httptest gives every request a non-loopback RemoteAddr, so this is the
	// allowlisted-out caller the 403 above describes.
	cfg := &config.Config{MetricsAllowedCIDRs: nil, MaxProbes: 500}
	rec := httptest.NewRecorder()
	metricsMux(cfg, prometheus.NewRegistry()).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /metrics from outside the allowlist = %d, want 403", rec.Code)
	}
}

func TestBuildPushMuxServesPushAndAdmin(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/p.db")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer func() { _ = st.Close() }()

	cfg := &config.Config{
		MaxProbes:       500,
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

// TestAdminRenderReachesWiredLatestAndThreshold pins two wiring hazards that are
// otherwise invisible: a nil Deps.Latest and a dropped OnlineThreshold. Both are
// reachable only through an authenticated render of the dashboard, because
// requireSession redirects an anonymous visitor before any page is built, and
// handleProbeList touches latest/onlineThreshold only inside its per-probe loop.
//
// net/http recovers handler panics per connection, so the first hazard would
// leave the process up and only break authenticated admin pages — no other test
// in this package (or admin's) would notice.
func TestAdminRenderReachesWiredLatestAndThreshold(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/wire.db")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer func() { _ = st.Close() }()

	// Non-default on purpose: 30s is admin's own fallback, so asserting it would
	// not tell "wired through" apart from "silently fell back to the default".
	const onlineThreshold = 7 * time.Second
	cfg := &config.Config{
		MaxProbes:       500,
		AdminUsername:   "admin",
		AdminPassword:   "test-password-value",
		PublicBaseURL:   "https://netprobe.example.com",
		OnlineThreshold: onlineThreshold,
		RateLimit:       5 * time.Second,
		RateLimitBurst:  3,
		DataDir:         t.TempDir(),
	}

	handler, err := buildHandler(cfg, st, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("buildHandler() error = %v", err)
	}

	// Enabled is load-bearing: onlineState returns before touching the latest
	// store for a disabled probe, which would make the render below vacuous.
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

	// POST /admin/login is deliberately not wrapped in requireCSRF, so a session
	// is reachable without first scraping a token out of the login form.
	login := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(url.Values{
		"username": {"admin"},
		"password": {"test-password-value"},
	}.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.Push.ServeHTTP(rec, login)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == adminSessionCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("login did not set a %q cookie", adminSessionCookieName)
	}

	// The dashboard calls s.latest.Get for every enabled probe, so a nil Latest
	// panics here rather than at build time.
	dash := httptest.NewRequest(http.MethodGet, "/admin", nil)
	dash.AddCookie(session)
	rec = httptest.NewRecorder()
	handler.Push.ServeHTTP(rec, dash)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated GET /admin = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hx-sy01-aaaaaa") {
		t.Error("dashboard did not render the probe row, so the list never reached onlineState")
	}

	// onlineThreshold is unexported, so reflection is the only way to observe the
	// wiring. If the field is renamed this fails loudly instead of passing on a
	// zero value.
	field := reflect.ValueOf(handler.Admin).Elem().FieldByName("onlineThreshold")
	if !field.IsValid() {
		t.Fatal("admin.Server has no onlineThreshold field; update this test")
	}
	if got := time.Duration(field.Int()); got != onlineThreshold {
		t.Errorf("admin onlineThreshold = %s, want %s (the configured value, not the default)",
			got, onlineThreshold)
	}
}

// TestGeneratedPasswordIsGeneratedOnce pins the store-backed half of the
// "log it exactly once" contract: main logs the plaintext when
// PasswordGeneration reports one, so a restart that minted a fresh password
// would silently invalidate the copy the operator already saved.
func TestGeneratedPasswordIsGeneratedOnce(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/g.db")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer func() { _ = st.Close() }()

	cfg := &config.Config{
		MaxProbes:       500,
		AdminUsername:   "admin",
		OnlineThreshold: 30 * time.Second,
		RateLimit:       5 * time.Second,
		RateLimitBurst:  3,
		DataDir:         t.TempDir(),
	}
	first, err := buildHandler(cfg, st, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("buildHandler() error = %v", err)
	}
	pw, generated := first.Admin.PasswordGeneration()
	if !generated {
		t.Fatal("no password was generated")
	}
	if len(pw) < 32 {
		t.Errorf("generated password is %d chars, want >= 32", len(pw))
	}

	// The same store, constructed again: this is the restarted-process path, on
	// which the stored hash must be reused and no plaintext handed out.
	second, err := buildHandler(cfg, st, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("second buildHandler() error = %v", err)
	}
	again, generatedAgain := second.Admin.PasswordGeneration()
	if generatedAgain {
		t.Error("a new password was generated on restart; it must be generated once")
	}
	if again != "" {
		t.Errorf("PasswordGeneration() = %q on a restart, want no plaintext at all", again)
	}
}

func TestPushThenScrapeReflectsMeasurement(t *testing.T) {
	st, _ := store.Open(t.TempDir() + "/e2e.db")
	defer func() { _ = st.Close() }()

	cfg := &config.Config{
		MaxProbes:     500,
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

func TestPublicMuxServesBothUIsAndNotMetrics(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/mux.db")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer func() { _ = st.Close() }()

	cfg := &config.Config{
		MaxProbes:     500,
		AdminUsername: "admin", AdminPassword: "test-password-value",
		PublicBaseURL:   "https://netprobe.example.com",
		OnlineThreshold: 30 * time.Second, RateLimit: 5 * time.Second, RateLimitBurst: 3,
		RegisterLimit: time.Hour, RegisterLimitBurst: 3,
		DataDir: t.TempDir(),
	}
	handler, err := buildHandler(cfg, st, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("buildHandler() error = %v", err)
	}

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/", http.StatusOK},               // the public registration form
		{"/static/app.css", http.StatusOK}, // the shared stylesheet
		{"/admin/login", http.StatusOK},    // the admin UI
		{"/metrics", http.StatusNotFound},  // metrics stay on their own listener
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.Push.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.want {
				t.Fatalf("GET %s = %d, want %d", tc.path, rec.Code, tc.want)
			}
		})
	}
}

// TestSelfRegisteredProbeWorksImmediately is the end-to-end proof of what the
// public page promises: register, configure the token, measurements appear —
// with no approval step, no admin action, and no second visit. It replaces a
// test that asserted the opposite, back when self-registered probes started
// disabled; what bounds the anonymous path now is the probe cap, not a queue.
func TestSelfRegisteredProbeWorksImmediately(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/public.db")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer func() { _ = st.Close() }()

	cfg := &config.Config{
		MaxProbes:     500,
		AdminUsername: "admin", AdminPassword: "test-password-value",
		PublicBaseURL:   "https://netprobe.example.com",
		OnlineThreshold: 30 * time.Second, RateLimit: 5 * time.Second, RateLimitBurst: 3,
		RegisterLimit: time.Hour, RegisterLimitBurst: 3,
		DataDir: t.TempDir(),
	}
	reg := prometheus.NewRegistry()
	handler, err := buildHandler(cfg, st, reg)
	if err != nil {
		t.Fatalf("buildHandler() error = %v", err)
	}
	reg.MustRegister(metrics.NewCollector(handler.Latest, st, cfg.OnlineThreshold, handler.Self))

	// A catalog must exist before anything can be registered.
	if err := st.CreateCampus(&store.Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	if err := st.CreateBuilding(&store.Building{Code: "sy01", CampusCode: "hx",
		BuildingGroupCode: "sy", BuildingGroupName: "松园", Name: "松园一栋"}); err != nil {
		t.Fatalf("CreateBuilding() error = %v", err)
	}

	// 1. Register through the public page.
	form := url.Values{"campus_code": {"hx"}, "building_code": {"sy01"}, "network_type": {"wired"}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.Push.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("registration status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}

	// 2. The token page is public and shows a token exactly once.
	loc := rec.Header().Get("Location")
	tokenRec := httptest.NewRecorder()
	handler.Push.ServeHTTP(tokenRec, httptest.NewRequest(http.MethodGet, loc, nil))
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("token page status = %d, want 200", tokenRec.Code)
	}
	raw := regexp.MustCompile(`cqu_probe_[A-Za-z0-9_-]{43}`).FindString(tokenRec.Body.String())
	if raw == "" {
		t.Fatal("no token on the page")
	}
	replay := httptest.NewRecorder()
	handler.Push.ServeHTTP(replay, httptest.NewRequest(http.MethodGet, loc, nil))
	if replay.Code != http.StatusGone {
		t.Fatalf("replaying the slot = %d, want 410", replay.Code)
	}

	// 3. The probe is live at once and attributed to the public page.
	probes, err := st.ListProbes()
	if err != nil || len(probes) != 1 {
		t.Fatalf("ListProbes() = %v, %v; want exactly one probe", probes, err)
	}
	if !probes[0].Enabled {
		t.Fatal("a self-registered probe must be usable without an approval step")
	}
	if probes[0].CreatedVia != "public" {
		t.Errorf("CreatedVia = %q, want public", probes[0].CreatedVia)
	}
	probeID := probes[0].ProbeID

	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"aliyun_dns":{"icmp":{"success":true,"sent":5,"received":5,"loss_ratio":0,"min_rtt_ms":10.2,"avg_rtt_ms":12.3,"max_rtt_ms":15.8,"jitter_ms":1.4}}}}`
	push := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/push", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		handler.Push.ServeHTTP(w, r)
		return w
	}

	// 4. Its token works on the very first push, with no administrator involved.
	if got := push().Code; got != http.StatusNoContent {
		t.Fatalf("push as a freshly registered probe = %d, want 204", got)
	}

	// 5. And the measurement reaches Prometheus under the identity the database
	//    holds, not anything the request supplied.
	scrape := func() string {
		w := httptest.NewRecorder()
		metricsHandler(reg).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return w.Body.String()
	}
	s := scrape()
	for _, want := range []string{
		`probe_id="` + probeID + `"`,
		`campus="hx"`,
		`building="sy01"`,
		`target="aliyun_dns"`,
		"campus_probe_icmp_rtt_seconds",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the scrape after a public registration lacks %s", want)
		}
	}
	online := regexp.MustCompile(`campus_probe_online\{[^}]*probe_id="` + probeID + `"[^}]*\} 1`)
	if !online.MatchString(s) {
		t.Error("a probe that just pushed is not reported online")
	}
}
