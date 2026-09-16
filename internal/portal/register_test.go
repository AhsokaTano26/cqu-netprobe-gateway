package portal

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tano/cqu-netprobe-gateway/internal/config"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
)

type harness struct {
	server *Server
	store  *store.Store
	clock  time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "portal.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		MaxProbes:          500,
		PublicBaseURL:      "https://netprobe.example.com",
		RegisterLimit:      time.Hour,
		RegisterLimitBurst: 3,
	}
	h := &harness{store: st, clock: time.Unix(1789490000, 0).UTC()}
	srv, err := NewServer(Deps{
		Store: st, Config: cfg,
		Limiter: NewRegisterLimiter(cfg.RegisterLimit, cfg.RegisterLimitBurst),
		Now:     func() time.Time { return h.clock },
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	h.server = srv

	if err := st.CreateCampus(&store.Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	if err := st.CreateBuilding(&store.Building{Code: "sy01", CampusCode: "hx",
		BuildingGroupCode: "sy", BuildingGroupName: "松园", Name: "松园一栋"}); err != nil {
		t.Fatalf("CreateBuilding() error = %v", err)
	}
	return h
}

func (h *harness) do(t *testing.T, method, path string, form url.Values, remote string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if form == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if remote != "" {
		req.RemoteAddr = remote
	}
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)
	return rec
}

func TestRegisterFormNeedsNoAuth(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodGet, "/", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 without any session", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "虎溪") {
		t.Error("the campus dropdown is not populated from the catalog")
	}
}

func TestRegisterFormFiltersBuildingsByCampus(t *testing.T) {
	h := newHarness(t)
	if err := h.store.CreateCampus(&store.Campus{Code: "aq", Name: "A区"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	if err := h.store.CreateBuilding(&store.Building{Code: "aq01", CampusCode: "aq",
		BuildingGroupCode: "aq", BuildingGroupName: "A区", Name: "A区一栋"}); err != nil {
		t.Fatalf("CreateBuilding() error = %v", err)
	}

	hxOnly := h.do(t, http.MethodGet, "/?campus=hx", nil, "").Body.String()
	if !strings.Contains(hxOnly, "松园一栋") {
		t.Error("hx's building is missing from its own filtered list")
	}
	if strings.Contains(hxOnly, "A区一栋") {
		t.Error("another campus's building leaked into the filtered list")
	}
}

// TestRegisterCreatesEnabledProbeAndShowsTokenOnce covers the anonymous path
// end to end: the probe is usable the moment the token is shown, and the token
// itself is still shown exactly once.
func TestRegisterCreatesEnabledProbeAndShowsTokenOnce(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/", url.Values{
		"campus_code":   {"hx"},
		"building_code": {"sy01"},
		"network_type":  {"wired"},
		"description":   {"宿舍探针"},
	}, "10.1.2.3:5555")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/token/") {
		t.Fatalf("Location = %q, want /token/{slot}", loc)
	}

	first := h.do(t, http.MethodGet, loc, nil, "")
	if first.Code != http.StatusOK {
		t.Fatalf("token page status = %d, want 200", first.Code)
	}
	token := regexp.MustCompile(`cqu_probe_[A-Za-z0-9_-]{43}`).FindString(first.Body.String())
	if token == "" {
		t.Fatal("no token on the token page")
	}
	if !strings.Contains(first.Body.String(), "https://netprobe.example.com") {
		t.Error("the push endpoint is not shown")
	}
	// The push path must NOT be on the page: a probe appends /api/v1/push
	// itself (Protocol v1 §2), so showing it the full URL would have it post to
	// /api/v1/push/api/v1/push.
	if strings.Contains(first.Body.String(), "netprobe.example.com/api/v1/push") {
		t.Error("the page shows the full push URL; a probe that appends the path would double it")
	}
	if !strings.Contains(first.Body.String(), "不需要管理员审批") {
		t.Error("the page does not tell the visitor the token works straight away")
	}

	// The probe exists, is enabled, and is attributed to the public page.
	probes, err := h.store.ListProbes()
	if err != nil {
		t.Fatalf("ListProbes() error = %v", err)
	}
	if len(probes) != 1 {
		t.Fatalf("probes = %d, want 1", len(probes))
	}
	if !probes[0].Enabled {
		t.Error("a self-registered probe must be enabled without an approval step")
	}
	if probes[0].CreatedVia != "public" {
		t.Errorf("CreatedVia = %q, want public", probes[0].CreatedVia)
	}
	if probes[0].Description != "宿舍探针" {
		t.Errorf("Description = %q", probes[0].Description)
	}

	// Replaying the slot must not show the token again.
	second := h.do(t, http.MethodGet, loc, nil, "")
	if second.Code != http.StatusGone {
		t.Fatalf("replay status = %d, want 410", second.Code)
	}
	if regexp.MustCompile(`cqu_probe_[A-Za-z0-9_-]{43}`).MatchString(second.Body.String()) {
		t.Fatal("the token was displayed twice")
	}
}

