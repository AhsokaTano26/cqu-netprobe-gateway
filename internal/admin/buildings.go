package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tano/cqu-netprobe-gateway/internal/store"
	"github.com/tano/cqu-netprobe-gateway/internal/webui"
)

// buildingRow is one line of the building list, carrying its campus display
// name so the list can group by campus without a second lookup.
type buildingRow struct {
	Building   store.Building
	CampusName string
}

// buildingBlock is one campus's section of the list.
type buildingBlock struct {
	CampusCode string
	CampusName string
	Rows       []buildingRow
}

// campusCount drives the filter links: how many buildings each campus holds.
type campusCount struct {
	Code  string
	Name  string
	Count int
}

// buildingView is the page's per-request state, grouped into a struct because
// the render call has five callers and a positional list of five strings would
// be unreadable at every one of them.
type buildingView struct {
	// selectedCampus preselects the campus pickers.
	selectedCampus string
	// filterCampus hides every other campus's buildings; empty shows all. It is
	// separate from selectedCampus because "show everything" and "preselect the
	// first campus" are different intentions that both look like an empty string.
	filterCampus string
	// report is the outcome of a batch import, when this render follows one.
	report *importReport
}

func (s *Server) handleBuildingList(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	campus := strings.TrimSpace(r.URL.Query().Get("campus"))
	s.renderBuildings(w, sess, http.StatusOK, "", buildingView{
		selectedCampus: campus,
		filterCampus:   campus,
	})
}

// importReport is what a batch import did, rendered above the list. Added is a
// count rather than a list: the rows themselves appear in the table right below.
type importReport struct {
	Added   int
	Skipped []string
	Failed  []string
}

// importLine is one parsed line of a batch import. A line that could not be
// parsed carries its reason and is never applied.
type importLine struct {
	line                             int
	code, name, groupCode, groupName string
	problem                          string
}

// parseBuildingImport reads the textarea, one building per line.
//
// Blank lines and lines starting with # are skipped, so a pasted list can carry
// comments. Two shapes are accepted:
//
//	code,name                      -- takes the batch-wide group
//	code,name,group_code,group_name -- names its own group
//
// Comma or tab separates fields; a spreadsheet copy is tab-separated, which is
// the more common way these lists arrive.
func parseBuildingImport(text, defaultGroupCode, defaultGroupName string) []importLine {
	var out []importLine
	for i, raw := range strings.Split(text, "\n") {
		number := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == '\t' })
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}

		entry := importLine{line: number}
		switch len(fields) {
		case 2:
			entry.code, entry.name = fields[0], fields[1]
			entry.groupCode, entry.groupName = defaultGroupCode, defaultGroupName
			if entry.groupCode == "" || entry.groupName == "" {
				entry.problem = "只写了代号和显示名，但上方没有填统一楼栋群"
			}
		case 4:
			entry.code, entry.name = fields[0], fields[1]
			entry.groupCode, entry.groupName = fields[2], fields[3]
		default:
			entry.problem = "字段数不对（" + itoa(len(fields)) + " 个），需要 2 个或 4 个"
		}
		out = append(out, entry)
	}
	return out
}

