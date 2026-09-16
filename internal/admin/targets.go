package admin

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
	"github.com/tano/cqu-netprobe-gateway/internal/webui"
)

// targetIDPattern is tighter than codePattern: a target ID is a long-lived
// Prometheus label value, so dashes are excluded to keep PromQL unambiguous.
var targetIDPattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

func validTargetID(s string) bool { return targetIDPattern.MatchString(s) }

// validProbeType reports whether pt is one of the three Protocol v1 types.
func validProbeType(s string) bool {
	switch protocol.ProbeType(s) {
	case protocol.ProbeICMP, protocol.ProbeDNS, protocol.ProbeHTTP:
		return true
	default:
		return false
	}
}

func (s *Server) handleTargetList(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	targets, err := s.store.ListTargets()
	if err != nil {
		s.logger.Error("failed to list targets", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	webui.Render(w, http.StatusOK, s.templates, "targets.html", webui.PageData{
		Title: "Targets", Username: sess.username, CSRF: sess.csrf,
		Pages: map[string]any{"targets": targets},
	})
}

// readTargetForm reads and validates the shared create/update form. It returns
// a user-facing message on failure.
func readTargetForm(r *http.Request) (*store.Target, string) {
	targetID := strings.TrimSpace(r.PostFormValue("target_id"))
	displayName := strings.TrimSpace(r.PostFormValue("display_name"))
	address := strings.TrimSpace(r.PostFormValue("address"))
	description := strings.TrimSpace(r.PostFormValue("description"))
	enabled := r.PostFormValue("enabled") != ""

	var probeTypes []string
	seen := map[string]bool{}
	for _, pt := range r.PostForm["probe_types"] {
		if !validProbeType(pt) {
			return nil, "Probe Type 只能是 icmp、dns 或 http"
		}
		if !seen[pt] {
			seen[pt] = true
			probeTypes = append(probeTypes, pt)
		}
	}
	if len(probeTypes) == 0 {
		return nil, "至少选择一种 Probe Type"
	}
	if len([]rune(displayName)) > 64 || len([]rune(address)) > 256 || len([]rune(description)) > 256 {
		return nil, "字段过长"
	}

	return &store.Target{
		TargetID:    targetID,
		DisplayName: displayName,
		Address:     address,
		Description: description,
		Enabled:     enabled,
		ProbeTypes:  store.ProbeTypesSorted(probeTypes),
	}, ""
}

func (s *Server) handleTargetCreate(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	target, msg := readTargetForm(r)
	if msg == "" && !validTargetID(target.TargetID) {
		msg = "Target ID 只能使用 1-32 位小写字母、数字或下划线"
	}
	if msg != "" {
		s.renderTargetsWithError(w, sess, msg, http.StatusBadRequest)
		return
	}

	err := s.store.CreateTarget(target)
	if errors.Is(err, store.ErrDuplicate) {
		// The form doubles as an edit form: an existing ID updates in place.
		err = s.store.UpdateTarget(target)
	}
	if err != nil {
		s.logger.Error("failed to save target", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Info("target saved", "target_id", target.TargetID, "admin", sess.username)
	http.Redirect(w, r, "/admin/targets", http.StatusSeeOther)
}

func (s *Server) handleTargetUpdate(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	id := r.PathValue("id")

	target, msg := readTargetForm(r)
	if msg == "" {
		// The path ID is authoritative; the form field is ignored so a crafted
		// form cannot rename a target through the update route.
		target.TargetID = id
	}
	if msg != "" {
		s.renderTargetsWithError(w, sess, msg, http.StatusBadRequest)
		return
	}

	if err := s.store.UpdateTarget(target); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.logger.Error("failed to update target", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Info("target updated", "target_id", id, "admin", sess.username)
	http.Redirect(w, r, "/admin/targets", http.StatusSeeOther)
}

func (s *Server) handleTargetDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	id := r.PathValue("id")

	if err := s.store.DeleteTarget(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.logger.Error("failed to delete target", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Info("target deleted", "target_id", id, "admin", sess.username)
	http.Redirect(w, r, "/admin/targets", http.StatusSeeOther)
}

func (s *Server) renderTargetsWithError(w http.ResponseWriter, sess *session, msg string, status int) {
	targets, err := s.store.ListTargets()
	if err != nil {
		s.logger.Error("failed to list targets", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	webui.Render(w, status, s.templates, "targets.html", webui.PageData{
		Title: "Targets", Username: sess.username, CSRF: sess.csrf, Error: msg,
		Pages: map[string]any{"targets": targets},
	})
}
