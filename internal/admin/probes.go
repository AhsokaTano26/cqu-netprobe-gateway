package admin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/tano/cqu-netprobe-gateway/internal/store"
	"github.com/tano/cqu-netprobe-gateway/internal/token"
	"github.com/tano/cqu-netprobe-gateway/internal/webui"
)

// codePattern constrains every code that reaches a Prometheus label or a probe
// ID. Free text here would let a typo like "Wired" or "无线" create a permanent
// extra series in Prometheus.
var codePattern = regexp.MustCompile(`^[a-z0-9_]{1,16}$`)

// networkTypes is the closed enum for network_type.
var networkTypes = map[string]bool{"wired": true, "wireless": true}

// probeIDRandomBytes yields a 6-character hex suffix.
const probeIDRandomBytes = 3

func validCode(s string) bool { return codePattern.MatchString(s) }

// generateProbeID builds "{campus}-{building}-{6 hex}".
func generateProbeID(campusCode, buildingCode string) (string, error) {
	return probeIDSource(campusCode, buildingCode)
}

// probeIDSource is the random source generateProbeID draws from. It is a
// variable so a test can force the ID-collision retry, which a 24-bit suffix
// makes unreachable by accident.
var probeIDSource = func(campusCode, buildingCode string) (string, error) {
	buf := make([]byte, probeIDRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("admin: read random bytes: %w", err)
	}
	return campusCode + "-" + buildingCode + "-" + hex.EncodeToString(buf), nil
}

// probeRow is one line of the probe list.
type probeRow struct {
	Probe    store.Probe
	Online   bool
	LastSeen time.Time
}

// onlineState mirrors the metrics collector's rule so the UI and /metrics never
// disagree about whether a probe is online.
func (s *Server) onlineState(p store.Probe, now time.Time) (bool, time.Time) {
	if !p.Enabled {
		return false, time.Time{}
	}
	entry, ok := s.latest.Get(p.ProbeID)
	if !ok {
		return false, time.Time{}
	}
	return now.Sub(entry.ServerReceivedAt) <= s.onlineThreshold, entry.ServerReceivedAt
}

