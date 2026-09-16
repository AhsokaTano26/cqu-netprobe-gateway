package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tano/cqu-netprobe-gateway/internal/store"
)

func campusForm(csrf, code, name string) url.Values {
	return url.Values{"csrf": {csrf}, "code": {code}, "name": {name}}
}

func TestCampusCreateListAndRename(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	csrf := csrfFrom(t, h.get(t, "/admin/probes/new", cookie).Body.String())

	rec := h.post(t, "/admin/campuses/new", campusForm(csrf, "aq", "A区"), cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}

	list := h.get(t, "/admin/campuses", cookie)
	if !strings.Contains(list.Body.String(), "A区") {
		t.Error("the new campus is not on the list page")
	}

	if rec := h.post(t, "/admin/campuses/aq/update", campusForm(csrf, "IGNORED", "A区(改)"), cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, want 303", rec.Code)
	}
	got, err := h.store.GetCampus("aq")
	if err != nil {
		t.Fatalf("GetCampus() error = %v", err)
	}
	if got.Name != "A区(改)" {
		t.Errorf("Name = %q, want A区(改)", got.Name)
	}
	if got.Code != "aq" {
		t.Errorf("Code = %q; the form's code field must be ignored", got.Code)
	}
}

func TestCampusCreateRejectsBadCodeAndDuplicate(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	csrf := csrfFrom(t, h.get(t, "/admin/probes/new", cookie).Body.String())

	for _, bad := range []string{"", "AQ", "a-q", "新校区", "aaaaaaaaaaaaaaaaa"} {
		if rec := h.post(t, "/admin/campuses/new", campusForm(csrf, bad, "x"), cookie); rec.Code == http.StatusSeeOther {
			t.Fatalf("bad campus code %q was accepted", bad)
		}
	}
	if rec := h.post(t, "/admin/campuses/new", campusForm(csrf, "aq", "A区"), cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("first create status = %d", rec.Code)
	}
	rec := h.post(t, "/admin/campuses/new", campusForm(csrf, "aq", "dup"), cookie)
	if rec.Code == http.StatusSeeOther {
		t.Fatal("a duplicate campus code was accepted")
	}
	if !strings.Contains(rec.Body.String(), "已存在") {
		t.Errorf("duplicate error not surfaced to the operator: %s", rec.Body.String())
	}
}

// seedProbeForAdmin inserts a probe directly, so a test can exercise the
// catalog's reference counting without driving the whole probe-creation form.
// The shared harness deliberately creates no probes and no catalog rows: adding
// them there would change the fixture every existing admin test depends on.
func seedProbeForAdmin(t *testing.T, h *adminHarness, probeID, tokenHash string) {
	t.Helper()
	p := &store.Probe{
		ProbeID: probeID, TokenHash: tokenHash,
		CampusCode: "hx", CampusName: "虎溪",
		BuildingGroupCode: "sy", BuildingGroupName: "松园",
		BuildingCode: "sy01", BuildingName: "松园一栋",
		NetworkType: "wired", Enabled: true,
	}
	if err := h.store.CreateProbe(p); err != nil {
		t.Fatalf("CreateProbe(%s) error = %v", probeID, err)
	}
}

func TestCampusDeleteBlockedWhileProbesReferenceIt(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	csrf := csrfFrom(t, h.get(t, "/admin/probes/new", cookie).Body.String())

	if err := h.store.CreateCampus(&store.Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	seedProbeForAdmin(t, h, "hx-sy01-aaaaaa", "hash-a")

	rec := h.post(t, "/admin/campuses/hx/delete", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 while probes reference the campus; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "探针") {
		t.Errorf("the message does not explain that probes reference it: %s", rec.Body.String())
	}
	// The campus must survive the refusal — that is the damage the guard prevents.
	if _, err := h.store.GetCampus("hx"); err != nil {
		t.Fatalf("GetCampus(hx) after the refused delete = %v", err)
	}

	// An unreferenced campus deletes.
	if r := h.post(t, "/admin/campuses/new", campusForm(csrf, "zz", "空校区"), cookie); r.Code != http.StatusSeeOther {
		t.Fatalf("create unused campus status = %d", r.Code)
	}
	if r := h.post(t, "/admin/campuses/zz/delete", url.Values{"csrf": {csrf}}, cookie); r.Code != http.StatusSeeOther {
		t.Fatalf("delete unused campus status = %d", r.Code)
	}
}

func TestCampusRoutesRequireAuthAndCSRF(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")

	for _, path := range []string{"/admin/campuses/new", "/admin/campuses/x/update", "/admin/campuses/x/delete"} {
		if rec := h.post(t, path, url.Values{}, nil); rec.Code != http.StatusSeeOther {
			t.Errorf("unauthenticated POST %s = %d, want 303", path, rec.Code)
		}
	}
	cookie := h.login(t)
	rec := h.post(t, "/admin/campuses/new", campusForm("wrong", "aa", "x"), cookie)
	if rec.Code != http.StatusForbidden {
		t.Errorf("CSRF-less POST = %d, want 403", rec.Code)
	}
}
