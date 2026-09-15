package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tano/cqu-netprobe-gateway/internal/config"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
)

type adminHarness struct {
	server   *Server
	store    *store.Store
	now      time.Time
	password string
}

func newAdminHarness(t *testing.T, adminPassword string) *adminHarness {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Unix(1789490000, 0).UTC()
	cfg := &config.Config{
		AdminUsername: "admin",
		AdminPassword: adminPassword,
		DataDir:       t.TempDir(),
	}

	srv, err := NewServer(Deps{
		Store:  st,
		Config: cfg,
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	pw := adminPassword
	if pw == "" {
		pw, _ = srv.PasswordGeneration()
	}
	return &adminHarness{server: srv, store: st, now: now, password: pw}
}

func (h *adminHarness) get(t *testing.T, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)
	return rec
}

func (h *adminHarness) post(t *testing.T, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.server.Routes().ServeHTTP(rec, req)
	return rec
}

// login performs the login flow and returns the session cookie.
func (h *adminHarness) login(t *testing.T) *http.Cookie {
	t.Helper()
	rec := h.post(t, "/admin/login", url.Values{
		"username": {"admin"},
		"password": {h.password},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("login did not set a session cookie")
	return nil
}

func TestAdminRedirectsWhenUnauthenticated(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	// Task 17 and Task 18 append their own paths to this table.
	for _, path := range []string{"/admin/logout"} {
		t.Run(path, func(t *testing.T) {
			rec := h.post(t, path, url.Values{}, nil)
			if rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want a redirect to login", rec.Code)
			}
			if loc := rec.Header().Get("Location"); loc != "/admin/login" {
				t.Errorf("Location = %q, want /admin/login", loc)
			}
		})
	}
}

func TestAdminLoginRejectsWrongPassword(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	rec := h.post(t, "/admin/login", url.Values{
		"username": {"admin"},
		"password": {"wrong"},
	}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "wrong") {
		t.Error("response echoes the submitted password")
	}
}

func TestAdminLoginRejectsWrongUsername(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	rec := h.post(t, "/admin/login", url.Values{
		"username": {"root"},
		"password": {h.password},
	}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAdminLoginSucceeds(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)

	if !cookie.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}

	// With a live session, the login page bounces to the dashboard. That is the
	// only session-gated GET available until Task 17 adds the probe list, and it
	// proves the cookie is accepted.
	rec := h.get(t, "/admin/login", cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("authenticated GET /admin/login status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin" {
		t.Errorf("Location = %q, want /admin", loc)
	}
}

func TestAdminLogoutInvalidatesSession(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)

	rec := h.post(t, "/admin/logout", url.Values{}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout status = %d, want 303", rec.Code)
	}

	// The session must be gone: the login page no longer bounces.
	after := h.get(t, "/admin/login", cookie)
	if after.Code != http.StatusOK {
		t.Fatalf("GET /admin/login after logout = %d, want 200 (session should be dead)", after.Code)
	}
}

func TestAdminGeneratesPasswordWhenUnset(t *testing.T) {
	h := newAdminHarness(t, "")
	if len(h.password) < 32 {
		t.Fatalf("generated password is %d chars, want at least 32", len(h.password))
	}
	// The generated password must actually work.
	h.login(t)
}

func TestAdminGeneratedPasswordPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")
	now := time.Unix(1789490000, 0).UTC()
	cfg := &config.Config{AdminUsername: "admin", DataDir: dir}

	st1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	srv1, err := NewServer(Deps{Store: st1, Config: cfg, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	first, generated := srv1.PasswordGeneration()
	if !generated {
		t.Fatal("PasswordGeneration() generated = false on first start")
	}
	_ = st1.Close()

	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer func() { _ = st2.Close() }()
	srv2, err := NewServer(Deps{Store: st2, Config: cfg, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("second NewServer() error = %v", err)
	}
	second, generatedAgain := srv2.PasswordGeneration()
	if generatedAgain {
		t.Error("a new password was generated on restart; it must be generated once")
	}
	if second != "" {
		t.Errorf("PasswordGeneration() = %q on a restart, want no plaintext at all", second)
	}
	// Only the hash is stored, so the second process cannot recover the
	// plaintext: "the password did not change" is observable on the hash alone,
	// and the login below proves the operator's copy still works.
	if srv2.passwordHash != srv1.passwordHash {
		t.Error("stored password hash changed across restart")
	}

	// And it must still authenticate.
	req := httptest.NewRequest(http.MethodPost, "/admin/login",
		strings.NewReader(url.Values{"username": {"admin"}, "password": {first}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv2.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login after restart status = %d, want 303", rec.Code)
	}
}

func TestAdminCSRFRequired(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)

	// The probe and target routes that wrap requireCSRF are registered by Tasks
	// 17 and 18. Until they exist this exercises the middleware directly, with
	// the same path and method those tasks will use.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux := http.NewServeMux()
	mux.Handle("POST /admin/probes/new", h.server.requireCSRF(next))

	post := func(cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/probes/new", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// A state-changing POST without a CSRF token must be rejected.
	rec := post(cookie, url.Values{
		"campus_code":   {"hx"},
		"building_code": {"sy01"},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a missing CSRF token", rec.Code)
	}

	// Without a session it must not reach the handler either.
	if anon := post(nil, url.Values{}); anon.Code != http.StatusSeeOther {
		t.Fatalf("status = %d without a session, want a redirect to login", anon.Code)
	}

	// A wrong token is rejected just like a missing one.
	if bad := post(cookie, url.Values{"csrf": {"not-the-token"}}); bad.Code != http.StatusForbidden {
		t.Fatalf("status = %d for a wrong CSRF token, want 403", bad.Code)
	}

	// The session's own token passes, so the check is not unconditional.
	sess, ok := h.server.sessions.get(cookie.Value)
	if !ok {
		t.Fatal("session for the login cookie is gone")
	}
	if good := post(cookie, url.Values{"csrf": {sess.csrf}}); good.Code != http.StatusOK {
		t.Fatalf("status = %d with the session CSRF token, want 200", good.Code)
	}
}

func TestAdminStaticCSSServed(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	rec := h.get(t, "/admin/static/admin.css", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("css status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "body") {
		t.Error("css body does not look like a stylesheet")
	}
}

func TestAdminLoginPageRenders(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	rec := h.get(t, "/admin/login", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<form") {
		t.Error("login page has no form")
	}
}