// TestTokenPageOffersClickToCopyAndReturn covers the two things a visitor does
// with this page: retype three values into a config file, and leave.
func TestTokenPageOffersClickToCopyAndReturn(t *testing.T) {
	h := newHarness(t)
	rec := h.do(t, http.MethodPost, "/", url.Values{
		"campus_code": {"hx"}, "building_code": {"sy01"}, "network_type": {"wired"},
	}, "10.1.2.7:5555")
	page := h.do(t, http.MethodGet, rec.Header().Get("Location"), nil, "")
	if page.Code != http.StatusOK {
		t.Fatalf("token page status = %d, want 200", page.Code)
	}
	body := page.Body.String()

	// Each value is its own plain <code> with a small button beside it — never a
	// wrapper of its own — so the text stays freely selectable and a drag to
	// select cannot fire a copy. The button reads the text of the element it
	// names, which is what keeps the bytes copied equal to the bytes on screen.
	rows := map[string]string{
		"v-probe-id": `hx-sy01-[a-z0-9]{6}`,
		"v-token":    `cqu_probe_[A-Za-z0-9_-]{43}`,
		// Base URL only. The pattern has to match the element's whole content —
		// </code> follows it immediately — so a value carrying the push path
		// would not match here.
		"v-endpoint": `https://netprobe\.example\.com`,
	}
	for id, value := range rows {
		re := regexp.MustCompile(`(?s)<div class="copy-row">\s*<code id="` + id + `">` +
			value + `</code>\s*<button[^>]*data-copy="#` + id + `"[^>]*>复制</button>\s*</div>`)
		if !re.MatchString(body) {
			t.Errorf("row %s is not a plain value with a copy button of its own; around it:\n%s",
				id, excerpt(body, `id="`+id+`"`))
		}
	}
	if !strings.Contains(body, `<a class="btn secondary" href="/">`) {
		t.Error("the token page does not offer a return to the registration form")
	}
	// The values stay readable and selectable without scripting, so a browser
	// that blocks the handler degrades to plain text rather than to nothing.
	if !strings.Contains(body, `src="/static/app.js"`) {
		t.Error("the layout does not load the copy handler")
	}
}

// excerpt returns the markup around marker, so a layout failure reports the row
// at fault rather than the whole page.
func excerpt(body, marker string) string {
	const width = 240
	i := strings.Index(body, marker)
	if i < 0 {
		return "(the page has no " + marker + ")"
	}
	start := max(i-width/2, 0)
	end := min(start+width, len(body))
	// A byte window can cut a multi-byte character in half; drop the fragments.
	return strings.ToValidUTF8(body[start:end], "")
}

