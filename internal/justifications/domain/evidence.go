package domain

import (
	"bytes"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// EvidenceType is the real kind of a file, decided from its content.
type EvidenceType struct {
	Mime string
	Ext  string // canonical extension used in the storage key
}

var (
	TypePDF  = EvidenceType{"application/pdf", "pdf"}
	TypeJPEG = EvidenceType{"image/jpeg", "jpg"}
	TypePNG  = EvidenceType{"image/png", "png"}
)

// SniffBytes is how many leading bytes are needed to recognise the allowed types.
const SniffBytes = 512

// Sniff recognises PDF, JPEG and PNG by their magic numbers. Anything else
// (executables, scripts, HTML, archives, ...) is unsupported, whatever the
// file name or the declared Content-Type say.
func Sniff(head []byte) (EvidenceType, bool) {
	switch {
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return TypePDF, true
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return TypeJPEG, true
	case bytes.HasPrefix(head, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return TypePNG, true
	}
	return EvidenceType{}, false
}

// extensionType maps an allowed client-side extension to the content type it must have.
func extensionType(ext string) (EvidenceType, bool) {
	switch strings.ToLower(ext) {
	case ".pdf":
		return TypePDF, true
	case ".jpg", ".jpeg":
		return TypeJPEG, true
	case ".png":
		return TypePNG, true
	}
	return EvidenceType{}, false
}

// SafeFilename reduces a client-supplied name to a harmless display name: path
// components and control characters are removed. It is NEVER used as a physical path.
func SafeFilename(raw string) (string, error) {
	if !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) {
		return "", ErrInvalidFilename
	}
	name := raw
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' || r == '<' || r == '>' || r == ':' || r == '|' || r == '?' || r == '*' {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.Trim(name, ". "))
	if name == "" || name == "." || name == ".." {
		return "", ErrInvalidFilename
	}
	if len(name) > 200 { // leave room for the column limit (255), keeping the extension
		ext := filepath.Ext(name)
		if len(ext) > 10 {
			return "", ErrInvalidFilename
		}
		name = truncateUTF8(strings.TrimSuffix(name, ext), 200-len(ext)) + ext
	}
	return name, nil
}

func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "")
}

// InspectEvidence validates a candidate file from its client name and first bytes:
// the extension must be allowed, the content must be a recognised allowed type, and
// both must agree. It returns the type to store under and the safe display name.
func InspectEvidence(clientName string, head []byte) (EvidenceType, string, error) {
	name, err := SafeFilename(clientName)
	if err != nil {
		return EvidenceType{}, "", err
	}
	if len(head) == 0 {
		return EvidenceType{}, "", ErrEmptyEvidence
	}
	byExt, ok := extensionType(filepath.Ext(name))
	if !ok {
		return EvidenceType{}, "", ErrUnsupportedEvidence
	}
	real, ok := Sniff(head)
	if !ok {
		return EvidenceType{}, "", ErrUnsupportedEvidence
	}
	if real != byExt {
		return EvidenceType{}, "", ErrEvidenceMismatch
	}
	return real, name, nil
}
