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

func (s *Server) handleBuildingList(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	s.renderBuildings(w, sess, http.StatusOK, "", r.URL.Query().Get("campus"))
}

func (s *Server) handleBuildingCreate(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)

	code := strings.TrimSpace(r.PostFormValue("code"))
	campusCode := strings.TrimSpace(r.PostFormValue("campus_code"))
	groupCode := strings.TrimSpace(r.PostFormValue("building_group_code"))
	groupName := strings.TrimSpace(r.PostFormValue("building_group_name"))
	name := strings.TrimSpace(r.PostFormValue("name"))

	if msg := s.validateBuilding(code, campusCode, groupCode, groupName, name); msg != "" {
		s.renderBuildings(w, sess, http.StatusBadRequest, msg, campusCode)
		return
	}

	b := &store.Building{
		Code: code, CampusCode: campusCode,
		BuildingGroupCode: groupCode, BuildingGroupName: groupName, Name: name,
	}
	err := s.store.CreateBuilding(b)
	if errors.Is(err, store.ErrDuplicate) {
		s.renderBuildings(w, sess, http.StatusBadRequest, "楼栋代号 "+code+" 已存在", campusCode)
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
		s.renderBuildings(w, sess, http.StatusBadRequest, msg, existing.CampusCode)
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
			"仍有 "+itoa(n)+" 个探针使用楼栋 "+code+"，请先迁移或删除这些探针", "")
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
	if campusCode == "" {
		return "请选择校区"
	}
	if _, err := s.store.GetCampus(campusCode); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "校区 " + campusCode + " 不存在，请从列表中选择"
		}
		return "无法校验校区，请稍后重试"
	}
	if !validCode(groupCode) {
		return "楼栋群代号只能使用 1-16 位小写字母、数字或下划线"
	}
	if msg := validateName(groupName); msg != "" {
		return "楼栋群显示名：" + msg
	}
	return validateName(name)
}

// renderBuildings reloads the catalog and renders the page. selectedCampus
// preselects the campus picker after a validation failure so the operator does
// not have to choose again.
func (s *Server) renderBuildings(w http.ResponseWriter, sess *session, status int, errMsg, selectedCampus string) {
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
	for _, c := range campuses {
		names[c.Code] = c.Name
	}
	rows := make([]buildingRow, 0, len(buildings))
	for _, b := range buildings {
		rows = append(rows, buildingRow{Building: b, CampusName: names[b.CampusCode]})
	}

	if selectedCampus == "" && len(campuses) > 0 {
		selectedCampus = campuses[0].Code
	}

	webui.Render(w, status, s.templates, "buildings.html", webui.PageData{
		Title: "楼栋", Username: sess.username, CSRF: sess.csrf, Error: errMsg,
		Pages: map[string]any{
			"rows": rows, "campuses": campuses, "groups": groups,
			"selectedCampus": selectedCampus,
		},
	})
}
