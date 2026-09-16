package admin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
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

// itoa keeps the import list in this package free of strconv for one call site.
func itoa(n int) string { return strconv.Itoa(n) }

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
	// PendingApproval is true for a probe created through the public page that
	// an administrator has not enabled yet. Without CreatedVia this would be
	// indistinguishable from a probe deliberately disabled for maintenance.
	PendingApproval bool
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

// probeFilters are the accepted values of the ?filter= query parameter.
var probeFilters = map[string]bool{
	"all": true, "online": true, "offline": true, "disabled": true, "pending": true,
}

func (s *Server) handleProbeList(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	probes, err := s.store.ListProbes()
	if err != nil {
		s.logger.Error("failed to list probes", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	filter := r.URL.Query().Get("filter")
	if !probeFilters[filter] {
		filter = "all"
	}

	now := s.now()
	rows := make([]probeRow, 0, len(probes))
	for _, p := range probes {
		online, lastSeen := s.onlineState(p, now)
		row := probeRow{
			Probe: p, Online: online, LastSeen: lastSeen,
			PendingApproval: p.CreatedVia == "public" && !p.Enabled,
		}
		if !matchesProbeFilter(filter, row) {
			continue
		}
		rows = append(rows, row)
	}

	webui.Render(w, http.StatusOK, s.templates, "probes.html", webui.PageData{
		Title: "Probes", Username: sess.username, CSRF: sess.csrf,
		Pages: map[string]any{"probes": rows, "now": now, "filter": filter,
			"counts": countProbeFilters(probes, s, now)},
	})
}

func matchesProbeFilter(filter string, row probeRow) bool {
	switch filter {
	case "pending":
		return row.PendingApproval
	case "disabled":
		return !row.Probe.Enabled && !row.PendingApproval
	case "online":
		return row.Probe.Enabled && row.Online
	case "offline":
		return row.Probe.Enabled && !row.Online
	default:
		return true
	}
}

// countProbeFilters computes each filter's count in one pass so the header can
// show them without re-querying or re-walking per filter.
func countProbeFilters(probes []store.Probe, s *Server, now time.Time) map[string]int {
	counts := map[string]int{"all": len(probes)}
	for _, p := range probes {
		online, _ := s.onlineState(p, now)
		row := probeRow{Probe: p, Online: online, PendingApproval: p.CreatedVia == "public" && !p.Enabled}
		for _, f := range []string{"pending", "disabled", "online", "offline"} {
			if matchesProbeFilter(f, row) {
				counts[f]++
			}
		}
	}
	return counts
}

func (s *Server) handleProbeNewForm(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	s.renderProbeNew(w, sess, probeFormValues{networkType: "wired"}, "", http.StatusOK)
}

// probeFormValues is the submitted create form. Location is expressed as two
// catalog codes; every name and group is resolved from the catalog, so the form
// cannot introduce a code or a name that the catalog does not already have.
type probeFormValues struct {
	campusCode   string
	buildingCode string
	networkType  string
	description  string
}

// view exposes the submitted values to the template, which reads them by form
// field name. The fields are unexported, so a map is the only shape the
// template engine can reach them through.
func (v probeFormValues) view() map[string]string {
	return map[string]string{
		"campus_code":   v.campusCode,
		"building_code": v.buildingCode,
		"network_type":  v.networkType,
		"description":   v.description,
	}
}

func readProbeForm(r *http.Request) probeFormValues {
	return probeFormValues{
		campusCode:   strings.TrimSpace(r.PostFormValue("campus_code")),
		buildingCode: strings.TrimSpace(r.PostFormValue("building_code")),
		networkType:  strings.TrimSpace(r.PostFormValue("network_type")),
		description:  strings.TrimSpace(r.PostFormValue("description")),
	}
}

// resolveLocation turns the submitted codes into the full location a probe row
// stores, enforcing that both exist and that the building really belongs to the
// named campus. Without the agreement check a probe could be filed under a
// campus it is not in — and a mismatched pair is what lets DeleteCampus destroy
// a building a probe still points at.
func (s *Server) resolveLocation(campusCode, buildingCode string) (*store.Campus, *store.Building, string) {
	if !validCode(campusCode) {
		return nil, nil, "请选择校区"
	}
	if !validCode(buildingCode) {
		return nil, nil, "请选择楼栋"
	}
	campus, err := s.store.GetCampus(campusCode)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, "校区不存在，请从列表中选择"
	}
	if err != nil {
		return nil, nil, "无法校验校区，请稍后重试"
	}
	building, err := s.store.GetBuilding(buildingCode)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, "楼栋不存在，请从列表中选择"
	}
	if err != nil {
		return nil, nil, "无法校验楼栋，请稍后重试"
	}
	if building.CampusCode != campus.Code {
		return nil, nil, "所选楼栋不属于该校区"
	}
	return campus, building, ""
}

// renderProbeNew renders the create form with its catalog-backed selects.
func (s *Server) renderProbeNew(w http.ResponseWriter, sess *session, form probeFormValues, errMsg string, status int) {
	campuses, err := s.store.ListCampuses()
	if err != nil {
		s.logger.Error("failed to list campuses", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pages := map[string]any{"campuses": campuses, "empty": len(campuses) == 0, "form": form.view()}
	selected := form.campusCode
	if selected == "" && len(campuses) > 0 {
		selected = campuses[0].Code
	}
	if selected != "" {
		buildings, err := s.store.ListBuildingsByCampus(selected)
		if err != nil {
			s.logger.Error("failed to list buildings", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		pages["buildings"] = buildings
		pages["campus"] = selected
	}
	webui.Render(w, status, s.templates, "probe_new.html", webui.PageData{
		Title: "新建 Probe", Username: sess.username, CSRF: sess.csrf, Error: errMsg, Pages: pages,
	})
}

func (s *Server) handleProbeCreate(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	form := readProbeForm(r)

	campus, building, locErr := s.resolveLocation(form.campusCode, form.buildingCode)
	if locErr == "" && len([]rune(form.description)) > 256 {
		locErr = "备注过长"
	}
	if locErr == "" && !networkTypes[form.networkType] {
		locErr = "网络类型必须是 wired 或 wireless"
	}
	if locErr != "" {
		s.renderProbeNew(w, sess, form, locErr, http.StatusBadRequest)
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
		CampusCode:        campus.Code,
		CampusName:        campus.Name,
		BuildingGroupCode: building.BuildingGroupCode,
		BuildingGroupName: building.BuildingGroupName,
		BuildingCode:      building.Code,
		BuildingName:      building.Name,
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
