package store

import (
	"errors"
	"testing"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
)

func TestSeedInsertsProtocolDefaults(t *testing.T) {
	s := newTestStore(t)
	targets, err := s.ListTargets()
	if err != nil {
		t.Fatalf("ListTargets() error = %v", err)
	}
	if len(targets) != 5 {
		t.Fatalf("seeded %d targets, want 5", len(targets))
	}
	byID := map[string]Target{}
	for _, tg := range targets {
		byID[tg.TargetID] = tg
	}
	for _, want := range []string{"campus_dns", "aliyun_dns", "dnspod_dns", "cloudflare_dns", "cqu_mirror"} {
		if _, ok := byID[want]; !ok {
			t.Errorf("seeded targets missing %q", want)
		}
	}
	if got := byID["aliyun_dns"].Address; got != "223.5.5.5" {
		t.Errorf("aliyun_dns address = %q, want 223.5.5.5", got)
	}
	if got := byID["cqu_mirror"].ProbeTypes; len(got) != 1 || got[0] != "http" {
		t.Errorf("cqu_mirror probe types = %v, want [http]", got)
	}
	if got := byID["campus_dns"].ProbeTypes; len(got) != 2 {
		t.Errorf("campus_dns probe types = %v, want two entries", got)
	}
}

func TestSeedDoesNotResurrectDeletedTargets(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/seed.db"

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := s1.DeleteTarget("cqu_mirror"); err != nil {
		t.Fatalf("DeleteTarget() error = %v", err)
	}
	_ = s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer func() { _ = s2.Close() }()

	targets, err := s2.ListTargets()
	if err != nil {
		t.Fatalf("ListTargets() error = %v", err)
	}
	if len(targets) != 4 {
		t.Fatalf("target count after reopen = %d, want 4 (deleted target must stay deleted)", len(targets))
	}
}

func TestCreateTargetWithProbeTypes(t *testing.T) {
	s := newTestStore(t)
	tg := &Target{
		TargetID:    "new_target",
		DisplayName: "新目标",
		Address:     "10.0.0.1",
		ProbeTypes:  []string{"icmp", "http"},
	}
	if err := s.CreateTarget(tg); err != nil {
		t.Fatalf("CreateTarget() error = %v", err)
	}

	got, err := s.ListTargets()
	if err != nil {
		t.Fatalf("ListTargets() error = %v", err)
	}
	var found *Target
	for i := range got {
		if got[i].TargetID == "new_target" {
			found = &got[i]
		}
	}
	if found == nil {
		t.Fatal("new_target not found")
	}
	if len(found.ProbeTypes) != 2 {
		t.Fatalf("ProbeTypes = %v, want 2 entries", found.ProbeTypes)
	}
}

func TestCreateTargetRejectsDuplicate(t *testing.T) {
	s := newTestStore(t)
	tg := &Target{TargetID: "dup_target", ProbeTypes: []string{"icmp"}}
	if err := s.CreateTarget(tg); err != nil {
		t.Fatalf("first CreateTarget() error = %v", err)
	}
	if err := s.CreateTarget(&Target{TargetID: "dup_target", ProbeTypes: []string{"icmp"}}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate error = %v, want ErrDuplicate", err)
	}
}

func TestUpdateTarget(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateTarget(&Target{TargetID: "upd", Address: "1.1.1.1", ProbeTypes: []string{"icmp"}}); err != nil {
		t.Fatalf("CreateTarget() error = %v", err)
	}
	if err := s.UpdateTarget(&Target{TargetID: "upd", Address: "2.2.2.2", DisplayName: "two", ProbeTypes: []string{"dns", "http"}}); err != nil {
		t.Fatalf("UpdateTarget() error = %v", err)
	}

	targets, err := s.ListTargets()
	if err != nil {
		t.Fatalf("ListTargets() error = %v", err)
	}
	for _, tg := range targets {
		if tg.TargetID != "upd" {
			continue
		}
		if tg.Address != "2.2.2.2" {
			t.Errorf("Address = %q, want 2.2.2.2", tg.Address)
		}
		if len(tg.ProbeTypes) != 2 {
			t.Errorf("ProbeTypes = %v, want 2 entries", tg.ProbeTypes)
		}
	}
}

func TestDeleteTargetCascadesProbeTypes(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateTarget(&Target{TargetID: "gone", ProbeTypes: []string{"icmp", "dns"}}); err != nil {
		t.Fatalf("CreateTarget() error = %v", err)
	}
	if err := s.DeleteTarget("gone"); err != nil {
		t.Fatalf("DeleteTarget() error = %v", err)
	}
	if err := s.DeleteTarget("gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteTarget() error = %v, want ErrNotFound", err)
	}

	al, err := s.Allowlist()
	if err != nil {
		t.Fatalf("Allowlist() error = %v", err)
	}
	if _, ok := al["gone"]; ok {
		t.Error("deleted target still present in allowlist")
	}
}

func TestAllowlistShape(t *testing.T) {
	s := newTestStore(t)
	al, err := s.Allowlist()
	if err != nil {
		t.Fatalf("Allowlist() error = %v", err)
	}

	if !al["campus_dns"][protocol.ProbeICMP] || !al["campus_dns"][protocol.ProbeDNS] {
		t.Errorf("campus_dns allows %v, want icmp and dns", al["campus_dns"])
	}
	if al["campus_dns"][protocol.ProbeHTTP] {
		t.Error("campus_dns must not allow http")
	}
	if !al["cqu_mirror"][protocol.ProbeHTTP] {
		t.Error("cqu_mirror must allow http")
	}
	if al["cqu_mirror"][protocol.ProbeICMP] {
		t.Error("cqu_mirror must not allow icmp")
	}
}

func TestDisabledTargetExcludedFromAllowlist(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateTarget(&Target{TargetID: "off", ProbeTypes: []string{"icmp"}}); err != nil {
		t.Fatalf("CreateTarget() error = %v", err)
	}
	if err := s.UpdateTarget(&Target{TargetID: "off", ProbeTypes: []string{"icmp"}, Enabled: false}); err != nil {
		t.Fatalf("UpdateTarget() error = %v", err)
	}
	al, err := s.Allowlist()
	if err != nil {
		t.Fatalf("Allowlist() error = %v", err)
	}
	if _, ok := al["off"]; ok {
		t.Error("disabled target must not appear in the allowlist")
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s := newTestStore(t)

	if _, ok, err := s.Setting("missing"); err != nil || ok {
		t.Fatalf("Setting(missing) = ok:%v err:%v, want ok:false err:nil", ok, err)
	}
	if err := s.SetSetting("admin_password_hash", "abc"); err != nil {
		t.Fatalf("SetSetting() error = %v", err)
	}
	got, ok, err := s.Setting("admin_password_hash")
	if err != nil || !ok {
		t.Fatalf("Setting() = ok:%v err:%v, want ok:true err:nil", ok, err)
	}
	if got != "abc" {
		t.Errorf("Setting() = %q, want abc", got)
	}

	// Upsert must overwrite.
	if err := s.SetSetting("admin_password_hash", "def"); err != nil {
		t.Fatalf("SetSetting() overwrite error = %v", err)
	}
	got, _, _ = s.Setting("admin_password_hash")
	if got != "def" {
		t.Errorf("Setting() after overwrite = %q, want def", got)
	}

	if err := s.DeleteSetting("admin_password_hash"); err != nil {
		t.Fatalf("DeleteSetting() error = %v", err)
	}
	if _, ok, _ := s.Setting("admin_password_hash"); ok {
		t.Error("setting still present after delete")
	}
}
