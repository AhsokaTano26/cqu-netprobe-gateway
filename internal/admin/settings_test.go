package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
)

// defaultSettingsForm is the shipped set, as the form submits it.
func defaultSettingsForm(csrf string) url.Values {
	c := protocol.DefaultMeasurementConfig()
	return url.Values{
		"csrf":                  {csrf},
		"interval_ms":           {itoa(c.IntervalMS)},
		"icmp_count":            {itoa(c.ICMP.Count)},
		"icmp_interval_ms":      {itoa(c.ICMP.IntervalMS)},
		"icmp_timeout_ms":       {itoa(c.ICMP.TimeoutMS)},
		"http_follow_redirects": {"on"},
		"http_verify_tls":       {"on"},
		"http_timeout_ms":       {itoa(c.HTTP.TimeoutMS)},
		"dns_timeout_ms":        {itoa(c.DNS.TimeoutMS)},
	}
}

func TestMeasurementSettingsRequireAuthAndCSRF(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")

	if rec := h.get(t, "/admin/settings", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous GET = %d, want a redirect to the login page", rec.Code)
	}

	cookie := h.login(t)
	form := defaultSettingsForm("")
	form.Del("csrf")
	if rec := h.post(t, "/admin/settings", form, cookie); rec.Code != http.StatusForbidden {
		t.Errorf("POST without csrf = %d, want 403", rec.Code)
	}
}

func TestMeasurementSettingsPageShowsDefaults(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)

	body := h.get(t, "/admin/settings", cookie).Body.String()

	// Every shipped value is on the form, so an administrator can see what is
	// in force without reading the protocol document.
	defaults := protocol.DefaultMeasurementConfig()
	for _, want := range []string{
		itoa(defaults.IntervalMS),
		itoa(defaults.ICMP.Count),
		itoa(defaults.ICMP.IntervalMS),
		itoa(defaults.ICMP.TimeoutMS),
		itoa(defaults.HTTP.TimeoutMS),
		itoa(defaults.DNS.TimeoutMS),
		// The two v1-fixed values are stated rather than offered as fields.
		"固定为 <code>GET</code>",
		"固定为 <code>UDP</code>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the settings page does not show %q", want)
		}
	}
	// The exact notice, not a substring: the reset card below it explains what
	// "自定义值" means, so a looser check would pass either way.
	if !strings.Contains(body, "当前使用的是<strong>协议默认值</strong>") {
		t.Error("a fresh deployment is not labelled as using the defaults")
	}
}

func TestMeasurementSettingsSaveRoundTrip(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)

	page := h.get(t, "/admin/settings", cookie)
	form := defaultSettingsForm(csrfFrom(t, page.Body.String()))
	form.Set("interval_ms", "30000")
	form.Set("icmp_count", "10")
	form.Del("http_verify_tls") // an unchecked box submits nothing

	if rec := h.post(t, "/admin/settings", form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("save status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}

	got, custom, err := h.store.MeasurementConfig()
	if err != nil {
		t.Fatalf("MeasurementConfig() error = %v", err)
	}
	if !custom {
		t.Fatal("the saved config is not in force")
	}
	if got.IntervalMS != 30000 || got.ICMP.Count != 10 {
		t.Errorf("stored %+v, want interval 30000 and icmp count 10", got)
	}
	if got.HTTP.VerifyTLS {
		t.Error("an unchecked box was stored as true")
	}
	// The protocol-fixed fields must survive a round trip through the form.
	if got.HTTP.Method != protocol.HTTPMethod || got.DNS.Transport != protocol.DNSTransport {
		t.Errorf("the form lost the fixed method/transport: %+v", got)
	}

	// The page must now say so.
	after := h.get(t, "/admin/settings", cookie).Body.String()
	if !strings.Contains(after, "当前使用的是<strong>自定义值</strong>") {
		t.Error("the page still reports the defaults after a save")
	}
}

// A rejected save must leave the stored set alone and give the form back with
// what was typed, or one bad digit costs the administrator every other field.
func TestMeasurementSettingsRejectsBadValues(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)

	cases := map[string]struct {
		mutate func(url.Values)
		want   string
	}{
		"not a number": {
			mutate: func(f url.Values) { f.Set("icmp_count", "five") },
			want:   "ICMP 每轮次数必须是整数",
		},
		"zero timeout": {
			mutate: func(f url.Values) { f.Set("dns_timeout_ms", "0") },
			want:   "DNS 超时必须大于 0",
		},
		"icmp round outruns the cycle": {
			mutate: func(f url.Values) { f.Set("icmp_count", "60") },
			want:   "必须小于测量周期",
		},
		"http timeout outruns the cycle": {
			mutate: func(f url.Values) { f.Set("http_timeout_ms", "20000") },
			want:   "必须小于测量周期",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			page := h.get(t, "/admin/settings", cookie)
			form := defaultSettingsForm(csrfFrom(t, page.Body.String()))
			tc.mutate(form)

			rec := h.post(t, "/admin/settings", form, cookie)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.want) {
				t.Errorf("the error does not mention %q", tc.want)
			}
			if _, custom, err := h.store.MeasurementConfig(); err != nil || custom {
				t.Errorf("a rejected save was stored (custom=%v, err=%v)", custom, err)
			}
		})
	}
}

