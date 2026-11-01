package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/edutrak/backend/internal/identity/ports"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const jwtIssuer = "edutrak"

// JWTTokenService issues short-lived HS256 access tokens bound to a session id
// (so revoking the session kills the token) and opaque refresh tokens.
type JWTTokenService struct {
	secret    []byte
	accessTTL time.Duration
}

func NewJWTTokenService(secret []byte, accessTTL time.Duration) (*JWTTokenService, error) {
	if len(secret) < 32 {
		return nil, errors.New("jwt secret must be at least 32 bytes")
	}
	return &JWTTokenService{secret: secret, accessTTL: accessTTL}, nil
}

type accessClaims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

func (s *JWTTokenService) IssueAccessToken(c ports.AccessClaims, now time.Time) (string, time.Time, error) {
	exp := now.Add(s.accessTTL)
	claims := accessClaims{
		SessionID: c.SessionID.String(),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			Subject:   c.UserID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return tok, exp, nil
}

func (s *JWTTokenService) ParseAccessToken(token string, now time.Time) (ports.AccessClaims, error) {
	var claims accessClaims
	_, err := jwt.ParseWithClaims(token, &claims,
		func(*jwt.Token) (any, error) { return s.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(jwtIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err != nil {
		return ports.AccessClaims{}, err
	}
	uid, err := uuid.Parse(claims.Subject)
	if err != nil {
		return ports.AccessClaims{}, errors.New("invalid subject")
	}
	sid, err := uuid.Parse(claims.SessionID)
	if err != nil {
		return ports.AccessClaims{}, errors.New("invalid session id")
	}
	return ports.AccessClaims{UserID: uid, SessionID: sid}, nil
}

// NewRefreshToken returns 256 bits of randomness; only its SHA-256 is stored.
func (s *JWTTokenService) NewRefreshToken() (string, string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("random: %w", err)
	}
	tok := base64.RawURLEncoding.EncodeToString(b[:])
	return tok, s.HashRefreshToken(tok), nil
}

func (s *JWTTokenService) HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
