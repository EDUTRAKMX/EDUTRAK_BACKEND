package security

import (
	"encoding/base64"
	"regexp"
	"testing"
)

func TestTokensAreRandomLongAndOnlyTheirHashIsDerivable(t *testing.T) {
	g := NewQRTokens()
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		tok, hash, err := g.New()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil || len(raw) != 32 {
			t.Fatalf("token %q: want 32 random bytes (256 bits), got %d (%v)", tok, len(raw), err)
		}
		if seen[tok] {
			t.Fatal("token repeated")
		}
		seen[tok] = true
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(hash) {
			t.Fatalf("hash %q is not a SHA-256 hex digest", hash)
		}
		if hash == tok || g.Hash(tok) != hash {
			t.Fatal("hash must be a deterministic digest different from the token")
		}
		if g.Hash(tok+"x") == hash {
			t.Fatal("different tokens must hash differently")
		}
	}
}
