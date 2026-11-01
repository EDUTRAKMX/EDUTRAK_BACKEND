package security

import (
	"strings"
	"testing"
	"time"

	"github.com/edutrak/backend/internal/identity/ports"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var fastParams = Argon2Params{MemoryKiB: 8, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

func TestArgon2RoundTrip(t *testing.T) {
	h := NewArgon2Hasher(fastParams)
	hash, err := h.Hash("s3cret-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") || strings.Contains(hash, "s3cret") {
		t.Fatalf("unexpected hash format: %s", hash)
	}
	if ok, err := h.Verify("s3cret-passphrase", hash); err != nil || !ok {
		t.Fatalf("correct password: ok=%v err=%v", ok, err)
	}
	if ok, err := h.Verify("other-passphrase", hash); err != nil || ok {
		t.Fatalf("wrong password: ok=%v err=%v", ok, err)
	}
	hash2, _ := h.Hash("s3cret-passphrase")
	if hash == hash2 {
		t.Fatal("hashes must be salted")
	}
}

func TestArgon2VerifyUsesStoredParams(t *testing.T) {
	hash, _ := NewArgon2Hasher(fastParams).Hash("pw-for-params")
	// A hasher configured differently must still verify an older hash.
	if ok, err := NewArgon2Hasher(DefaultArgon2Params).Verify("pw-for-params", hash); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestArgon2MalformedHash(t *testing.T) {
	h := NewArgon2Hasher(fastParams)
	for _, bad := range []string{"", "plaintext", "$argon2id$v=19$m=1,t=1,p=1$!!$!!", "$bcrypt$a$b$c$d", "$argon2id$v=19$m=99999999,t=1,p=1$AAAA$AAAA"} {
		if ok, err := h.Verify("x", bad); ok || err == nil {
			t.Errorf("%q: ok=%v err=%v, want error", bad, ok, err)
		}
	}
}

func newSvc(t *testing.T) *JWTTokenService {
	t.Helper()
	s, err := NewJWTTokenService([]byte(strings.Repeat("k", 32)), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAccessTokenRoundTripAndExpiry(t *testing.T) {
	s := newSvc(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	want := ports.AccessClaims{UserID: uuid.New(), SessionID: uuid.New()}
	tok, exp, err := s.IssueAccessToken(want, now)
	if err != nil || !exp.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("exp=%v err=%v", exp, err)
	}
	got, err := s.ParseAccessToken(tok, now.Add(14*time.Minute))
	if err != nil || got != want {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := s.ParseAccessToken(tok, now.Add(16*time.Minute)); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestAccessTokenRejectsTampering(t *testing.T) {
	s := newSvc(t)
	now := time.Now()
	claims := ports.AccessClaims{UserID: uuid.New(), SessionID: uuid.New()}
	tok, _, _ := s.IssueAccessToken(claims, now)

	other, _ := NewJWTTokenService([]byte(strings.Repeat("z", 32)), time.Minute)
	foreign, _, _ := other.IssueAccessToken(claims, now)

	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": claims.UserID.String(), "sid": claims.SessionID.String(), "iss": jwtIssuer, "exp": now.Add(time.Hour).Unix(),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)

	wrongIss, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": claims.UserID.String(), "sid": claims.SessionID.String(), "iss": "evil", "exp": now.Add(time.Hour).Unix(),
	}).SignedString(s.secret)

	noExp, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": claims.UserID.String(), "sid": claims.SessionID.String(), "iss": jwtIssuer,
	}).SignedString(s.secret)

	for name, bad := range map[string]string{
		"flipped signature": tok[:len(tok)-2] + "xx",
		"other secret":      foreign,
		"alg none":          none,
		"wrong issuer":      wrongIss,
		"no expiry":         noExp,
		"garbage":           "a.b.c",
		"empty":             "",
	} {
		if _, err := s.ParseAccessToken(bad, now); err == nil {
			t.Errorf("%s: token accepted", name)
		}
	}
}

func TestShortSecretRejected(t *testing.T) {
	if _, err := NewJWTTokenService([]byte("short"), time.Minute); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestRefreshToken(t *testing.T) {
	s := newSvc(t)
	tok, hash, err := s.NewRefreshToken()
	if err != nil || len(hash) != 64 || hash == tok || s.HashRefreshToken(tok) != hash {
		t.Fatalf("tok=%q hash=%q err=%v", tok, hash, err)
	}
	tok2, _, _ := s.NewRefreshToken()
	if tok == tok2 {
		t.Fatal("refresh tokens must be unique")
	}
}
