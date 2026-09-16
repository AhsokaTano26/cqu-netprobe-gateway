package portal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/tano/cqu-netprobe-gateway/internal/clientip"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
	"github.com/tano/cqu-netprobe-gateway/internal/token"
	"github.com/tano/cqu-netprobe-gateway/internal/webui"
)

// codePattern mirrors internal/admin's: every code here reaches a Prometheus
// label and a probe_id prefix.
var codePattern = regexp.MustCompile(`^[a-z0-9_]{1,16}$`)

// networkTypes is the closed enum for network_type.
var networkTypes = map[string]bool{"wired": true, "wireless": true}

// probeIDRandomBytes yields a 6-character hex suffix.
const probeIDRandomBytes = 3

func (s *Server) handleForm(w http.ResponseWriter, r *http.Request) {
	campus := strings.TrimSpace(r.URL.Query().Get("campus"))
	s.renderForm(w, http.StatusOK, "", campus)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	campusCode := strings.TrimSpace(r.PostFormValue("campus_code"))
	buildingCode := strings.TrimSpace(r.PostFormValue("building_code"))
	networkType := strings.TrimSpace(r.PostFormValue("network_type"))
	description := strings.TrimSpace(r.PostFormValue("description"))

	campuses, err := s.store.ListCampuses()
	if err != nil {
		s.logger.Error("failed to list campuses", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if len(campuses) == 0 {
		// Nothing can be registered until an administrator configures the
		// catalog; that is a server-side gap, not a bad request.
		s.renderForm(w, http.StatusServiceUnavailable, "管理员尚未配置任何校区，暂时无法注册。", "")
		return
	}

	form := registrationForm{
		campusCode: campusCode, buildingCode: buildingCode,
		networkType: networkType, description: description,
	}
	if msg := s.validateRegistration(form); msg != "" {
		s.renderForm(w, http.StatusBadRequest, msg, campusCode)
		return
	}

	building, err := s.store.GetBuilding(buildingCode)
	if err != nil {
		s.renderForm(w, http.StatusBadRequest, "楼栋不存在，请从列表中选择。", campusCode)
		return
	}
	campus, err := s.store.GetCampus(building.CampusCode)
	if err != nil {
		s.renderForm(w, http.StatusBadRequest, "楼栋所属校区不存在。", campusCode)
		return
	}

	// The allowance is spent only by a submission that is otherwise going to
	// succeed. A rejected one costs a regex and one indexed lookup, and spending
	// an allowance on it would lock a visitor out for the whole interval over a
	// typo — the form deliberately re-renders for a corrected retry.
	if s.limiter != nil && !s.limiter.Allow(clientip.From(r, s.cfg.TrustedProxyCIDRs)) {
		s.renderForm(w, http.StatusTooManyRequests,
			"注册过于频繁，请稍后再试。", campusCode)
		return
	}

	// The limiter bounds how fast one address can create probes; this bounds how
	// many can exist, which is what actually protects Prometheus cardinality
	// over a semester. Two simultaneous registrations can both pass this check
	// and land a probe or two over the cap — the limiter keeps that from being
	// more than a rounding error, and the alternative is holding a transaction
	// open across token generation.
	//
	// Only this path is capped. An administrator is not: a full table must not
	// stop the person who can empty it.
	count, err := s.store.CountProbes()
	if err != nil {
		s.logger.Error("failed to count probes", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if count >= s.cfg.MaxProbes {
		s.logger.Warn("registration refused: probe cap reached",
			"probes", count, "max_probes", s.cfg.MaxProbes, "remote_ip", clientip.From(r, s.cfg.TrustedProxyCIDRs))
		s.renderForm(w, http.StatusServiceUnavailable, fmt.Sprintf(
			"探针数量已达上限（%d 个），暂时无法自助注册。请联系网络中心。",
			s.cfg.MaxProbes), campusCode)
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
		NetworkType:       networkType,
		// Enabled at once: a self-registered probe starts reporting as soon as
		// its token is configured, with no approval step. What bounds the
		// anonymous path is the probe cap checked above, not a queue nobody
		// works through — a probe parked in "pending" measures nothing and
		// looks identical to a broken one.
		Enabled:     true,
		Description: description,
		CreatedVia:  "public",
	}

	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		id, err := generateProbeID(campus.Code, building.Code)
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

	// A self-registered visitor came from the form, so the token page returns
	// there — ready to register a second probe for the same room.
	slot, err := s.oneShot.putPair(probe.ProbeID, rawToken, "/")
	if err != nil {
		s.logger.Error("failed to store one-shot token", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The token itself is never logged; probe_id and location are public.
	s.logger.Info("probe self-registered", "probe_id", probe.ProbeID,
		"campus", probe.CampusCode, "building", probe.BuildingCode, "remote_ip", clientip.From(r, s.cfg.TrustedProxyCIDRs))

	http.Redirect(w, r, "/token/"+slot, http.StatusSeeOther)
}

func (s *Server) handleTokenShow(w http.ResponseWriter, r *http.Request) {
	slot := r.PathValue("slot")
	rec, ok := s.oneShot.takePair(slot)
	if !ok {
		// The return target is a constant here: the record is gone, so there is
		// nothing left that knows where this visitor came from.
		webui.Render(w, http.StatusGone, s.templates, "token.html", webui.PageData{
			Title: "Token 已失效",
			Error: "该 Token 已显示过或已过期，无法再次查看。如需新凭据，请重新注册或联系管理员轮换。",
			Pages: map[string]any{"Back": "/"},
		})
		return
	}
	webui.Render(w, http.StatusOK, s.templates, "token.html", webui.PageData{
		Title: "探针 Token",
		Pages: map[string]any{
			"ProbeID": rec.probeID,
			"Token":   rec.value,
			// The bare base URL, not the full push path. A probe appends
			// /api/v1/push itself (Protocol v1 §2), so showing it the whole URL
			// would have it post to /api/v1/push/api/v1/push.
			//
			// The trailing slash is still trimmed: PUBLIC_BASE_URL is commonly
			// written with one, and it would otherwise end up in the value the
			// visitor copies and pastes.
			"PushEndpoint": strings.TrimSuffix(s.cfg.PublicBaseURL, "/"),
			"Back":         rec.back,
		},
	})
}

// registrationForm is the submitted form, retained so a validation failure can
// re-render without retyping.
type registrationForm struct {
	campusCode, buildingCode, networkType, description string
}

// validateRegistration checks the fields that do not need a database lookup.
// Location existence and campus/building agreement are checked by the caller,
// which has already loaded the building.
func (s *Server) validateRegistration(f registrationForm) string {
	if !codePattern.MatchString(f.campusCode) {
		return "请选择校区。"
	}
	if !codePattern.MatchString(f.buildingCode) {
		return "请选择楼栋。"
	}
	if !networkTypes[f.networkType] {
		return "网络类型必须是 wired 或 wireless。"
	}
	if len([]rune(f.description)) > 256 {
		return "备注过长。"
	}
	b, err := s.store.GetBuilding(f.buildingCode)
	if errors.Is(err, store.ErrNotFound) {
		return "楼栋不存在，请从列表中选择。"
	}
	if err != nil {
		return "无法校验楼栋，请稍后重试。"
	}
	// The pair must agree: without this a caller could file a building under a
	// campus it does not belong to, and the label pair would be nonsense.
	if b.CampusCode != f.campusCode {
		return "所选楼栋不属于该校区。"
	}
	return ""
}

// renderForm renders the registration page, carrying an optional error and
// keeping the chosen campus selected.
func (s *Server) renderForm(w http.ResponseWriter, status int, errMsg, campus string) {
	campuses, err := s.store.ListCampuses()
	if err != nil {
		s.logger.Error("failed to list campuses", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pages := map[string]any{"campuses": campuses, "campus": campus, "empty": len(campuses) == 0}
	if campus != "" {
		buildings, err := s.store.ListBuildingsByCampus(campus)
		if err != nil {
			s.logger.Error("failed to list buildings", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		pages["buildings"] = buildings
	}
	webui.Render(w, status, s.templates, "register.html", webui.PageData{
		Title: "注册探针", Error: errMsg, Pages: pages,
	})
}

// generateProbeID builds "{campus}-{building}-{6 hex}", matching the admin
// flow so a self-registered probe is indistinguishable in form.
func generateProbeID(campusCode, buildingCode string) (string, error) {
	buf := make([]byte, probeIDRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return campusCode + "-" + buildingCode + "-" + hex.EncodeToString(buf), nil
}
