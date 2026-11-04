package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const validKey = "ab/abcdef0123456789abcdef0123456789.pdf"

func newFS(t *testing.T) (*FS, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func TestPutOpenDeleteRoundTrip(t *testing.T) {
	s, root := newFS(t)
	ctx := context.Background()
	n, err := s.Put(ctx, validKey, strings.NewReader("%PDF-hello"))
	if err != nil || n != 10 {
		t.Fatalf("put: %d %v", n, err)
	}
	rc, err := s.Open(ctx, validKey)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "%PDF-hello" {
		t.Fatalf("read back %q", b)
	}
	// stays inside the root, owner-only on Unix
	p := filepath.Join(root, "ab", "abcdef0123456789abcdef0123456789.pdf")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("object not where expected: %v", err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v, want 0600", st.Mode().Perm())
	}
	if err := s.Delete(ctx, validKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, validKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.Delete(ctx, validKey); err != nil {
		t.Fatalf("deleting a missing object is not an error: %v", err)
	}
}

func TestKeysThatCouldEscapeTheRootAreRefused(t *testing.T) {
	s, root := newFS(t)
	ctx := context.Background()
	outside := filepath.Join(filepath.Dir(root), "outside.pdf")
	bad := []string{
		"", "../outside.pdf", "../../etc/passwd", "ab/../../outside.pdf", "/etc/passwd", `C:\Windows\win.ini`,
		`ab\abcdef0123456789abcdef0123456789.pdf`, "ab/abcdef0123456789abcdef0123456789.exe",
		"ab/abcdef0123456789abcdef0123456789.pdf/extra", "ab/ABCDEF0123456789ABCDEF0123456789.pdf",
		"abc/abcdef0123456789abcdef0123456789.pdf", "ab/abcdef0123456789abcdef0123456789.pdf\x00.png",
		"ab/short.pdf", "..", ".", "ab/../ab/abcdef0123456789abcdef0123456789.pdf",
		"ab/abcdef0123456789abcdef0123456789.pdf\n",
	}
	for _, k := range bad {
		if _, err := s.Put(ctx, k, strings.NewReader("x")); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Put(%q) = %v, want ErrInvalidKey", k, err)
		}
		if _, err := s.Open(ctx, k); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Open(%q) = %v", k, err)
		}
		if err := s.Delete(ctx, k); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Delete(%q) = %v", k, err)
		}
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("a file was written outside the root")
	}
}

func TestPutNeverOverwrites(t *testing.T) {
	s, _ := newFS(t)
	ctx := context.Background()
	if _, err := s.Put(ctx, validKey, strings.NewReader("original")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, validKey, strings.NewReader("attacker")); err == nil {
		t.Fatal("a second Put on the same key must fail")
	}
	rc, _ := s.Open(ctx, validKey)
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "original" {
		t.Fatalf("object now holds %q: it was overwritten", b)
	}
}

func TestFailedWriteLeavesNoPartialFile(t *testing.T) {
	s, root := newFS(t)
	boom := errors.New("network died")
	r := io.MultiReader(strings.NewReader("%PDF-partial"), &errReader{boom})
	if _, err := s.Put(context.Background(), validKey, r); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "ab", "abcdef0123456789abcdef0123456789.pdf")); err == nil {
		t.Fatal("a partial file survived a failed write")
	}
	// the key can be used again afterwards
	if _, err := s.Put(context.Background(), validKey, bytes.NewReader([]byte("%PDF-ok"))); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
}

type errReader struct{ err error }

func (e *errReader) Read([]byte) (int, error) { return 0, e.err }

func TestCancelledContextStopsTheUpload(t *testing.T) {
	s, root := newFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, validKey, strings.NewReader("%PDF-x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "ab", "abcdef0123456789abcdef0123456789.pdf")); err == nil {
		t.Fatal("cancelled upload left a file")
	}
}

func TestUnconfiguredStoreRefusesEverything(t *testing.T) {
	s, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Put(ctx, validKey, strings.NewReader("x")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Put: %v", err)
	}
	if _, err := s.Open(ctx, validKey); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Open: %v", err)
	}
	if entries, _ := os.ReadDir("."); len(entries) > 0 {
		for _, e := range entries {
			if e.Name() == "ab" {
				t.Fatal("an unconfigured store wrote into the working directory")
			}
		}
	}
}

func TestRootIsCreatedPrivate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a", "b", "evidence")
	if _, err := New(root); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		t.Fatalf("root not created: %v", err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o700 {
		t.Fatalf("root mode %v, want 0700", st.Mode().Perm())
	}
}