func (s *Server) handleProbeList(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	probes, err := s.store.ListProbes()
	if err != nil {
		s.logger.Error("failed to list probes", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	now := s.now()
	rows := make([]probeRow, 0, len(probes))
	for _, p := range probes {
		online, lastSeen := s.onlineState(p, now)
		rows = append(rows, probeRow{Probe: p, Online: online, LastSeen: lastSeen})
	}

	webui.Render(w, http.StatusOK, s.templates, "probes.html", webui.PageData{
		Title:    "Probes",
		Username: sess.username,
		CSRF:     sess.csrf,
		Pages:    map[string]any{"probes": rows, "now": now},
	})
}

func (s *Server) handleProbeNewForm(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	webui.Render(w, http.StatusOK, s.templates, "probe_new.html", webui.PageData{
		Title:    "新建 Probe",
		Username: sess.username,
		CSRF:     sess.csrf,
		Pages:    map[string]any{"form": probeFormValues{networkType: "wired"}.view()},
	})
}

// probeFormValues is the submitted create form, retained so a validation error
// can re-render the form without retyping.
type probeFormValues struct {
	campusCode, campusName               string
	buildingGroupCode, buildingGroupName string
	buildingCode, buildingName           string
	networkType, description             string
}

// view exposes the submitted values to the template, which reads them by form
// field name. The fields are unexported, so a map is the only shape the
// template engine can reach them through.
func (v probeFormValues) view() map[string]string {
	return map[string]string{
		"campus_code":         v.campusCode,
		"campus_name":         v.campusName,
		"building_group_code": v.buildingGroupCode,
		"building_group_name": v.buildingGroupName,
		"building_code":       v.buildingCode,
		"building_name":       v.buildingName,
		"network_type":        v.networkType,
		"description":         v.description,
	}
}

func readProbeForm(r *http.Request) probeFormValues {
	return probeFormValues{
		campusCode:        strings.TrimSpace(r.PostFormValue("campus_code")),
		campusName:        strings.TrimSpace(r.PostFormValue("campus_name")),
		buildingGroupCode: strings.TrimSpace(r.PostFormValue("building_group_code")),
		buildingGroupName: strings.TrimSpace(r.PostFormValue("building_group_name")),
		buildingCode:      strings.TrimSpace(r.PostFormValue("building_code")),
		buildingName:      strings.TrimSpace(r.PostFormValue("building_name")),
		networkType:       strings.TrimSpace(r.PostFormValue("network_type")),
		description:       strings.TrimSpace(r.PostFormValue("description")),
	}
}

func (v probeFormValues) validate() string {
	for _, c := range []struct{ name, value string }{
		{"校区代号", v.campusCode},
		{"楼栋群代号", v.buildingGroupCode},
		{"楼栋代号", v.buildingCode},
	} {
		if !validCode(c.value) {
			return c.name + "只能使用 1-16 位小写字母、数字或下划线"
		}
	}
	for _, c := range []struct{ name, value string }{
		{"校区显示名", v.campusName},
		{"楼栋群显示名", v.buildingGroupName},
		{"楼栋显示名", v.buildingName},
	} {
		if c.value == "" {
			return c.name + "不能为空"
		}
		if len([]rune(c.value)) > 64 {
			return c.name + "过长"
		}
	}
	if !networkTypes[v.networkType] {
		return "网络类型必须是 wired 或 wireless"
	}
	if len([]rune(v.description)) > 256 {
		return "备注过长"
	}
	return ""
}

func (s *Server) handleProbeCreate(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	form := readProbeForm(r)

	if msg := form.validate(); msg != "" {
		webui.Render(w, http.StatusBadRequest, s.templates, "probe_new.html", webui.PageData{
			Title: "新建 Probe", Username: sess.username, CSRF: sess.csrf, Error: msg,
			Pages: map[string]any{"form": form.view()},
		})
		return
	}

	rawToken, err := token.Generate()
	if err != nil {
		s.logger.Error("failed to generate token", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	probe := &store.Probe{
		TokenHash:         token.Hash(rawToken),
		CampusCode:        form.campusCode,
		CampusName:        form.campusName,
		BuildingGroupCode: form.buildingGroupCode,
		BuildingGroupName: form.buildingGroupName,
		BuildingCode:      form.buildingCode,
		BuildingName:      form.buildingName,
		NetworkType:       form.networkType,
		Enabled:           true,
		Description:       form.description,
	}

	// Retry on the astronomically unlikely ID collision rather than surfacing a
	// confusing duplicate error to the operator.
	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		id, err := generateProbeID(form.campusCode, form.buildingCode)
		if err != nil {
			s.logger.Error("failed to generate probe id", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		probe.ProbeID = id
		err = s.store.CreateProbe(probe)
		if err == nil {
			break
		}
		if !errors.Is(err, store.ErrDuplicate) {
			s.logger.Error("failed to create probe", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if attempt == maxAttempts-1 {
			s.logger.Error("exhausted probe id attempts")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	// Hand the plaintext token, and the ID it belongs to, to exactly one render.
	slot, err := s.oneShot.putPair(probe.ProbeID, rawToken)
	if err != nil {
		s.logger.Error("failed to store one-shot token", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Info("probe created", "probe_id", probe.ProbeID, "campus", probe.CampusCode,
		"building", probe.BuildingCode, "admin", sess.username)

	http.Redirect(w, r, "/admin/token/"+slot, http.StatusSeeOther)
}

func (s *Server) handleTokenShow(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	slot := r.PathValue("slot")

	// take deletes the slot, so a refresh or a back-button finds nothing.
	probeID, rawToken, ok := s.oneShot.takePair(slot)
	if !ok {
		webui.Render(w, http.StatusGone, s.templates, "token.html", webui.PageData{
			Title: "Token 已失效", Username: sess.username, CSRF: sess.csrf,
			Error: "该 Token 已显示过或已过期，无法再次查看。如需新凭据请轮换 Token。",
			Pages: map[string]any{"Token": "", "ProbeID": "", "PushEndpoint": ""},
		})
		return
	}

	webui.Render(w, http.StatusOK, s.templates, "token.html", webui.PageData{
		Title: "Probe Token", Username: sess.username, CSRF: sess.csrf,
		Pages: map[string]any{
			"Token":        rawToken,
			"ProbeID":      probeID,
			"PushEndpoint": strings.TrimSuffix(s.cfg.PublicBaseURL, "/") + "/api/v1/push",
		},
	})
}

func (s *Server) handleProbeDetail(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	id := r.PathValue("id")

	probe, err := s.store.GetProbe(id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.logger.Error("failed to load probe", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	online, lastSeen := s.onlineState(*probe, s.now())
	webui.Render(w, http.StatusOK, s.templates, "probe_detail.html", webui.PageData{
		Title: id, Username: sess.username, CSRF: sess.csrf,
		Pages: map[string]any{"probe": probeRow{Probe: *probe, Online: online, LastSeen: lastSeen}},
	})
}

func (s *Server) handleProbeToggle(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	id := r.PathValue("id")

	probe, err := s.store.GetProbe(id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.logger.Error("failed to load probe", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := s.store.SetProbeEnabled(id, !probe.Enabled); err != nil {
		s.logger.Error("failed to toggle probe", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// A disabled probe must not keep a rate-limit bucket or a stale measurement
	// that would resurface as "online" the moment it is re-enabled.
	if probe.Enabled {
		s.limiter.Remove(id)
		s.latest.Delete(id)
	}
	s.logger.Info("probe toggled", "probe_id", id, "enabled", !probe.Enabled, "admin", sess.username)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) handleProbeRotate(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	id := r.PathValue("id")

	if _, err := s.store.GetProbe(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.logger.Error("failed to load probe", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rawToken, err := token.Generate()
	if err != nil {
		s.logger.Error("failed to generate token", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.store.UpdateProbeToken(id, token.Hash(rawToken)); err != nil {
		s.logger.Error("failed to rotate token", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	slot, err := s.oneShot.putPair(id, rawToken)
	if err != nil {
		s.logger.Error("failed to store one-shot token", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Info("probe token rotated", "probe_id", id, "admin", sess.username)
	http.Redirect(w, r, "/admin/token/"+slot, http.StatusSeeOther)
}

func (s *Server) handleProbeDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	id := r.PathValue("id")

	if err := s.store.DeleteProbe(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.logger.Error("failed to delete probe", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Clean up the in-memory state so a new probe reusing the ID starts clean.
	s.latest.Delete(id)
	s.limiter.Remove(id)

	s.logger.Info("probe deleted", "probe_id", id, "admin", sess.username)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}
