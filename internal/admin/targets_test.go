package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tano/cqu-netprobe-gateway/internal/store"
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

// TestTargetValidationErrorsAreRendered pins the error paragraph in
// targets.html: a rejection that renders no message leaves the operator
// staring at an unchanged table with no idea what was wrong.
func TestTargetValidationErrorsAreRendered(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	page := h.get(t, "/admin/targets", cookie)
	csrf := csrfFrom(t, page.Body.String())

	cases := []struct {
		name string
		form func() url.Values
		want string
	}{
		{
			name: "no probe types",
			form: func() url.Values {
				f := targetForm(csrf)
				f.Del("probe_types")
				return f
			},
			want: "至少选择一种 Probe Type",
		},
		{
			name: "invalid target id",
			form: func() url.Values {
				f := targetForm(csrf)
				f.Set("target_id", "NewTarget")
				return f
			},
			want: "Target ID 只能使用 1-32 位小写字母、数字或下划线",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.post(t, "/admin/targets/new", tc.form(), cookie)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("400 page does not show %q", tc.want)
			}
		})
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

// TestTargetUpdateIgnoresCraftedTargetID exercises the attack the path-ID rule
// defends against: a form body naming a different target, posted to aliyun_dns's
// update route, must not rename aliyun_dns nor overwrite cloudflare_dns.
func TestTargetUpdateIgnoresCraftedTargetID(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	page := h.get(t, "/admin/targets", cookie)
	csrf := csrfFrom(t, page.Body.String())

	form := targetForm(csrf)
	form.Set("target_id", "cloudflare_dns")
	form.Set("display_name", "劫持尝试")
	form.Set("address", "223.6.6.6")
	if rec := h.post(t, "/admin/targets/aliyun_dns/update", form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body = %s", rec.Code, rec.Body.String())
	}

	targets, err := h.store.ListTargets()
	if err != nil {
		t.Fatalf("ListTargets() error = %v", err)
	}
	byID := map[string]store.Target{}
	for _, tg := range targets {
		byID[tg.TargetID] = tg
	}

	// The path target was updated, so the request did land somewhere.
	aliyun, ok := byID["aliyun_dns"]
	if !ok {
		t.Fatal("aliyun_dns is gone: a crafted target_id renamed it")
	}
	if aliyun.Address != "223.6.6.6" {
		t.Errorf("aliyun_dns address = %q, want the posted 223.6.6.6", aliyun.Address)
	}
	if aliyun.DisplayName != "劫持尝试" {
		t.Errorf("aliyun_dns display name = %q, want the posted 劫持尝试", aliyun.DisplayName)
	}

	// The target named in the body is untouched.
	cf, ok := byID["cloudflare_dns"]
	if !ok {
		t.Fatal("cloudflare_dns is gone: a crafted target_id overwrote or renamed it")
	}
	if cf.Address != "1.1.1.1" {
		t.Errorf("cloudflare_dns address = %q, want its seeded 1.1.1.1", cf.Address)
	}
	if cf.DisplayName != "Cloudflare DNS" {
		t.Errorf("cloudflare_dns display name = %q, want its seeded Cloudflare DNS", cf.DisplayName)
	}
	if len(cf.ProbeTypes) != 1 || cf.ProbeTypes[0] != "icmp" {
		t.Errorf("cloudflare_dns probe types = %v, want [icmp]", cf.ProbeTypes)
	}

	// No renamed target appeared: exactly the seeded IDs remain.
	wantIDs := []string{"aliyun_dns", "campus_dns", "cloudflare_dns", "cqu_mirror", "dnspod_dns"}
	if len(targets) != len(wantIDs) {
		t.Fatalf("target count = %d, want %d: a rename added or removed a row", len(targets), len(wantIDs))
	}
	for _, id := range wantIDs {
		if _, ok := byID[id]; !ok {
			t.Errorf("target %q is missing after the crafted update", id)
		}
	}
}

func TestTargetUpdateUnknownTargetIsNotFound(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	page := h.get(t, "/admin/targets", cookie)
	csrf := csrfFrom(t, page.Body.String())

	rec := h.post(t, "/admin/targets/nonexistent/update", targetForm(csrf), cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}

func TestTargetDeleteUnknownTargetIsNotFound(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")
	cookie := h.login(t)
	page := h.get(t, "/admin/targets", cookie)
	csrf := csrfFrom(t, page.Body.String())

	rec := h.post(t, "/admin/targets/nonexistent/delete", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
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

	// The checkbox expresses both directions, so re-enabling through the same
	// form must put the target back.
	form.Set("enabled", "on")
	if rec := h.post(t, "/admin/targets/aliyun_dns/update", form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("re-enable status = %d, want 303", rec.Code)
	}
	al, err = h.store.Allowlist()
	if err != nil {
		t.Fatalf("Allowlist() after re-enable error = %v", err)
	}
	types, ok := al["aliyun_dns"]
	if !ok {
		t.Fatal("a re-enabled target did not return to the allowlist")
	}
	if !types["icmp"] {
		t.Errorf("re-enabled allowlist = %v, want icmp", types)
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

// TestTargetRoutesRequireAuth covers every target route, GET and POST alike.
// The POST routes are the ones that matter: they are the state-changing ones.
func TestTargetRoutesRequireAuth(t *testing.T) {
	h := newAdminHarness(t, "test-password-value")

	for _, path := range []string{"/admin/targets"} {
		t.Run("GET "+path, func(t *testing.T) {
			rec := h.get(t, path, nil)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want a redirect to login", rec.Code)
			}
			if loc := rec.Header().Get("Location"); loc != "/admin/login" {
				t.Errorf("Location = %q, want /admin/login", loc)
			}
		})
	}

	for _, path := range []string{"/admin/targets/new", "/admin/targets/x/update", "/admin/targets/x/delete"} {
		t.Run("POST "+path, func(t *testing.T) {
			rec := h.post(t, path, url.Values{}, nil)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want a redirect to login", rec.Code)
			}
			if loc := rec.Header().Get("Location"); loc != "/admin/login" {
				t.Errorf("Location = %q, want /admin/login", loc)
			}
		})
	}

	// A live session is not enough: the POST routes also require the token.
	cookie := h.login(t)
	rec := h.post(t, "/admin/targets/new", url.Values{}, cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d for a CSRF-less POST, want 403", rec.Code)
	}
}
