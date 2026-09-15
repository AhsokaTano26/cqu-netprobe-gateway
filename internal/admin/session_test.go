package admin

import (
	"strings"
	"testing"
	"time"
)

func TestSessionCreateAndGet(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	s := newSessionStore(12*time.Hour, func() time.Time { return now })

	sess, err := s.create("admin")
	if err != nil {
		t.Fatalf("create() error = %v", err)
	}
	if sess.username != "admin" {
		t.Errorf("username = %q", sess.username)
	}
	if len(sess.id) < 32 {
		t.Errorf("session id is only %d chars; must be a high-entropy random value", len(sess.id))
	}
	if len(sess.csrf) < 32 {
		t.Errorf("csrf token is only %d chars", len(sess.csrf))
	}

	got, ok := s.get(sess.id)
	if !ok {
		t.Fatal("get() ok = false")
	}
	if got.username != "admin" {
		t.Errorf("get() username = %q", got.username)
	}
}

func TestSessionIDsAreUnique(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	s := newSessionStore(12*time.Hour, func() time.Time { return now })

	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		sess, err := s.create("admin")
		if err != nil {
			t.Fatalf("create() error = %v", err)
		}
		if seen[sess.id] {
			t.Fatal("duplicate session id generated")
		}
		seen[sess.id] = true
	}
}

func TestSessionExpires(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	current := now
	s := newSessionStore(12*time.Hour, func() time.Time { return current })

	sess, _ := s.create("admin")
	current = now.Add(11 * time.Hour)
	if _, ok := s.get(sess.id); !ok {
		t.Fatal("session expired before its TTL")
	}

	// The access above slid the idle window forward, so the session now dies
	// one full TTL after that access rather than one TTL after creation.
	current = now.Add(11*time.Hour + 12*time.Hour + time.Second)
	if _, ok := s.get(sess.id); ok {
		t.Fatal("session survived a full idle TTL")
	}
}

func TestSessionSlidesOnAccess(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	current := now
	s := newSessionStore(12*time.Hour, func() time.Time { return current })

	sess, _ := s.create("admin")

	// Touch every 11 hours; the session must stay alive indefinitely.
	for i := 0; i < 5; i++ {
		current = current.Add(11 * time.Hour)
		if _, ok := s.get(sess.id); !ok {
			t.Fatalf("session died on access %d despite sliding expiry", i+1)
		}
	}
}

func TestSessionDestroy(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	s := newSessionStore(12*time.Hour, func() time.Time { return now })

	sess, _ := s.create("admin")
	s.destroy(sess.id)
	if _, ok := s.get(sess.id); ok {
		t.Fatal("session survived destroy")
	}
	// Destroying twice must not panic.
	s.destroy(sess.id)
}

func TestOneShotTokenTakeOnce(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	o := newOneShotStore(60*time.Second, func() time.Time { return now })

	slot, err := o.put("cqu_probe_secret")
	if err != nil {
		t.Fatalf("put() error = %v", err)
	}
	if len(slot) < 32 {
		t.Errorf("slot id is only %d chars", len(slot))
	}

	got, ok := o.take(slot)
	if !ok || got != "cqu_probe_secret" {
		t.Fatalf("take() = %q, %v; want the token, true", got, ok)
	}
	// A second take must fail: this is what makes the token show-once.
	if _, ok := o.take(slot); ok {
		t.Fatal("take() succeeded twice; the token must be shown only once")
	}
}

func TestOneShotTokenExpires(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	current := now
	o := newOneShotStore(60*time.Second, func() time.Time { return current })

	slot, _ := o.put("cqu_probe_secret")
	current = now.Add(61 * time.Second)
	if _, ok := o.take(slot); ok {
		t.Fatal("an expired slot was still redeemable")
	}
}

func TestPasswordGeneration(t *testing.T) {
	pw, hash, err := generateAdminPassword()
	if err != nil {
		t.Fatalf("generateAdminPassword() error = %v", err)
	}
	if len(pw) < 32 {
		t.Errorf("generated password is only %d chars", len(pw))
	}
	if strings.ContainsAny(pw, " \t\n") {
		t.Error("generated password contains whitespace")
	}
	if err := verifyPassword(hash, pw); err != nil {
		t.Fatalf("verifyPassword() error = %v", err)
	}
	if err := verifyPassword(hash, "wrong"); err == nil {
		t.Fatal("verifyPassword() accepted the wrong password")
	}
}

func TestHashPasswordRejectsEmpty(t *testing.T) {
	if _, err := hashPassword(""); err == nil {
		t.Fatal("hashPassword(\"\") should fail")
	}
}
