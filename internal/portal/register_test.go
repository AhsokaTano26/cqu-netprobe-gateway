package portal

import (
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

func TestRegisterCreatesDisabledProbeAndShowsTokenOnce(t *testing.T) {
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
	if !strings.Contains(first.Body.String(), "https://netprobe.example.com/api/v1/push") {
		t.Error("the push endpoint is not shown")
	}
	if !strings.Contains(first.Body.String(), "待") {
		t.Error("the page does not tell the visitor the probe awaits approval")
	}

	// The probe exists, is disabled, and is attributed to the public page.
	probes, err := h.store.ListProbes()
	if err != nil {
		t.Fatalf("ListProbes() error = %v", err)
	}
	if len(probes) != 1 {
		t.Fatalf("probes = %d, want 1", len(probes))
	}
	if probes[0].Enabled {
		t.Error("a self-registered probe must start disabled")
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
	cfg := &config.Config{RegisterLimit: time.Hour, RegisterLimitBurst: 3}
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
