package admin

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
	"github.com/tano/cqu-netprobe-gateway/internal/token"
)

// csrfFrom extracts the CSRF token from a rendered page.
func csrfFrom(t *testing.T, html string) string {
	t.Helper()
	re := regexp.MustCompile(`name="csrf" value="([^"]+)"`)
	m := re.FindStringSubmatch(html)
	if m == nil {
		t.Fatalf("no csrf token found in page")
	}
	return m[1]
}

// validProbeForm submits only catalog codes: the form no longer carries any
// campus or building name, so every display name a probe stores is resolved
// from the catalog inside the handler.
func validProbeForm(csrf string) url.Values {
	return url.Values{
		"csrf":          {csrf},
		"campus_code":   {"hx"},
		"building_code": {"sy01"},
		"network_type":  {"wired"},
		"description":   {"测试探针"},
	}
}

// seedProbeCatalog creates the hx/sy01 catalog rows a form-driven create
// resolves against. The shared harness seeds no catalog on purpose, so every
// test that creates a probe through the form brings its own location.
func seedProbeCatalog(t *testing.T, h *adminHarness) {
	t.Helper()
	if err := h.store.CreateCampus(&store.Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus(hx) error = %v", err)
	}
	if err := h.store.CreateBuilding(&store.Building{
		Code: "sy01", CampusCode: "hx",
		BuildingGroupCode: "sy", BuildingGroupName: "松园", Name: "松园一栋",
	}); err != nil {
		t.Fatalf("CreateBuilding(sy01) error = %v", err)
	}
}

var (
	probeIDPattern    = regexp.MustCompile(`hx-sy01-[0-9a-f]{6}`)
	probeTokenPattern = regexp.MustCompile(`cqu_probe_[A-Za-z0-9_-]{43}`)
)

// createProbe drives the create flow and returns the probe ID and its token
// page URL.
func (h *adminHarness) createProbe(t *testing.T, cookie *http.Cookie) (id, tokenURL string) {
	t.Helper()
	page := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, page.Body.String())
	rec := h.post(t, "/admin/probes/new", validProbeForm(csrf), cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	tokPage := h.get(t, loc, cookie)
	id = probeIDPattern.FindString(tokPage.Body.String())
	if id == "" {
		t.Fatalf("no probe ID on the token page: %s", tokPage.Body.String())
	}
	return id, loc
}

func TestProbeCreateShowsTokenOnce(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeCatalog(t, h)

	listPage := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, listPage.Body.String())

	rec := h.post(t, "/admin/probes/new", validProbeForm(csrf), cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/token/") {
		t.Fatalf("Location = %q, want /token/{slot}", loc)
	}

	// First visit shows the token, on the public page: admin has no token page
	// of its own any more, and the slot carries the probe ID server-side.
	first := h.get(t, loc, cookie)
	if first.Code != http.StatusOK {
		t.Fatalf("token page status = %d, want 200", first.Code)
	}
	body := first.Body.String()
	if !regexp.MustCompile(`cqu_probe_[A-Za-z0-9_-]{43}`).MatchString(body) {
		t.Fatal("token page does not contain a token")
	}
	if !regexp.MustCompile(`hx-sy01-[0-9a-f]{6}`).MatchString(body) {
		t.Fatal("token page does not contain a probe ID")
	}

	// Second visit must not: the slot was consumed, and a spent slot is gone
	// rather than rendering a page without a token.
	second := h.get(t, loc, cookie)
	if second.Code != http.StatusGone {
		t.Fatalf("replay status = %d, want 410", second.Code)
	}
}

func TestProbeCreateStoresHashNotPlaintext(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeCatalog(t, h)

	page := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, page.Body.String())
	rec := h.post(t, "/admin/probes/new", validProbeForm(csrf), cookie)
	loc := rec.Header().Get("Location")

	tokPage := h.get(t, loc, cookie)
	m := regexp.MustCompile(`cqu_probe_[A-Za-z0-9_-]{43}`).FindString(tokPage.Body.String())
	if m == "" {
		t.Fatal("no token on the token page")
	}
	idMatch := regexp.MustCompile(`hx-sy01-[0-9a-f]{6}`).FindString(tokPage.Body.String())

	got, err := h.store.GetProbe(idMatch)
	if err != nil {
		t.Fatalf("GetProbe() error = %v", err)
	}
	if got.TokenHash != token.Hash(m) {
		t.Error("stored hash does not match the displayed token")
	}
	if got.TokenHash == m {
		t.Fatal("the database stored the plaintext token")
	}
	if got.CampusName != "虎溪" || got.BuildingGroupName != "松园" {
		t.Errorf("display names not stored: %+v", got)
	}
}

