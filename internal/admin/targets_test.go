package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func targetForm(csrf string) url.Values {
	return url.Values{
		"csrf":         {csrf},
		"target_id":    {"new_target"},
		"display_name": {"新目标"},
		"address":      {"10.0.0.1"},
		"description":  {"测试"},
		"probe_types":  {"icmp", "dns"},
		"enabled":      {"on"},
	}
}

func TestTargetListRendersSeededTargets(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)

	rec := h.get(t, "/admin/targets", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for _, want := range []string{"campus_dns", "aliyun_dns", "cqu_mirror", "223.5.5.5"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("targets page does not show %q", want)
		}
	}
}

func TestTargetCreate(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	page := h.get(t, "/admin/targets", cookie)
	csrf := csrfFrom(t, page.Body.String())

	rec := h.post(t, "/admin/targets/new", targetForm(csrf), cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}

	al, err := h.store.Allowlist()
	if err != nil {
		t.Fatalf("Allowlist() error = %v", err)
	}
	if !al["new_target"]["icmp"] || !al["new_target"]["dns"] {
		t.Errorf("new_target allowlist = %v, want icmp and dns", al["new_target"])
	}
}

func TestTargetCreateRejectsUnknownProbeType(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	page := h.get(t, "/admin/targets", cookie)
	csrf := csrfFrom(t, page.Body.String())

	form := targetForm(csrf)
	form.Set("probe_types", "tcp")
	rec := h.post(t, "/admin/targets/new", form, cookie)
	if rec.Code == http.StatusSeeOther {
		t.Fatal("an unknown probe type was accepted")
	}
}

func TestTargetCreateRejectsNoProbeTypes(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	page := h.get(t, "/admin/targets", cookie)
	csrf := csrfFrom(t, page.Body.String())

	form := targetForm(csrf)
	form.Del("probe_types")
	rec := h.post(t, "/admin/targets/new", form, cookie)
	if rec.Code == http.StatusSeeOther {
		t.Fatal("a target with no probe types was accepted")
	}
}

func TestTargetCreateRejectsBadID(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	page := h.get(t, "/admin/targets", cookie)
	csrf := csrfFrom(t, page.Body.String())

	for _, bad := range []string{"", "NewTarget", "new-target", "新目标", "new target"} {
		form := targetForm(csrf)
		form.Set("target_id", bad)
		if rec := h.post(t, "/admin/targets/new", form, cookie); rec.Code == http.StatusSeeOther {
			t.Fatalf("bad target_id %q was accepted", bad)
		}
	}
}

func TestTargetUpdate(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	page := h.get(t, "/admin/targets", cookie)
	csrf := csrfFrom(t, page.Body.String())

	form := url.Values{
		"csrf":         {csrf},
		"display_name": {"阿里 DNS 修改"},
		"address":      {"223.6.6.6"},
		"description":  {"updated"},
		"probe_types":  {"icmp", "dns"},
		"enabled":      {"on"},
	}
	if rec := h.post(t, "/admin/targets/aliyun_dns/update", form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	targets, err := h.store.ListTargets()
	if err != nil {
		t.Fatalf("ListTargets() error = %v", err)
	}
	for _, tg := range targets {
		if tg.TargetID != "aliyun_dns" {
			continue
		}
		if tg.Address != "223.6.6.6" {
			t.Errorf("Address = %q, want 223.6.6.6", tg.Address)
		}
		if len(tg.ProbeTypes) != 2 {
			t.Errorf("ProbeTypes = %v, want 2", tg.ProbeTypes)
		}
	}
}

func TestTargetDisabledIsRemovedFromAllowlist(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	page := h.get(t, "/admin/targets", cookie)
	csrf := csrfFrom(t, page.Body.String())

	// Omit the "enabled" checkbox field to disable.
	form := url.Values{
		"csrf":         {csrf},
		"display_name": {"阿里 DNS"},
		"address":      {"223.5.5.5"},
		"probe_types":  {"icmp"},
	}
	if rec := h.post(t, "/admin/targets/aliyun_dns/update", form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	al, err := h.store.Allowlist()
	if err != nil {
		t.Fatalf("Allowlist() error = %v", err)
	}
	if _, ok := al["aliyun_dns"]; ok {
		t.Error("a disabled target is still in the allowlist")
	}
}

func TestTargetDelete(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	page := h.get(t, "/admin/targets", cookie)
	csrf := csrfFrom(t, page.Body.String())

	rec := h.post(t, "/admin/targets/cloudflare_dns/delete", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	al, err := h.store.Allowlist()
	if err != nil {
		t.Fatalf("Allowlist() error = %v", err)
	}
	if _, ok := al["cloudflare_dns"]; ok {
		t.Error("deleted target is still in the allowlist")
	}
}

func TestTargetRoutesRequireAuth(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	for _, path := range []string{"/admin/targets"} {
		rec := h.get(t, path, nil)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("GET %s status = %d, want redirect", path, rec.Code)
		}
	}
}
