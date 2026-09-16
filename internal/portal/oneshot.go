// Package portal serves the public, unauthenticated registration pages.
package portal

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"regexp"
	"sync"
	"time"
)

// oneShotStore hands a plaintext token to exactly one page render, and is what
// makes "the token is shown exactly once" true rather than aspirational: take
// deletes the slot inside the same critical section as the read, so a refresh,
// a back-button, or a replay finds nothing.
//
// This duplicates internal/admin's store deliberately. The portal must not
// import internal/admin (keeping the authentication boundary a package boundary
// is the point of internal/webui), and the type is small enough that a shared
// abstraction would cost more than it saves.
//
// The slot id is 32 crypto/rand bytes, which is what makes the token page safe
// to serve without a session: it is a capability URL, unguessable and
// single-use, with a 60 second TTL.
type oneShotStore struct {
	mu    sync.Mutex
	slots map[string]oneShot
	ttl   time.Duration
	now   func() time.Time
}

// oneShot is one displayable token: the plaintext value, the probe it belongs
// to, and where the page showing it should offer to go back to.
type oneShot struct {
	probeID string
	value   string
	back    string
	expires time.Time
}

func newOneShotStore(ttl time.Duration, now func() time.Time) *oneShotStore {
	return &oneShotStore{slots: make(map[string]oneShot), ttl: ttl, now: now}
}

// putPair stores a token together with the probe it belongs to. back is the
// page the token page returns to; see safeBackPath.
func (o *oneShotStore) putPair(probeID, value, back string) (string, error) {
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
	o.slots[slot] = oneShot{
		probeID: probeID,
		value:   value,
		back:    safeBackPath(back),
		expires: now.Add(o.ttl),
	}
	return slot, nil
}

// takePair redeems a slot, deleting it in the same critical section so two
// concurrent requests cannot both receive the token.
func (o *oneShotStore) takePair(slot string) (oneShot, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()

	v, found := o.slots[slot]
	if !found {
		return oneShot{}, false
	}
	delete(o.slots, slot)
	if o.now().After(v.expires) {
		return oneShot{}, false
	}
	return v, true
}

// backPathPattern matches a plain absolute path with no empty segment, which is
// what rules out "//host" and any backslash.
var backPathPattern = regexp.MustCompile(`^/(?:[A-Za-z0-9._~-]+/)*[A-Za-z0-9._~-]*$`)

// safeBackPath keeps a return path safe to put in an href.
//
// Both callers are server-side today and pass constants, but this value lands in
// a link on a public page, and getting it wrong is an open redirect: browsers
// read "//evil.example" and "/\evil.example" as scheme-relative URLs pointing at
// another host. Anything that is not a plain path on this site degrades to the
// registration page rather than failing the mint — the token is the point, and
// losing it over a bad return link would be the worse outcome.
func safeBackPath(path string) string {
	if backPathPattern.MatchString(path) {
		return path
	}
	return "/"
}

func randomString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("portal: read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