func TestProbeCreateRejectsBadCode(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	page := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, page.Body.String())

	cases := map[string]string{
		"uppercase": "HX",
		"spaces":    "h x",
		"chinese":   "虎溪",
		"dash":      "h-x",
		"empty":     "",
		"too long":  "aaaaaaaaaaaaaaaaaaaaa",
	}
	for name, code := range cases {
		t.Run(name, func(t *testing.T) {
			form := validProbeForm(csrf)
			form.Set("campus_code", code)
			rec := h.post(t, "/admin/probes/new", form, cookie)
			if rec.Code == http.StatusSeeOther {
				t.Fatalf("bad campus_code %q was accepted", code)
			}
		})
	}
}

func TestProbeCreateRejectsBadNetworkType(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	page := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, page.Body.String())

	seedProbeCatalog(t, h)

	form := validProbeForm(csrf)
	form.Set("network_type", "satellite")
	rec := h.post(t, "/admin/probes/new", form, cookie)
	// The location now resolves, so the rejection is attributable to the
	// network type and nothing else.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

func TestProbeListRendersCreatedProbe(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeCatalog(t, h)
	page := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, page.Body.String())
	rec := h.post(t, "/admin/probes/new", validProbeForm(csrf), cookie)
	tokPage := h.get(t, rec.Header().Get("Location"), cookie)
	idMatch := regexp.MustCompile(`hx-sy01-[0-9a-f]{6}`).FindString(tokPage.Body.String())

	list := h.get(t, "/admin", cookie)
	body := list.Body.String()
	if !regexp.MustCompile(idMatch).MatchString(body) {
		t.Errorf("probe %s not shown in the list", idMatch)
	}
	for _, want := range []string{"虎溪", "松园", "松园一栋", "wired"} {
		if !regexp.MustCompile(want).MatchString(body) {
			t.Errorf("list does not show %q", want)
		}
	}
}

func TestProbeToggle(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeCatalog(t, h)
	page := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, page.Body.String())
	rec := h.post(t, "/admin/probes/new", validProbeForm(csrf), cookie)
	tokPage := h.get(t, rec.Header().Get("Location"), cookie)
	id := regexp.MustCompile(`hx-sy01-[0-9a-f]{6}`).FindString(tokPage.Body.String())

	form := url.Values{"csrf": {csrf}}
	if r := h.post(t, "/admin/probes/"+id+"/toggle", form, cookie); r.Code != http.StatusSeeOther {
		t.Fatalf("toggle status = %d, want 303", r.Code)
	}

	got, err := h.store.GetProbe(id)
	if err != nil {
		t.Fatalf("GetProbe() error = %v", err)
	}
	if got.Enabled {
		t.Error("probe is still enabled after toggle")
	}

	if r := h.post(t, "/admin/probes/"+id+"/toggle", form, cookie); r.Code != http.StatusSeeOther {
		t.Fatalf("second toggle status = %d", r.Code)
	}
	got, _ = h.store.GetProbe(id)
	if !got.Enabled {
		t.Error("probe is still disabled after a second toggle")
	}
}

