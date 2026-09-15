package token

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateFormat(t *testing.T) {
	tok, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.HasPrefix(tok, Prefix) {
		t.Fatalf("token %q does not start with %q", tok, Prefix)
	}
	body := strings.TrimPrefix(tok, Prefix)
	if len(body) != 43 {
		t.Fatalf("token body length = %d, want 43 (32 bytes base64url)", len(body))
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("token body is not valid base64url: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("decoded entropy = %d bytes, want 32 (256 bit)", len(raw))
	}
}

func TestGenerateIsUnique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		tok, err := Generate()
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if seen[tok] {
			t.Fatalf("duplicate token generated: %q", tok)
		}
		seen[tok] = true
	}
}

func TestHashIsDeterministicHex(t *testing.T) {
	const tok = "cqu_probe_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	h1 := Hash(tok)
	h2 := Hash(tok)
	if h1 != h2 {
		t.Fatalf("Hash is not deterministic: %q != %q", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("hash length = %d, want 64 hex chars (sha256)", len(h1))
	}
	for _, c := range h1 {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("hash contains non-lowercase-hex rune %q", c)
		}
	}
}

func TestHashDiffersForDifferentTokens(t *testing.T) {
	a, _ := Generate()
	b, _ := Generate()
	if Hash(a) == Hash(b) {
		t.Fatal("different tokens produced the same hash")
	}
}