// TestTokenPageReturnsWhereTheMinterSaid keeps the admin flows out of the public
// registration form — an operator who just created a probe belongs in /admin —
// and pins the guard that stops a return path from pointing off-site.
func TestTokenPageReturnsWhereTheMinterSaid(t *testing.T) {
	h := newHarness(t)
	cases := map[string]string{
		"/admin":                       "/admin",
		"/admin/probes/hx-sy01-aaaaaa": "/admin/probes/hx-sy01-aaaaaa",
		"//evil.example":               "/",
		`/\evil.example`:               "/",
		"https://evil.example":         "/",
	}
	for back, want := range cases {
		t.Run(back, func(t *testing.T) {
			slot, err := h.server.MintTokenSlot("hx-sy01-aaaaaa", "cqu_probe_secret", back)
			if err != nil {
				t.Fatalf("MintTokenSlot() error = %v", err)
			}
			body := h.do(t, http.MethodGet, "/token/"+slot, nil, "").Body.String()
			// The whole anchor, not just the href: the topbar nav also links to
			// /admin, so a bare href check could pass with the button missing.
			if !strings.Contains(body, `<a class="btn secondary" href="`+want+`">`) {
				t.Errorf("return link is not %q", want)
			}
			if strings.Contains(body, "evil.example") {
				t.Error("an off-site return path reached the page")
			}
		})
	}
}

func TestTokenPageExpiredStillOffersAWayBack(t *testing.T) {
	h := newHarness(t)
	body := h.do(t, http.MethodGet, "/token/not-a-real-slot", nil, "").Body.String()
	if !strings.Contains(body, `<a class="btn secondary" href="/">`) {
		t.Error("the expired page is a dead end")
	}
}

func TestRegisterRejectsUnknownOrMismatchedLocation(t *testing.T) {
	h := newHarness(t)
	if err := h.store.CreateCampus(&store.Campus{Code: "aq", Name: "A区"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}

	cases := map[string]url.Values{
		"unknown campus":   {"campus_code": {"nope"}, "building_code": {"sy01"}, "network_type": {"wired"}},
		"unknown building": {"campus_code": {"hx"}, "building_code": {"nope"}, "network_type": {"wired"}},
		"mismatched pair":  {"campus_code": {"aq"}, "building_code": {"sy01"}, "network_type": {"wired"}},
		"bad network type": {"campus_code": {"hx"}, "building_code": {"sy01"}, "network_type": {"satellite"}},
	}
	for name, form := range cases {
		t.Run(name, func(t *testing.T) {
			rec := h.do(t, http.MethodPost, "/", form, "10.1.2.4:5555")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			probes, _ := h.store.ListProbes()
			if len(probes) != 0 {
				t.Fatal("a rejected registration still created a probe")
			}
		})
	}
}

func TestRegisterRateLimited(t *testing.T) {
	h := newHarness(t)
	form := url.Values{
		"campus_code": {"hx"}, "building_code": {"sy01"}, "network_type": {"wired"},
	}
	for i := 1; i <= 3; i++ {
		if rec := h.do(t, http.MethodPost, "/", form, "10.1.2.5:5555"); rec.Code != http.StatusSeeOther {
			t.Fatalf("registration %d status = %d, want 303", i, rec.Code)
		}
	}
	rec := h.do(t, http.MethodPost, "/", form, "10.1.2.5:5555")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 after the burst", rec.Code)
	}

	// A different address is unaffected.
	if rec := h.do(t, http.MethodPost, "/", form, "10.1.2.6:5555"); rec.Code != http.StatusSeeOther {
		t.Fatalf("a different IP was throttled by another address's registrations: %d", rec.Code)
	}
}

// The cap is what bounds the anonymous path now that registration is
// immediate. It must refuse at the boundary, not one probe later, and it must
// say so in a way the visitor can act on.
func TestRegisterRefusedWhenProbeCapReached(t *testing.T) {
	h := newHarness(t)
	h.server.cfg.MaxProbes = 1

	form := url.Values{
		"campus_code": {"hx"}, "building_code": {"sy01"}, "network_type": {"wired"},
	}
	if rec := h.do(t, http.MethodPost, "/", form, "10.1.9.1:5555"); rec.Code != http.StatusSeeOther {
		t.Fatalf("first registration status = %d, want 303", rec.Code)
	}

	rec := h.do(t, http.MethodPost, "/", form, "10.1.9.2:5555")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("registration past the cap = %d, want 503", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "上限") {
		t.Error("the refusal does not tell the visitor a limit was reached")
	}
	probes, err := h.store.ListProbes()
	if err != nil {
		t.Fatalf("ListProbes() error = %v", err)
	}
	if len(probes) != 1 {
		t.Fatalf("probes = %d, want 1: the cap did not hold", len(probes))
	}
}