func (s *Server) handleBuildingImport(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)

	campusCode := strings.TrimSpace(r.PostFormValue("campus_code"))
	defaultGroupCode := strings.TrimSpace(r.PostFormValue("building_group_code"))
	defaultGroupName := strings.TrimSpace(r.PostFormValue("building_group_name"))
	view := buildingView{selectedCampus: campusCode, filterCampus: campusCode}

	// The campus applies to the whole batch, so a bad one is one page-level
	// error rather than the same complaint repeated on every line.
	if msg := s.validateCampus(campusCode); msg != "" {
		s.renderBuildings(w, sess, http.StatusBadRequest, msg, view)
		return
	}

	lines := parseBuildingImport(r.PostFormValue("lines"), defaultGroupCode, defaultGroupName)
	if len(lines) == 0 {
		s.renderBuildings(w, sess, http.StatusBadRequest, "没有可导入的内容：每行一栋，用逗号或制表符分隔", view)
		return
	}

	report := &importReport{}
	for _, entry := range lines {
		if entry.problem != "" {
			report.Failed = append(report.Failed, "第 "+itoa(entry.line)+" 行："+entry.problem)
			continue
		}
		if msg := validateBuildingFields(entry.code, entry.groupCode, entry.groupName, entry.name); msg != "" {
			report.Failed = append(report.Failed,
				"第 "+itoa(entry.line)+" 行 "+entry.code+"："+msg)
			continue
		}

		err := s.store.CreateBuilding(&store.Building{
			Code: entry.code, CampusCode: campusCode,
			BuildingGroupCode: entry.groupCode, BuildingGroupName: entry.groupName,
			Name: entry.name,
		})
		if errors.Is(err, store.ErrDuplicate) {
			// Reported rather than treated as a failure: re-pasting a list after
			// fixing a few lines is the normal way to use this, and the second
			// pass must not be an error.
			report.Skipped = append(report.Skipped, entry.code)
			continue
		}
		if err != nil {
			s.logger.Error("failed to import building", "code", entry.code, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		report.Added++
	}

	if report.Added > 0 {
		s.logger.Info("buildings imported", "campus", campusCode, "added", report.Added,
			"skipped", len(report.Skipped), "failed", len(report.Failed), "admin", sess.username)
	}
	// Rendered rather than redirected: the report is the point of the request.
	// Re-submitting it is harmless — the second pass skips what the first added.
	view.report = report
	s.renderBuildings(w, sess, http.StatusOK, "", view)
}

func (s *Server) handleBuildingCreate(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)

	code := strings.TrimSpace(r.PostFormValue("code"))
	campusCode := strings.TrimSpace(r.PostFormValue("campus_code"))
	groupCode := strings.TrimSpace(r.PostFormValue("building_group_code"))
	groupName := strings.TrimSpace(r.PostFormValue("building_group_name"))
	name := strings.TrimSpace(r.PostFormValue("name"))

	if msg := s.validateBuilding(code, campusCode, groupCode, groupName, name); msg != "" {
		s.renderBuildings(w, sess, http.StatusBadRequest, msg, buildingView{selectedCampus: campusCode, filterCampus: campusCode})
		return
	}

	b := &store.Building{
		Code: code, CampusCode: campusCode,
		BuildingGroupCode: groupCode, BuildingGroupName: groupName, Name: name,
	}
	err := s.store.CreateBuilding(b)
	if errors.Is(err, store.ErrDuplicate) {
		s.renderBuildings(w, sess, http.StatusBadRequest, "楼栋代号 "+code+" 已存在", buildingView{selectedCampus: campusCode, filterCampus: campusCode})
		return
	}
	if err != nil {
		s.logger.Error("failed to create building", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Info("building created", "code", code, "campus", campusCode, "admin", sess.username)
	http.Redirect(w, r, "/admin/buildings", http.StatusSeeOther)
}

func (s *Server) handleBuildingUpdate(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	code := r.PathValue("code") // the path is authoritative; the form's code is ignored

	existing, err := s.store.GetBuilding(code)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.logger.Error("failed to load building", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	groupCode := strings.TrimSpace(r.PostFormValue("building_group_code"))
	groupName := strings.TrimSpace(r.PostFormValue("building_group_name"))
	name := strings.TrimSpace(r.PostFormValue("name"))

	// The campus is not editable either: moving a building between campuses
	// would silently relocate every probe already reporting from it.
	if msg := s.validateBuilding("", existing.CampusCode, groupCode, groupName, name); msg != "" {
		// The form carries the list filter it was submitted under, so a failed
		// save returns the operator to the view they were editing from.
		s.renderBuildings(w, sess, http.StatusBadRequest, msg, buildingView{
			selectedCampus: existing.CampusCode,
			filterCampus:   strings.TrimSpace(r.PostFormValue("filter")),
		})
		return
	}

	updated := &store.Building{
		Code: code, CampusCode: existing.CampusCode,
		BuildingGroupCode: groupCode, BuildingGroupName: groupName, Name: name,
	}
	if err := s.store.UpdateBuilding(updated); err != nil {
		s.logger.Error("failed to update building", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Info("building updated", "code", code, "admin", sess.username)
	http.Redirect(w, r, "/admin/buildings", http.StatusSeeOther)
}

func (s *Server) handleBuildingDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	code := r.PathValue("code")

	err := s.store.DeleteBuilding(code)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, store.ErrInUse) {
		n, _ := s.store.BuildingProbeCount(code)
		s.renderBuildings(w, sess, http.StatusConflict,
			"仍有 "+itoa(n)+" 个探针使用楼栋 "+code+"，请先迁移或删除这些探针",
			buildingView{filterCampus: strings.TrimSpace(r.PostFormValue("filter"))})
		return
	}
	if err != nil {
		s.logger.Error("failed to delete building", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Info("building deleted", "code", code, "admin", sess.username)
	http.Redirect(w, r, "/admin/buildings", http.StatusSeeOther)
}

// validateBuilding checks a building's fields. code may be empty on the update
// path, where the path segment is authoritative and the code is immutable.
func (s *Server) validateBuilding(code, campusCode, groupCode, groupName, name string) string {
	if code != "" && !validCode(code) {
		return "楼栋代号只能使用 1-16 位小写字母、数字或下划线"
	}
	if msg := s.validateCampus(campusCode); msg != "" {
		return msg
	}
	return validateBuildingGroup(groupCode, groupName, name)
}

// validateCampus reports whether the campus exists.
func (s *Server) validateCampus(campusCode string) string {
	if campusCode == "" {
		return "请选择校区"
	}
	if _, err := s.store.GetCampus(campusCode); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "校区 " + campusCode + " 不存在，请从列表中选择"
		}
		return "无法校验校区，请稍后重试"
	}
	return ""
}

// validateBuildingFields is the batch import's per-line check. It differs from
// validateBuilding in two ways the import needs: the campus is checked once for
// the whole batch rather than per line, and the code is always required — an
// import line has no path segment standing in for it.
func validateBuildingFields(code, groupCode, groupName, name string) string {
	if !validCode(code) {
		return "楼栋代号只能使用 1-16 位小写字母、数字或下划线"
	}
	return validateBuildingGroup(groupCode, groupName, name)
}

// validateBuildingGroup checks everything a building carries except its campus
// and code: the group is what a typo here turns into an unmergeable Prometheus
// series, so it is checked on every path that writes one.
func validateBuildingGroup(groupCode, groupName, name string) string {
	if !validCode(groupCode) {
		return "楼栋群代号只能使用 1-16 位小写字母、数字或下划线"
	}
	if msg := validateName(groupName); msg != "" {
		return "楼栋群显示名：" + msg
	}
	return validateName(name)
}

// renderBuildings reloads the catalog and renders the page. view carries the
// campus pickers' selection, the list filter and any import report, so a
// validation failure gives the operator back the page they were looking at.
func (s *Server) renderBuildings(w http.ResponseWriter, sess *session, status int, errMsg string, view buildingView) {
	campuses, err := s.store.ListCampuses()
	if err != nil {
		s.logger.Error("failed to list campuses", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	buildings, err := s.store.ListBuildings()
	if err != nil {
		s.logger.Error("failed to list buildings", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	groups, err := s.store.BuildingGroups()
	if err != nil {
		s.logger.Error("failed to list building groups", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	names := map[string]string{}
	counts := make([]campusCount, 0, len(campuses))
	all := make([]buildingRow, 0, len(buildings))
	for _, c := range campuses {
		names[c.Code] = c.Name
	}
	for _, b := range buildings {
		all = append(all, buildingRow{Building: b, CampusName: names[b.CampusCode]})
	}
	for _, c := range campuses {
		n := 0
		for _, b := range buildings {
			if b.CampusCode == c.Code {
				n++
			}
		}
		counts = append(counts, campusCount{Code: c.Code, Name: c.Name, Count: n})
	}

	// A filter naming a campus that no longer exists shows an empty list rather
	// than silently falling back to everything: the empty page is the honest
	// answer, and the campus links above it are right there.
	visible := all
	if view.filterCampus != "" {
		visible = make([]buildingRow, 0, len(all))
		for _, row := range all {
			if row.Building.CampusCode == view.filterCampus {
				visible = append(visible, row)
			}
		}
	}

	// Grouped by campus in the order ListBuildings returns, which is campus then
	// code, so blocks come out contiguous and in a stable order.
	blocks := make([]buildingBlock, 0, len(campuses))
	index := map[string]int{}
	for _, row := range visible {
		i, ok := index[row.Building.CampusCode]
		if !ok {
			blocks = append(blocks, buildingBlock{
				CampusCode: row.Building.CampusCode,
				CampusName: row.CampusName,
			})
			i = len(blocks) - 1
			index[row.Building.CampusCode] = i
		}
		blocks[i].Rows = append(blocks[i].Rows, row)
	}

	if view.selectedCampus == "" && len(campuses) > 0 {
		view.selectedCampus = campuses[0].Code
	}

	webui.Render(w, status, s.templates, "buildings.html", webui.PageData{
		Title: "楼栋", Username: sess.username, CSRF: sess.csrf, Error: errMsg,
		Pages: map[string]any{
			"blocks": blocks, "total": len(all), "campusCounts": counts,
			"campuses": campuses, "groups": groups,
			"selectedCampus": view.selectedCampus, "filterCampus": view.filterCampus,
			"report": view.report,
		},
	})
}
