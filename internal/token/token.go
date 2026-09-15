// Package token generates and hashes probe authentication tokens.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Prefix marks a string as a probe token.
const Prefix = "cqu_probe_"

// secretBytes is the raw entropy per token. 32 bytes = 256 bit, the floor
// required by Protocol v1 §30.
const secretBytes = 32

// Generate returns a new token of the form "cqu_probe_<43 base64url chars>".
func Generate() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("token: read random bytes: %w", err)
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// Hash returns the lowercase hex SHA-256 of a token.
//
// SHA-256 rather than bcrypt/argon2 is deliberate: tokens are 256-bit uniform
// random values, so there is no dictionary or brute-force input space for a slow
// KDF to defend. A salted KDF would also be unindexable, forcing a full table
// scan plus N KDF evaluations per push. See the design doc §5.1.
func Hash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}
