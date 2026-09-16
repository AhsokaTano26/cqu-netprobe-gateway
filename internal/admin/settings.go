package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
	"github.com/tano/cqu-netprobe-gateway/internal/webui"
)

// measurementForm is the settings form as typed, kept as strings rather than
// numbers so a rejected submission is re-rendered with every field the way the
// administrator left it — a bad digit in one box should not cost them the other
// seven.
type measurementForm struct {
	IntervalMS      string
	ICMPCount       string
	ICMPIntervalMS  string
	ICMPTimeoutMS   string
	HTTPTimeoutMS   string
	DNSTimeoutMS    string
	FollowRedirects bool
	VerifyTLS       bool
}

func formFromConfig(c protocol.MeasurementConfig) measurementForm {
	return measurementForm{
		IntervalMS:      strconv.Itoa(c.IntervalMS),
		ICMPCount:       strconv.Itoa(c.ICMP.Count),
		ICMPIntervalMS:  strconv.Itoa(c.ICMP.IntervalMS),
		ICMPTimeoutMS:   strconv.Itoa(c.ICMP.TimeoutMS),
		HTTPTimeoutMS:   strconv.Itoa(c.HTTP.TimeoutMS),
		DNSTimeoutMS:    strconv.Itoa(c.DNS.TimeoutMS),
		FollowRedirects: c.HTTP.FollowRedirects,
		VerifyTLS:       c.HTTP.VerifyTLS,
	}
}

func formFromRequest(r *http.Request) measurementForm {
	return measurementForm{
		IntervalMS:     strings.TrimSpace(r.PostFormValue("interval_ms")),
		ICMPCount:      strings.TrimSpace(r.PostFormValue("icmp_count")),
		ICMPIntervalMS: strings.TrimSpace(r.PostFormValue("icmp_interval_ms")),
		ICMPTimeoutMS:  strings.TrimSpace(r.PostFormValue("icmp_timeout_ms")),
		HTTPTimeoutMS:  strings.TrimSpace(r.PostFormValue("http_timeout_ms")),
		DNSTimeoutMS:   strings.TrimSpace(r.PostFormValue("dns_timeout_ms")),
		// An unchecked box submits nothing at all, which is the only signal
		// HTML gives for "off".
		FollowRedirects: r.PostFormValue("http_follow_redirects") == "on",
		VerifyTLS:       r.PostFormValue("http_verify_tls") == "on",
	}
}

func (s *Server) handleMeasurementSettings(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	cfg, custom, err := s.store.MeasurementConfig()
	if err != nil {
		s.logger.Error("failed to read measurement config", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderMeasurementSettings(w, sess, http.StatusOK, "", formFromConfig(cfg), custom)
}

func (s *Server) handleMeasurementSettingsSave(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	form := formFromRequest(r)

	cfg, msg := s.configFromForm(form)
	if msg == "" {
		if err := s.store.SetMeasurementConfig(cfg); err != nil {
			s.logger.Error("failed to save measurement config", "error", err)
			msg = "保存失败：" + err.Error()
		}
	}
	if msg != "" {
		s.renderMeasurementSettings(w, sess, http.StatusBadRequest, msg, form,
			s.storedConfigIsCustom())
		return
	}

	s.logger.Info("measurement config updated", "admin", sess.username,
		"interval_ms", cfg.IntervalMS, "icmp_count", cfg.ICMP.Count)
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

func (s *Server) handleMeasurementSettingsReset(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromRequest(r)
	if err := s.store.ResetMeasurementConfig(); err != nil {
		s.logger.Error("failed to reset measurement config", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Info("measurement config reset to protocol defaults", "admin", sess.username)
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// configFromForm builds a config from the submitted fields, returning a message
// for the administrator when it cannot.
func (s *Server) configFromForm(f measurementForm) (protocol.MeasurementConfig, string) {
	var values []int
	for _, field := range []struct{ label, raw string }{
		{"测量周期", f.IntervalMS},
		{"ICMP 每轮次数", f.ICMPCount},
		{"ICMP 请求间隔", f.ICMPIntervalMS},
		{"ICMP 单次超时", f.ICMPTimeoutMS},
		{"HTTP 整体超时", f.HTTPTimeoutMS},
		{"DNS 整体超时", f.DNSTimeoutMS},
	} {
		v, err := strconv.Atoi(field.raw)
		if err != nil {
			return protocol.MeasurementConfig{}, field.label + "必须是整数"
		}
		values = append(values, v)
	}

	cfg := protocol.MeasurementConfig{
		IntervalMS: values[0],
		ICMP: protocol.ICMPConfig{
			Count:      values[1],
			IntervalMS: values[2],
			TimeoutMS:  values[3],
		},
		HTTP: protocol.HTTPConfig{
			// Fixed by Protocol v1. The form shows them read-only, so they come
			// from the constants rather than from the request: a crafted POST
			// cannot smuggle in a POST-method probe.
			Method:          protocol.HTTPMethod,
			FollowRedirects: f.FollowRedirects,
			VerifyTLS:       f.VerifyTLS,
			TimeoutMS:       values[4],
		},
		DNS: protocol.DNSConfig{
			Transport: protocol.DNSTransport,
			TimeoutMS: values[5],
		},
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err.Error()
	}

	// The one rule the protocol layer cannot state, because it does not know
	// this deployment's push rate limit: a cycle shorter than the limiter's
	// refill interval means a probe's sustained push rate exceeds its bucket, so
	// once the burst drains, every push is throttled for as long as the setting
	// stands. The symptom is a fleet that looks half-broken.
	if limit := s.cfg.RateLimit.Milliseconds(); int64(cfg.IntervalMS) < limit {
		return cfg, fmt.Sprintf(
			"测量周期必须不小于 %d 毫秒：这是本机推送限流 RATE_LIMIT 的间隔，比它更短的周期会让探针持续被限流",
			limit)
	}
	return cfg, ""
}

// storedConfigIsCustom reports whether an administrator's own set is in force.
// A failure here only costs a heading on the form, so it is logged and treated
// as "not custom" rather than failing the request.
func (s *Server) storedConfigIsCustom() bool {
	_, custom, err := s.store.MeasurementConfig()
	if err != nil {
		s.logger.Error("failed to read measurement config", "error", err)
		return false
	}
	return custom
}

func (s *Server) renderMeasurementSettings(w http.ResponseWriter, sess *session,
	status int, errMsg string, form measurementForm, custom bool) {
	webui.Render(w, status, s.templates, "settings.html", webui.PageData{
		Title: "测量参数", Username: sess.username, CSRF: sess.csrf, Error: errMsg,
		Pages: map[string]any{"form": form, "custom": custom},
	})
}
