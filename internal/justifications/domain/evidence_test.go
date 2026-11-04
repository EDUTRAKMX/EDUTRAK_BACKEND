package domain

import (
	"errors"
	"strings"
	"testing"
)

var (
	pdfHead  = []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	jpgHead  = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	pngHead  = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0x0D}
	exeHead  = []byte("MZ\x90\x00\x03\x00\x00\x00")
	htmlHead = []byte("<html><script>alert(1)</script>")
	elfHead  = []byte("\x7fELF\x02\x01\x01")
	zipHead  = []byte("PK\x03\x04")
)

func TestInspectAcceptsTheThreeAllowedTypes(t *testing.T) {
	tests := []struct {
		name string
		head []byte
		want EvidenceType
	}{
		{"constancia.pdf", pdfHead, TypePDF},
		{"CONSTANCIA.PDF", pdfHead, TypePDF},
		{"foto.jpg", jpgHead, TypeJPEG},
		{"foto.jpeg", jpgHead, TypeJPEG},
		{"FOTO.JPG", jpgHead, TypeJPEG},
		{"captura.png", pngHead, TypePNG},
	}
	for _, tt := range tests {
		got, name, err := InspectEvidence(tt.name, tt.head)
		if err != nil || got != tt.want || name != tt.name {
			t.Errorf("%s: %+v %q %v", tt.name, got, name, err)
		}
	}
	if TypeJPEG.Ext != "jpg" || TypePDF.Mime != "application/pdf" || TypePNG.Mime != "image/png" || TypeJPEG.Mime != "image/jpeg" {
		t.Fatal("canonical types")
	}
}

func TestInspectRejectsFakeAndForbiddenContent(t *testing.T) {
	tests := []struct {
		name string
		file string
		head []byte
		want error
	}{
		{"executable renamed to pdf", "factura.pdf", exeHead, ErrUnsupportedEvidence},
		{"html renamed to png", "x.png", htmlHead, ErrUnsupportedEvidence},
		{"elf renamed to jpg", "x.jpg", elfHead, ErrUnsupportedEvidence},
		{"zip renamed to pdf", "x.pdf", zipHead, ErrUnsupportedEvidence},
		{"png content with pdf extension", "x.pdf", pngHead, ErrEvidenceMismatch},
		{"pdf content with jpg extension", "x.jpg", pdfHead, ErrEvidenceMismatch},
		{"jpeg content with png extension", "x.png", jpgHead, ErrEvidenceMismatch},
		{"real pdf with forbidden extension", "x.exe", pdfHead, ErrUnsupportedEvidence},
		{"real pdf with script extension", "x.html", pdfHead, ErrUnsupportedEvidence},
		{"double extension", "x.pdf.exe", pdfHead, ErrUnsupportedEvidence},
		{"no extension", "constancia", pdfHead, ErrUnsupportedEvidence},
		{"gif", "x.gif", []byte("GIF89a"), ErrUnsupportedEvidence},
		{"empty file", "x.pdf", nil, ErrEmptyEvidence},
		{"empty name", "", pdfHead, ErrInvalidFilename},
		{"only dots", "...", pdfHead, ErrInvalidFilename},
		{"NUL in name", "a\x00.pdf", pdfHead, ErrInvalidFilename},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := InspectEvidence(tt.file, tt.head); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSafeFilenameStripsPathsAndControlCharacters(t *testing.T) {
	tests := []struct{ in, want string }{
		{"constancia.pdf", "constancia.pdf"},
		{"../../etc/passwd.pdf", "passwd.pdf"},
		{`..\..\windows\system32\cmd.pdf`, "cmd.pdf"},
		{"/var/www/html/shell.pdf", "shell.pdf"},
		{"C:\\Users\\x\\a.png", "a.png"},
		{"a\r\nb.pdf", "ab.pdf"},
		{`evil".pdf`, "evil.pdf"},
		{"  spaced name .pdf  ", "spaced name .pdf"},
		{"constancia médica.pdf", "constancia médica.pdf"},
		{".hidden.pdf", "hidden.pdf"},
	}
	for _, tt := range tests {
		got, err := SafeFilename(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("SafeFilename(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
		if strings.ContainsAny(got, `/\`) || strings.Contains(got, "..") && !strings.HasSuffix(got, "..") && strings.Contains(got, "/") {
			t.Errorf("%q still carries a path", got)
		}
	}
	for _, bad := range []string{"", "..", "/", "../", "   ", "\x01\x02", "a\xffb.pdf"} {
		if _, err := SafeFilename(bad); !errors.Is(err, ErrInvalidFilename) {
			t.Errorf("SafeFilename(%q) must fail, got %v", bad, err)
		}
	}
	long, err := SafeFilename(strings.Repeat("a", 400) + ".pdf")
	if err != nil || len(long) > 200 || !strings.HasSuffix(long, ".pdf") {
		t.Fatalf("long name: %d %q %v", len(long), long[len(long)-8:], err)
	}
}
