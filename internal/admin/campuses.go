package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tano/cqu-netprobe-gateway/internal/store"
	"github.com/tano/cqu-netprobe-gateway/internal/webui"
)

// campusRow is one line of the campus list.
type campusRow struct {
	Campus    store.Campus
	Buildings int
}

func (s *Server) handleCampusList(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	s.renderCampuses(w, sess, http.StatusOK, "")
}

func (s *Server) handleCampusCreate(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	code := strings.TrimSpace(r.PostFormValue("code"))
	name := strings.TrimSpace(r.PostFormValue("name"))

	if msg := validateCatalogNames(code, name); msg != "" {
		s.renderCampuses(w, sess, http.StatusBadRequest, msg)
		return
	}
	err := s.store.CreateCampus(&store.Campus{Code: code, Name: name})
	if errors.Is(err, store.ErrDuplicate) {
		s.renderCampuses(w, sess, http.StatusBadRequest, "代号 "+code+" 已存在")
		return
	}
	if err != nil {
		s.logger.Error("failed to create campus", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Info("campus created", "code", code, "admin", sess.username)
	http.Redirect(w, r, "/admin/campuses", http.StatusSeeOther)
}

func (s *Server) handleCampusUpdate(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	code := r.PathValue("code") // the path is authoritative; the form's code is ignored
	name := strings.TrimSpace(r.PostFormValue("name"))

	if !validCode(code) {
		http.NotFound(w, r)
		return
	}
	if msg := validateName(name); msg != "" {
		s.renderCampuses(w, sess, http.StatusBadRequest, msg)
		return
	}
	if err := s.store.UpdateCampusName(code, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.logger.Error("failed to update campus", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Info("campus renamed", "code", code, "admin", sess.username)
	http.Redirect(w, r, "/admin/campuses", http.StatusSeeOther)
}

func (s *Server) handleCampusDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	code := r.PathValue("code")

	err := s.store.DeleteCampus(code)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, store.ErrInUse) {
		// Deliberately no count in this message. CampusProbeCount counts by
		// campus_code only, while DeleteCampus also refuses when a probe points
		// at one of the campus's buildings — so the count can legitimately be 0
		// on a refusal, and "仍有 0 个探针使用" would read as a bug.
		s.renderCampuses(w, sess, http.StatusConflict,
			"校区 "+code+" 仍被探针引用（直接引用，或引用了它名下的楼栋），请先迁移或删除这些探针")
		return
	}
	if err != nil {
		s.logger.Error("failed to delete campus", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Info("campus deleted", "code", code, "admin", sess.username)
	http.Redirect(w, r, "/admin/campuses", http.StatusSeeOther)
}

// renderCampuses reloads the list and renders it, carrying an optional error.
func (s *Server) renderCampuses(w http.ResponseWriter, sess *session, status int, errMsg string) {
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
	byCampus := map[string]int{}
	for _, b := range buildings {
		byCampus[b.CampusCode]++
	}

	rows := make([]campusRow, 0, len(campuses))
	for _, c := range campuses {
		rows = append(rows, campusRow{Campus: c, Buildings: byCampus[c.Code]})
	}

	webui.Render(w, status, s.templates, "campuses.html", webui.PageData{
		Title: "校区", Username: sess.username, CSRF: sess.csrf, Error: errMsg,
		Pages: map[string]any{"campuses": rows},
	})
}

// validateCatalogNames checks a campus/building code (which reaches a
// Prometheus label and a probe_id) together with its display name.
func validateCatalogNames(code, name string) string {
	if !validCode(code) {
		return "代号只能使用 1-16 位小写字母、数字或下划线"
	}
	return validateName(name)
}

func validateName(name string) string {
	if name == "" {
		return "显示名不能为空"
	}
	if len([]rune(name)) > 64 {
		return "显示名过长"
	}
	return ""
}