func TestProbeRotateKeepsIDAndRevokesOldToken(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeCatalog(t, h)
	page := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, page.Body.String())
	rec := h.post(t, "/admin/probes/new", validProbeForm(csrf), cookie)

	tokPage := h.get(t, rec.Header().Get("Location"), cookie)
	firstToken := regexp.MustCompile(`cqu_probe_[A-Za-z0-9_-]{43}`).FindString(tokPage.Body.String())
	id := regexp.MustCompile(`hx-sy01-[0-9a-f]{6}`).FindString(tokPage.Body.String())

	rot := h.post(t, "/admin/probes/"+id+"/rotate", url.Values{"csrf": {csrf}}, cookie)
	if rot.Code != http.StatusSeeOther {
		t.Fatalf("rotate status = %d, want 303", rot.Code)
	}
	rotLoc := rot.Header().Get("Location")
	if !strings.HasPrefix(rotLoc, "/token/") {
		t.Fatalf("Location = %q, want /token/{slot}", rotLoc)
	}

	rotPage := h.get(t, rotLoc, cookie)
	newToken := regexp.MustCompile(`cqu_probe_[A-Za-z0-9_-]{43}`).FindString(rotPage.Body.String())
	if newToken == firstToken {
		t.Fatal("rotation produced the same token")
	}
	if !regexp.MustCompile(id).MatchString(rotPage.Body.String()) {
		t.Fatal("rotation changed the probe ID")
	}

	// The old token must no longer resolve.
	if _, err := h.store.ProbeByTokenHash(token.Hash(firstToken)); err == nil {
		t.Fatal("the old token still authenticates after rotation")
	}
	if _, err := h.store.ProbeByTokenHash(token.Hash(newToken)); err != nil {
		t.Fatalf("the new token does not authenticate: %v", err)
	}
}

func TestProbeDelete(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeCatalog(t, h)
	page := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, page.Body.String())
	rec := h.post(t, "/admin/probes/new", validProbeForm(csrf), cookie)
	tokPage := h.get(t, rec.Header().Get("Location"), cookie)
	id := regexp.MustCompile(`hx-sy01-[0-9a-f]{6}`).FindString(tokPage.Body.String())

	del := h.post(t, "/admin/probes/"+id+"/delete", url.Values{"csrf": {csrf}}, cookie)
	if del.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303", del.Code)
	}
	if _, err := h.store.GetProbe(id); err == nil {
		t.Fatal("probe still exists after delete")
	}
}

func TestProbeDetailNotFound(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	rec := h.get(t, "/admin/probes/hx-sy01-ffffff", cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestAdminServesNoTokenPage pins the consolidation: the one-shot token page is
// the portal's public /token/{slot}. Admin keeps a route for neither the page nor
// its display, so /admin/token/... is now a plain 404 — a second implementation
// cannot come back by accident.
func TestAdminServesNoTokenPage(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	if rec := h.get(t, "/admin/token/any-slot", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("GET /admin/token/any-slot = %d, want 404: admin must not serve a token page", rec.Code)
	}
	// The public page answers without any session. An unknown slot is gone
	// (410), not a redirect to the login form.
	if rec := h.get(t, "/token/any-slot", nil); rec.Code != http.StatusGone {
		t.Fatalf("GET /token/any-slot = %d, want 410 without a session", rec.Code)
	}
}

func TestValidCode(t *testing.T) {
	valid := []string{"hx", "sy01", "songyuan_1", "a", "abc123", "abcdefghijklmnop"}
	for _, c := range valid {
		if !validCode(c) {
			t.Errorf("validCode(%q) = false, want true", c)
		}
	}
	invalid := []string{"", "HX", "h-x", "h x", "虎溪", "abc123def456ghi789", "h.x"}
	for _, c := range invalid {
		if validCode(c) {
			t.Errorf("validCode(%q) = true, want false", c)
		}
	}
}

// TestProbeTokenPageIgnoresForgedProbeIDQuery pins the probe ID to the
// server-side one-shot slot: a crafted ?probe_id= must not relabel a real token.
func TestProbeTokenPageIgnoresForgedProbeIDQuery(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeCatalog(t, h)
	page := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, page.Body.String())
	rec := h.post(t, "/admin/probes/new", validProbeForm(csrf), cookie)
	loc := rec.Header().Get("Location")

	// This is the slot's only redemption, so the forged query parameter is
	// present at the one render that carries a real token.
	got := h.get(t, loc+"?probe_id=hx-sy01-ffffff", cookie)
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", got.Code)
	}
	body := got.Body.String()

	probes, err := h.store.ListProbes()
	if err != nil || len(probes) != 1 {
		t.Fatalf("ListProbes() = %d probes, %v; want exactly 1", len(probes), err)
	}
	if !strings.Contains(body, probes[0].ProbeID) {
		t.Errorf("token page does not show the real probe ID %s", probes[0].ProbeID)
	}
	if strings.Contains(body, "hx-sy01-ffffff") {
		t.Error("the forged ?probe_id was rendered beside a real token")
	}
	if !probeTokenPattern.MatchString(body) {
		t.Error("the token itself is missing from the page")
	}
}

