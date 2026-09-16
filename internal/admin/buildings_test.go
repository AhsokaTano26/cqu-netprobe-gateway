package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tano/cqu-netprobe-gateway/internal/store"
)

func buildingForm(csrf, code, campus, groupCode, groupName, name string) url.Values {
	return url.Values{
		"csrf": {csrf}, "code": {code}, "campus_code": {campus},
		"building_group_code": {groupCode}, "building_group_name": {groupName},
		"name": {name},
	}
}

func TestBuildingCreateListAndUpdate(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	csrf := csrfFrom(t, h.get(t, "/admin/probes/new", cookie).Body.String())

	// The harness seeds no catalog rows, and a building cannot be created under
	// a campus that does not exist, so this test brings its own.
	if err := h.store.CreateCampus(&store.Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}

	rec := h.post(t, "/admin/buildings/new",
		buildingForm(csrf, "sy09", "hx", "sy", "松园", "松园九栋"), cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}

	list := h.get(t, "/admin/buildings", cookie)
	if !strings.Contains(list.Body.String(), "松园九栋") {
		t.Error("the new building is not on the list page")
	}

	if rec := h.post(t, "/admin/buildings/sy09/update",
		buildingForm(csrf, "IGNORED", "hx", "sy", "松园", "松园九栋(改)"), cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, want 303", rec.Code)
	}
	got, err := h.store.GetBuilding("sy09")
	if err != nil {
		t.Fatalf("GetBuilding() error = %v", err)
	}
	if got.Name != "松园九栋(改)" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.Code != "sy09" {
		t.Errorf("Code = %q; the form's code field must be ignored", got.Code)
	}

	// A crafted campus_code must not move the building: relocating it would
	// silently relabel every probe already reporting from it. The forged campus
	// is one that does not exist, so a handler that read the field would reject
	// the update outright — this update must land and leave the campus alone.
	if rec := h.post(t, "/admin/buildings/sy09/update",
		buildingForm(csrf, "sy09", "nope", "sy", "松园", "松园九栋(改2)"), cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("update with a forged campus_code status = %d, want 303; body = %s",
			rec.Code, rec.Body.String())
	}
	moved, err := h.store.GetBuilding("sy09")
	if err != nil {
		t.Fatalf("GetBuilding() error = %v", err)
	}
	if moved.CampusCode != "hx" {
		t.Errorf("CampusCode = %q; the form's campus_code field must be ignored", moved.CampusCode)
	}
}

func TestBuildingCreateRejectsUnknownCampus(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	csrf := csrfFrom(t, h.get(t, "/admin/probes/new", cookie).Body.String())

	rec := h.post(t, "/admin/buildings/new",
		buildingForm(csrf, "zz01", "nope", "zz", "无", "无"), cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown campus; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "校区") {
		t.Errorf("error does not mention the campus: %s", rec.Body.String())
	}
}

func TestBuildingCreateRejectsEmptyGroupAndName(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	csrf := csrfFrom(t, h.get(t, "/admin/probes/new", cookie).Body.String())

	// The campus must exist, or its own validation error would answer first and
	// the group and name rules would never be reached.
	if err := h.store.CreateCampus(&store.Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}

	for name, form := range map[string]url.Values{
		"empty group code": buildingForm(csrf, "zz01", "hx", "", "松园", "松园一栋"),
		"bad group code":   buildingForm(csrf, "zz01", "hx", "SY", "松园", "松园一栋"),
		"empty name":       buildingForm(csrf, "zz01", "hx", "sy", "松园", ""),
	} {
		t.Run(name, func(t *testing.T) {
			if rec := h.post(t, "/admin/buildings/new", form, cookie); rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestBuildingDeleteBlockedWhileProbesReferenceIt(t *testing.T) {
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
	seedProbeForAdmin(t, h, "hx-sy01-aaaaaa", "hash-a")

	rec := h.post(t, "/admin/buildings/sy01/delete", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "探针") {
		t.Errorf("the message does not mention probes: %s", rec.Body.String())
	}

	if r := h.post(t, "/admin/buildings/new",
		buildingForm(csrf, "zz09", "hx", "zz", "空", "空楼栋"), cookie); r.Code != http.StatusSeeOther {
		t.Fatalf("create unused building status = %d", r.Code)
	}
	if r := h.post(t, "/admin/buildings/zz09/delete", url.Values{"csrf": {csrf}}, cookie); r.Code != http.StatusSeeOther {
		t.Fatalf("delete unused building status = %d", r.Code)
	}
}

func TestBuildingGroupsAreOfferedForReuse(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)

	if err := h.store.CreateCampus(&store.Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	if err := h.store.CreateBuilding(&store.Building{Code: "sy01", CampusCode: "hx",
		BuildingGroupCode: "sy", BuildingGroupName: "松园", Name: "松园一栋"}); err != nil {
		t.Fatalf("CreateBuilding() error = %v", err)
	}

	body := h.get(t, "/admin/buildings", cookie).Body.String()
	if !strings.Contains(body, "松园") {
		t.Error("existing building groups are not offered on the page")
	}
	if !strings.Contains(body, `name="building_group_code"`) {
		t.Error("the group picker is missing from the form")
	}
}

func TestBuildingRoutesRequireAuthAndCSRF(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	for _, path := range []string{"/admin/buildings/new", "/admin/buildings/x/update", "/admin/buildings/x/delete"} {
		if rec := h.post(t, path, url.Values{}, nil); rec.Code != http.StatusSeeOther {
			t.Errorf("unauthenticated POST %s = %d, want 303", path, rec.Code)
		}
	}
	cookie := h.login(t)
	if rec := h.post(t, "/admin/buildings/new", buildingForm("wrong", "aa", "hx", "aa", "A", "A"), cookie); rec.Code != http.StatusForbidden {
		t.Errorf("CSRF-less POST = %d, want 403", rec.Code)
	}
}

func TestBuildingsAreGroupedByCampusOnTheList(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	// Both campuses are created here: the harness seeds none, and the page can
	// only group by the campuses that exist.
	if err := h.store.CreateCampus(&store.Campus{Code: "aq", Name: "A区"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	if err := h.store.CreateCampus(&store.Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	if err := h.store.CreateBuilding(&store.Building{Code: "aq01", CampusCode: "aq",
		BuildingGroupCode: "aq", BuildingGroupName: "A区", Name: "A区一栋"}); err != nil {
		t.Fatalf("CreateBuilding() error = %v", err)
	}
	body := h.get(t, "/admin/buildings", cookie).Body.String()
	if !strings.Contains(body, "虎溪") || !strings.Contains(body, "A区") {
		t.Error("the list does not show both campuses as groups")
	}
}
