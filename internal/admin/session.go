// Package admin implements the web management interface.
package admin

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// sessionTTL is the sliding idle window for an admin session.
const sessionTTL = 12 * time.Hour

// oneShotTTL bounds how long a freshly created token stays displayable.
const oneShotTTL = 60 * time.Second

// bcryptCost is the standard work factor for the admin password. This is a
// deliberate contrast with probe tokens, which use SHA-256: the admin password
// may be human-chosen and the login endpoint is reachable, so it needs a slow
// KDF; probe tokens are 256-bit random and need an indexable fast hash instead.
const bcryptCost = bcrypt.DefaultCost

// maxSessions bounds memory if an attacker loops the login endpoint.
const maxSessions = 1000

type session struct {
	id       string
	username string
	csrf     string
	expires  time.Time
}

// sessionStore keeps admin sessions in memory. A restart invalidates every
// session, which is the desired behaviour for a single-instance gateway.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*session
	ttl      time.Duration
	now      func() time.Time
}

func newSessionStore(ttl time.Duration, now func() time.Time) *sessionStore {
	return &sessionStore{
		sessions: make(map[string]*session),
		ttl:      ttl,
		now:      now,
	}
}

// create starts a new session with a fresh random ID and CSRF token.
func (s *sessionStore) create(username string) (*session, error) {
	id, err := randomString(32)
	if err != nil {
		return nil, err
	}
	csrf, err := randomString(32)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.evictLocked()
	if len(s.sessions) >= maxSessions {
		// Refuse rather than grow without bound.
		return nil, fmt.Errorf("admin: session limit reached")
	}

	sess := &session{
		id:       id,
		username: username,
		csrf:     csrf,
		expires:  s.now().Add(s.ttl),
	}
	s.sessions[id] = sess
	return sess, nil
}

// get returns a live session, sliding its expiry forward. A session that has
// expired is removed and reported as absent.
func (s *sessionStore) get(id string) (*session, bool) {
	if id == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	now := s.now()
	if now.After(sess.expires) {
		delete(s.sessions, id)
		return nil, false
	}
	sess.expires = now.Add(s.ttl)
	return sess, true
}

// destroy ends a session.
func (s *sessionStore) destroy(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func (s *sessionStore) evictLocked() {
	now := s.now()
	for id, sess := range s.sessions {
		if now.After(sess.expires) {
			delete(s.sessions, id)
		}
	}
}

// oneShotStore hands a plaintext token to exactly one page render. This is what
// makes "the token is shown once" true rather than aspirational: the slot is
// deleted on first read, so a refresh, a back-button, or a replay finds nothing.
type oneShotStore struct {
	mu    sync.Mutex
	slots map[string]oneShot
	ttl   time.Duration
	now   func() time.Time
}

// oneShot is one pending render: the secret to display and the probe it belongs
// to. The probe ID travels with the secret because the token page has to name
// the probe it is handing a credential to, and a query parameter would let a
// crafted link put an arbitrary ID beside a real token.
type oneShot struct {
	probeID string
	value   string
	expires time.Time
}

func newOneShotStore(ttl time.Duration, now func() time.Time) *oneShotStore {
	return &oneShotStore{slots: make(map[string]oneShot), ttl: ttl, now: now}
}

// put stores a value and returns the slot key used to redeem it.
func (o *oneShotStore) put(value string) (string, error) {
	return o.putPair("", value)
}

// putPair stores a token together with its probe ID.
func (o *oneShotStore) putPair(probeID, value string) (string, error) {
	slot, err := randomString(32)
	if err != nil {
		return "", err
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	now := o.now()
	for k, v := range o.slots {
		if now.After(v.expires) {
			delete(o.slots, k)
		}
	}
	o.slots[slot] = oneShot{probeID: probeID, value: value, expires: now.Add(o.ttl)}
	return slot, nil
}

// take redeems a slot, deleting it in the same critical section so two
// concurrent requests cannot both receive the token.
func (o *oneShotStore) take(slot string) (string, bool) {
	_, value, ok := o.takePair(slot)
	return value, ok
}

// takePair redeems both halves of a slot. The delete happens in the same
// critical section as the read, so exactly one caller can win.
func (o *oneShotStore) takePair(slot string) (string, string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()

	v, ok := o.slots[slot]
	if !ok {
		return "", "", false
	}
	delete(o.slots, slot)
	if o.now().After(v.expires) {
		return "", "", false
	}
	return v.probeID, v.value, true
}

// randomString returns n cryptographically random bytes, base64url encoded.
func randomString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("admin: read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashPassword derives a bcrypt hash of the admin password.
func hashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("admin: password must not be empty")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("admin: hash password: %w", err)
	}
	return string(h), nil
}

// verifyPassword compares a password against a bcrypt hash in constant time.
func verifyPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// generateAdminPassword returns a 32-byte random password and its hash, used
// when ADMIN_PASSWORD is unset.
func generateAdminPassword() (password, hash string, err error) {
	password, err = randomString(32)
	if err != nil {
		return "", "", err
	}
	hash, err = hashPassword(password)
	if err != nil {
		return "", "", err
	}
	return password, hash, nil
}