// TestProbeValidationErrorRendersForm: a rejected create must re-render the form
// with the submitted values, not an opaque error page.
func TestProbeValidationErrorRendersForm(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeCatalog(t, h)
	page := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, page.Body.String())

	form := validProbeForm(csrf)
	form.Set("campus_code", "HX") // outside the code charset
	form.Set("description", "保留这段备注")
	rec := h.post(t, "/admin/probes/new", form, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The location is a catalog-backed select now, so the free-text field that
	// can carry a submitted value back is the description.
	if !strings.Contains(body, "保留这段备注") {
		t.Error("the rejected form values were not preserved")
	}
	if !strings.Contains(body, `name="csrf"`) {
		t.Error("the re-rendered form has no csrf field")
	}
}

// TestProbeDisableAndDeleteClearInMemoryState covers the in-memory cleanup: a
// disabled probe must not keep a rate-limit bucket or a stale measurement that
// would make it look online the instant it is re-enabled.
func TestProbeDisableAndDeleteClearInMemoryState(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeCatalog(t, h)
	page := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, page.Body.String())
	rec := h.post(t, "/admin/probes/new", validProbeForm(csrf), cookie)
	id := probeIDPattern.FindString(h.get(t, rec.Header().Get("Location"), cookie).Body.String())
	form := url.Values{"csrf": {csrf}}

	// Disabling clears both the bucket and the measurement.
	h.latest.Put(id, protocol.Results{}, h.now)
	if r := h.post(t, "/admin/probes/"+id+"/toggle", form, cookie); r.Code != http.StatusSeeOther {
		t.Fatalf("disable status = %d, want 303", r.Code)
	}
	if _, ok := h.latest.Get(id); ok {
		t.Error("disabling a probe left its measurement in place")
	}
	if got := h.limiter.count(id); got != 1 {
		t.Errorf("limiter.Remove called %d times on disable, want 1", got)
	}

	// Re-enabling must not clear anything: the probe is live again and any
	// measurement that arrives from here on is its own.
	h.latest.Put(id, protocol.Results{}, h.now)
	if r := h.post(t, "/admin/probes/"+id+"/toggle", form, cookie); r.Code != http.StatusSeeOther {
		t.Fatalf("re-enable status = %d, want 303", r.Code)
	}
	if _, ok := h.latest.Get(id); !ok {
		t.Error("re-enabling a probe dropped its measurement")
	}

	// Deleting clears both, unconditionally.
	if r := h.post(t, "/admin/probes/"+id+"/delete", form, cookie); r.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303", r.Code)
	}
	if _, ok := h.latest.Get(id); ok {
		t.Error("deleting a probe left its measurement in place")
	}
	if got := h.limiter.count(id); got != 2 {
		t.Errorf("limiter.Remove called %d times in total, want 2", got)
	}
}

// TestProbeListOnlineState mirrors the metrics collector's rule: a measurement
// inside the online threshold is Online, one outside it is Offline.
func TestProbeListOnlineState(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeCatalog(t, h)
	id, _ := h.createProbe(t, cookie)

	h.latest.Put(id, protocol.Results{}, h.now.Add(-29*time.Second))
	body := h.get(t, "/admin", cookie).Body.String()
	if !strings.Contains(body, "Online") {
		t.Error("a fresh measurement did not render as Online")
	}
	// The list's per-row toggle form must carry the CSRF token, not just the
	// create form the other tests scrape theirs from.
	if !strings.Contains(body, `name="csrf" value="`) {
		t.Error("the list page's toggle forms have no csrf field")
	}

	h.latest.Put(id, protocol.Results{}, h.now.Add(-31*time.Second))
	if body := h.get(t, "/admin", cookie).Body.String(); strings.Contains(body, "Online") {
		t.Error("a stale measurement rendered as Online")
	}
}