// The one rule the protocol layer cannot state, because it does not know the
// deployment: a cycle shorter than the push limiter's refill interval means
// every probe is throttled once its burst drains — for as long as the setting
// stands, and it looks like a broken fleet rather than a misconfiguration.
//
// 10s is perfectly valid per the protocol; it is this deployment's 20s
// RATE_LIMIT that rejects it.
func TestMeasurementSettingsRejectsCycleBelowTheRateLimit(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	h.server.cfg.RateLimit = 20 * time.Second
	cookie := h.login(t)

	page := h.get(t, "/admin/settings", cookie)
	form := defaultSettingsForm(csrfFrom(t, page.Body.String()))
	form.Set("interval_ms", "10000")

	rec := h.post(t, "/admin/settings", form, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "RATE_LIMIT") {
		t.Error("the error does not explain that the push rate limit is what rejected it")
	}
	if _, custom, err := h.store.MeasurementConfig(); err != nil || custom {
		t.Errorf("a rejected save was stored (custom=%v, err=%v)", custom, err)
	}
}

// The form has no method or transport field, so a crafted POST must not be able
// to introduce one: v1 fixes both, and a probe measuring something else would
// produce numbers that cannot be compared with the rest of the fleet.
func TestMeasurementSettingsCannotChangeProtocolFixedFields(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)

	page := h.get(t, "/admin/settings", cookie)
	form := defaultSettingsForm(csrfFrom(t, page.Body.String()))
	form.Set("http_method", "POST")
	form.Set("dns_transport", "tcp")

	if rec := h.post(t, "/admin/settings", form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	got, _, err := h.store.MeasurementConfig()
	if err != nil {
		t.Fatalf("MeasurementConfig() error = %v", err)
	}
	if got.HTTP.Method != protocol.HTTPMethod {
		t.Errorf("HTTP method = %q; the form must not be able to change it", got.HTTP.Method)
	}
	if got.DNS.Transport != protocol.DNSTransport {
		t.Errorf("DNS transport = %q; the form must not be able to change it", got.DNS.Transport)
	}
}

func TestMeasurementSettingsReset(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)

	custom := protocol.DefaultMeasurementConfig()
	custom.IntervalMS = 30000
	if err := h.store.SetMeasurementConfig(custom); err != nil {
		t.Fatalf("SetMeasurementConfig() error = %v", err)
	}

	page := h.get(t, "/admin/settings", cookie)
	rec := h.post(t, "/admin/settings/reset",
		url.Values{"csrf": {csrfFrom(t, page.Body.String())}}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reset status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}

	got, custom2, err := h.store.MeasurementConfig()
	if err != nil {
		t.Fatalf("MeasurementConfig() error = %v", err)
	}
	if custom2 || got != protocol.DefaultMeasurementConfig() {
		t.Errorf("after reset: %+v custom=%v, want the protocol defaults", got, custom2)
	}
}
