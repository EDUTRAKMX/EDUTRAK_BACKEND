// Package security implements the technical QR token generator.
package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// QRTokens issues 256-bit random tokens from crypto/rand. A token is shown once;
// only its SHA-256 (hex) is stored, so a database leak cannot be replayed.
type QRTokens struct{}

func NewQRTokens() QRTokens { return QRTokens{} }

func (QRTokens) New() (token, hash string, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b[:])
	return token, QRTokens{}.Hash(token), nil
}

func (QRTokens) Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