// TestProbeCreateRetriesOnIDCollision: a colliding ID must be retried, not
// reported to the operator as an error they cannot act on.
func TestProbeCreateRetriesOnIDCollision(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeCatalog(t, h)

	taken := "hx-sy01-abcdef"
	if err := h.store.CreateProbe(&store.Probe{
		ProbeID:   taken,
		TokenHash: token.Hash("seed-token"),
		Enabled:   true,
	}); err != nil {
		t.Fatalf("seeding the colliding probe failed: %v", err)
	}

	// Hand out the taken ID once, then fall back to the real generator.
	original := probeIDSource
	defer func() { probeIDSource = original }()
	calls := 0
	probeIDSource = func(campusCode, buildingCode string) (string, error) {
		calls++
		if calls == 1 {
			return taken, nil
		}
		return original(campusCode, buildingCode)
	}

	page := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, page.Body.String())
	rec := h.post(t, "/admin/probes/new", validProbeForm(csrf), cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303 after retrying a colliding ID; body = %s",
			rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Errorf("generateProbeID called %d times, want 2 (one collision, one success)", calls)
	}
	probes, err := h.store.ListProbes()
	if err != nil {
		t.Fatalf("ListProbes() error = %v", err)
	}
	if len(probes) != 2 {
		t.Errorf("store holds %d probes, want 2", len(probes))
	}
}

// TestProbeDetailRendersProbe: the detail template is the one page a viewer
// reaches by hand, so a typo in it must not ship silently as a 500.
func TestProbeDetailRendersProbe(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeCatalog(t, h)
	id, _ := h.createProbe(t, cookie)

	rec := h.get(t, "/admin/probes/"+id, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{id, "虎溪", "松园", "松园一栋", "wired", "测试探针", "轮换 Token"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page does not show %q", want)
		}
	}
	if !strings.Contains(body, `name="csrf" value="`) {
		t.Error("the detail page's action forms have no csrf field")
	}
}

// seedPendingProbe inserts a probe as the public page would: disabled and
// attributable to self-registration.
func seedPendingProbe(t *testing.T, h *adminHarness, probeID, tokenHash string) {
	t.Helper()
	if err := h.store.CreateProbe(&store.Probe{
		ProbeID: probeID, TokenHash: tokenHash,
		CampusCode: "hx", CampusName: "虎溪",
		BuildingGroupCode: "sy", BuildingGroupName: "松园",
		BuildingCode: "sy01", BuildingName: "松园一栋",
		NetworkType: "wired", Enabled: false, CreatedVia: "public",
	}); err != nil {
		t.Fatalf("CreateProbe(%s) error = %v", probeID, err)
	}
}

func TestProbeListShowsCredentialInventory(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)

	seedPendingProbe(t, h, "hx-sy01-cccccc", "hash-c")

	body := h.get(t, "/admin", cookie).Body.String()
	if !strings.Contains(body, "从未轮换") {
		t.Error("a never-rotated credential is not labelled as such")
	}
	if !strings.Contains(body, "待启用") {
		t.Error("a public registration awaiting approval is not badged")
	}
	if !strings.Contains(body, "hx-sy01-cccccc") {
		t.Error("the probe is not listed at all")
	}
}

func TestProbeListMarksRotatedCredentials(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeForAdmin(t, h, "hx-sy01-dddddd", "hash-d")

	before := h.get(t, "/admin", cookie).Body.String()
	if !strings.Contains(before, "从未轮换") {
		t.Fatal("a freshly created credential should read as never rotated")
	}

	if err := h.store.UpdateProbeToken("hx-sy01-dddddd", "hash-e"); err != nil {
		t.Fatalf("UpdateProbeToken() error = %v", err)
	}
	after := h.get(t, "/admin", cookie).Body.String()
	if strings.Contains(after, "从未轮换") {
		t.Error("a rotated credential still reads as never rotated")
	}
}

func TestProbeListStatusFilter(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeForAdmin(t, h, "hx-sy01-aaaaaa", "hash-a")
	seedPendingProbe(t, h, "hx-sy01-eeeeee", "hash-f")

	pending := h.get(t, "/admin?filter=pending", cookie).Body.String()
	if !strings.Contains(pending, "hx-sy01-eeeeee") {
		t.Error("the pending filter dropped the probe awaiting approval")
	}
	if strings.Contains(pending, "hx-sy01-aaaaaa") {
		t.Error("the pending filter included an enabled probe")
	}

	disabled := h.get(t, "/admin?filter=disabled", cookie).Body.String()
	if strings.Contains(disabled, "hx-sy01-eeeeee") {
		t.Error("a probe awaiting approval was counted as deliberately disabled")
	}

	all := h.get(t, "/admin?filter=all", cookie).Body.String()
	if !strings.Contains(all, "hx-sy01-eeeeee") || !strings.Contains(all, "hx-sy01-aaaaaa") {
		t.Error("the all filter dropped a probe")
	}

	// An unknown filter value must fall back to "all" rather than showing nothing.
	bogus := h.get(t, "/admin?filter=nonsense", cookie).Body.String()
	if !strings.Contains(bogus, "hx-sy01-aaaaaa") {
		t.Error("an unrecognised filter value should behave as all")
	}
}

