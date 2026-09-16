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

// --- batch import ----------------------------------------------------------

func importForm(csrf, campus, groupCode, groupName, lines string) url.Values {
	return url.Values{
		"csrf":                {csrf},
		"campus_code":         {campus},
		"building_group_code": {groupCode},
		"building_group_name": {groupName},
		"lines":               {lines},
	}
}

// The parser is the part worth testing on its own: the handler around it only
// decides what to do with what it returns.
func TestParseBuildingImport(t *testing.T) {
	cases := []struct {
		name string
		text string
		// noBatchGroup leaves the form's group fields empty, which is a
		// different situation from filling them in: a two-field line then has
		// nowhere to take its group from.
		noBatchGroup bool
		want         []importLine
		// problems lists the line numbers expected to carry a parse failure.
		problems []int
	}{
		{
			name: "two fields take the batch group",
			text: "sy01,松园一栋\nsy02,松园二栋",
			want: []importLine{
				{line: 1, code: "sy01", name: "松园一栋", groupCode: "sy", groupName: "松园"},
				{line: 2, code: "sy02", name: "松园二栋", groupCode: "sy", groupName: "松园"},
			},
		},
		{
			name: "four fields name their own group",
			text: "zy01,竹园一栋,zy,竹园",
			want: []importLine{
				{line: 1, code: "zy01", name: "竹园一栋", groupCode: "zy", groupName: "竹园"},
			},
		},
		{
			// A spreadsheet paste arrives tab-separated, which is the way this
			// list is actually going to be produced.
			name: "tabs separate fields too",
			text: "sy03\t松园三栋\tsy\t松园",
			want: []importLine{
				{line: 1, code: "sy03", name: "松园三栋", groupCode: "sy", groupName: "松园"},
			},
		},
		{
			name: "blank and comment lines are skipped",
			text: "\n# 松园\nsy01,松园一栋\n\n",
			want: []importLine{{line: 3, code: "sy01", name: "松园一栋", groupCode: "sy", groupName: "松园"}},
		},
		{
			name:         "two fields with no batch group cannot be used",
			text:         "sy01,松园一栋",
			noBatchGroup: true,
			problems:     []int{1},
		},
		{
			name:     "one field is not enough",
			text:     "sy01",
			problems: []int{1},
		},
		{
			name:     "five fields is too many",
			text:     "sy01,松园一栋,sy,松园,多余",
			problems: []int{1},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			groupCode, groupName := "sy", "松园"
			if tc.noBatchGroup {
				groupCode, groupName = "", ""
			}
			got := parseBuildingImport(tc.text, groupCode, groupName)
			if tc.problems != nil {
				if len(got) != len(tc.problems) {
					t.Fatalf("parsed %d lines, want %d", len(got), len(tc.problems))
				}
				for i, line := range tc.problems {
					if got[i].line != line || got[i].problem == "" {
						t.Errorf("line %d: problem = %q, want a failure on line %d", i+1, got[i].problem, line)
					}
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parsed %d lines, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("line %d:\n got %+v\nwant %+v", i+1, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestBuildingImportAddsSkipsAndReports(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	if err := h.store.CreateCampus(&store.Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	csrf := csrfFrom(t, h.get(t, "/admin/buildings", cookie).Body.String())

	rec := h.post(t, "/admin/buildings/import", importForm(csrf, "hx", "sy", "松园",
		"sy01,松园一栋\nsy02,松园二栋,zy,竹园\nSY03,BAD CODE\n"), cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"新增 <strong>2</strong>", "失败 1 个", "SY03"} {
		if !strings.Contains(body, want) {
			t.Errorf("the report does not mention %q", want)
		}
	}

	// The per-line group override must win over the batch default.
	zy, err := h.store.GetBuilding("sy02")
	if err != nil {
		t.Fatalf("GetBuilding(sy02) error = %v", err)
	}
	if zy.BuildingGroupCode != "zy" || zy.BuildingGroupName != "竹园" {
		t.Errorf("sy02 group = %s/%s, want zy/竹园", zy.BuildingGroupCode, zy.BuildingGroupName)
	}
	sy, err := h.store.GetBuilding("sy01")
	if err != nil {
		t.Fatalf("GetBuilding(sy01) error = %v", err)
	}
	if sy.BuildingGroupCode != "sy" || sy.BuildingGroupName != "松园" {
		t.Errorf("sy01 group = %s/%s, want the batch default sy/松园", sy.BuildingGroupCode, sy.BuildingGroupName)
	}
	// The rejected line must not have been written.
	if _, err := h.store.GetBuilding("SY03"); err == nil {
		t.Error("a line that failed validation was imported anyway")
	}
}

// Re-pasting a corrected list is the normal way to use this, so the second pass
// must report the already-present rows as skipped rather than as an error.
func TestBuildingImportIsRepeatable(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	if err := h.store.CreateCampus(&store.Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	csrf := csrfFrom(t, h.get(t, "/admin/buildings", cookie).Body.String())
	lines := "sy01,松园一栋\nsy02,松园二栋"

	for i := 1; i <= 2; i++ {
		rec := h.post(t, "/admin/buildings/import", importForm(csrf, "hx", "sy", "松园", lines), cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("import %d status = %d, want 200", i, rec.Code)
		}
	}
	body := h.post(t, "/admin/buildings/import", importForm(csrf, "hx", "sy", "松园", lines), cookie).Body.String()
	if !strings.Contains(body, "新增 <strong>0</strong>") || !strings.Contains(body, "跳过 2 个") {
		t.Errorf("a repeated import does not report everything as skipped:\n%s", body)
	}

	buildings, err := h.store.ListBuildings()
	if err != nil {
		t.Fatalf("ListBuildings() error = %v", err)
	}
	if len(buildings) != 2 {
		t.Errorf("buildings = %d, want 2: the repeated import duplicated rows", len(buildings))
	}
}

func TestBuildingImportRejectsEmptyAndBadCampus(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	if err := h.store.CreateCampus(&store.Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	csrf := csrfFrom(t, h.get(t, "/admin/buildings", cookie).Body.String())

	if rec := h.post(t, "/admin/buildings/import",
		importForm(csrf, "hx", "sy", "松园", "\n# 只有注释\n"), cookie); rec.Code != http.StatusBadRequest {
		t.Errorf("empty import = %d, want 400", rec.Code)
	}
	// A bad campus is one page-level error, not one per line.
	rec := h.post(t, "/admin/buildings/import",
		importForm(csrf, "nope", "sy", "松园", "sy01,松园一栋\nsy02,松园二栋"), cookie)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown campus = %d, want 400", rec.Code)
	}
	if n := strings.Count(rec.Body.String(), "不存在"); n != 1 {
		t.Errorf("the missing campus is reported %d times, want once", n)
	}
}

func TestBuildingImportRequiresAuthAndCSRF(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	if rec := h.post(t, "/admin/buildings/import", url.Values{}, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("unauthenticated import = %d, want 303", rec.Code)
	}
	cookie := h.login(t)
	if rec := h.post(t, "/admin/buildings/import",
		importForm("wrong", "hx", "sy", "松园", "sy01,松园一栋"), cookie); rec.Code != http.StatusForbidden {
		t.Errorf("CSRF-less import = %d, want 403", rec.Code)
	}
}