// A cap of zero would reject every registration with a "limit reached" page,
// which is a far worse way to discover a missing setting than a refusal to
// start. Same reasoning as the metrics allowlist: fail closed, and fail early.
func TestPortalRefusesToStartWithoutAProbeCap(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "nocap.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer func() { _ = st.Close() }()

	if _, err := NewServer(Deps{Store: st, Config: &config.Config{}}); err == nil {
		t.Fatal("NewServer() accepted a config with no probe cap")
	}
	if _, err := NewServer(Deps{Store: st}); err == nil {
		t.Fatal("NewServer() accepted a nil config")
	}
}

// The anonymous path has the same failure mode as the push throttle: behind a
// proxy every visitor shares one address, so the allowance becomes global and
// self-registration stops working for everyone after the first few people.
// Declaring the proxy trusted that the header may be believed is the fix.
func TestRegisterLimiterSeparatesClientsBehindATrustedProxy(t *testing.T) {
	register := func(t *testing.T, h *harness, clientIP string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(url.Values{
			"campus_code": {"hx"}, "building_code": {"sy01"}, "network_type": {"wired"},
		}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "127.0.0.1:5000" // the proxy
		req.Header.Set("X-Forwarded-For", clientIP)
		rec := httptest.NewRecorder()
		h.server.Routes().ServeHTTP(rec, req)
		return rec.Code
	}

	t.Run("without a trusted proxy the allowance is global", func(t *testing.T) {
		h := newHarness(t)
		for i := 0; i < 3; i++ {
			if got := register(t, h, fmt.Sprintf("203.0.113.%d", i)); got != http.StatusSeeOther {
				t.Fatalf("registration %d = %d, want 303", i, got)
			}
		}
		if got := register(t, h, "198.51.100.4"); got != http.StatusTooManyRequests {
			t.Errorf("a fourth visitor = %d, want 429 (one allowance for the proxy)", got)
		}
	})

	t.Run("with a trusted proxy each visitor has their own", func(t *testing.T) {
		h := newHarness(t)
		_, proxy, err := net.ParseCIDR("127.0.0.0/8")
		if err != nil {
			t.Fatal(err)
		}
		h.server.cfg.TrustedProxyCIDRs = []*net.IPNet{proxy}

		for i := 0; i < 3; i++ {
			if got := register(t, h, "203.0.113.9"); got != http.StatusSeeOther {
				t.Fatalf("registration %d = %d, want 303", i, got)
			}
		}
		if got := register(t, h, "203.0.113.9"); got != http.StatusTooManyRequests {
			t.Errorf("the fourth registration by one visitor = %d, want 429", got)
		}
		if got := register(t, h, "198.51.100.4"); got != http.StatusSeeOther {
			t.Errorf("another visitor = %d, want 303: one person's registrations must not lock out the rest", got)
		}
	})
}

func TestRegisterRejectsForeignOrigin(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(url.Values{
		"campus_code": {"hx"}, "building_code": {"sy01"}, "network_type": {"wired"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a foreign Origin", rec.Code)
	}
}

func TestRegisterServes503WhenCatalogIsEmpty(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer func() { _ = st.Close() }()
	// The welcome migration seeds targets, not campuses; clear any backfill.
	if _, err := st.ListCampuses(); err != nil {
		t.Fatalf("ListCampuses() error = %v", err)
	}
	cfg := &config.Config{RegisterLimit: time.Hour, RegisterLimitBurst: 3, MaxProbes: 500}
	srv, err := NewServer(Deps{Store: st, Config: cfg,
		Limiter: NewRegisterLimiter(cfg.RegisterLimit, cfg.RegisterLimitBurst)})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		url.Values{"campus_code": {"hx"}, "building_code": {"sy01"}, "network_type": {"wired"}}.Encode())))
	rec.Result() // drain
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when no campus is configured", rec.Code)
	}
}