func TestProbeFormOffersCatalogOptions(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)

	if err := h.store.CreateCampus(&store.Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	if err := h.store.CreateBuilding(&store.Building{Code: "sy01", CampusCode: "hx",
		BuildingGroupCode: "sy", BuildingGroupName: "松园", Name: "松园一栋"}); err != nil {
		t.Fatalf("CreateBuilding() error = %v", err)
	}

	body := h.get(t, "/admin/probes/new", cookie).Body.String()
	if !strings.Contains(body, `name="campus_code"`) || !strings.Contains(body, "<select") {
		t.Error("the campus field is not a select backed by the catalog")
	}
	if !strings.Contains(body, "虎溪") {
		t.Error("the catalog campus is not offered")
	}
	// Building and group must come from the catalog too, not from free text.
	if !strings.Contains(body, `name="building_code"`) {
		t.Error("the building field is missing")
	}
	if strings.Contains(body, `name="campus_name"`) {
		t.Error("the form still asks for a free-text campus name; the catalog owns it")
	}
}

// TestProbeCreateIgnoresSubmittedNames pins the catalog as the only source of a
// probe's location text: a body that still carries campus_name/building_name
// must not be able to inject one.
func TestProbeCreateIgnoresSubmittedNames(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	seedProbeCatalog(t, h)
	page := h.get(t, "/admin/probes/new", cookie)
	csrf := csrfFrom(t, page.Body.String())

	form := validProbeForm(csrf)
	form.Set("campus_name", "注入的校区")
	form.Set("building_name", "注入的楼栋")
	form.Set("building_group_name", "注入的楼栋群")
	rec := h.post(t, "/admin/probes/new", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}

	id := probeIDPattern.FindString(h.get(t, rec.Header().Get("Location"), cookie).Body.String())
	got, err := h.store.GetProbe(id)
	if err != nil {
		t.Fatalf("GetProbe() error = %v", err)
	}
	if got.CampusName != "虎溪" || got.BuildingGroupName != "松园" || got.BuildingName != "松园一栋" {
		t.Errorf("display names did not come from the catalog: %+v", got)
	}
}

func TestProbeCreateRejectsLocationOutsideCatalog(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	csrf := csrfFrom(t, h.get(t, "/admin/probes/new", cookie).Body.String())

	if err := h.store.CreateCampus(&store.Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	if err := h.store.CreateBuilding(&store.Building{Code: "sy01", CampusCode: "hx",
		BuildingGroupCode: "sy", BuildingGroupName: "松园", Name: "松园一栋"}); err != nil {
		t.Fatalf("CreateBuilding() error = %v", err)
	}
	if err := h.store.CreateCampus(&store.Campus{Code: "aq", Name: "A区"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}

	form := validProbeForm(csrf)
	form.Set("campus_code", "hx")
	form.Set("building_code", "sy01")

	// In-catalog and agreeing: accepted.
	if rec := h.post(t, "/admin/probes/new", form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("in-catalog create status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}

	cases := map[string]struct{ campus, building string }{
		"campus not in catalog":   {"nope", "sy01"},
		"building not in catalog": {"hx", "nope"},
		// The pair must agree: sy01 belongs to hx, not aq.
		"mismatched pair": {"aq", "sy01"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := validProbeForm(csrf)
			f.Set("campus_code", tc.campus)
			f.Set("building_code", tc.building)
			rec := h.post(t, "/admin/probes/new", f, cookie)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestProbeFormEmptyCatalogExplainsItself(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	body := h.get(t, "/admin/probes/new", cookie).Body.String()
	if !strings.Contains(body, "校区") {
		t.Error("the page does not explain that a campus must be configured first")
	}
}
