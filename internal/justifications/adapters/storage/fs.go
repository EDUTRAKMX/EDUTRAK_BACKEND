// Package storage implements ports.EvidenceStorage on a private local directory.
// Swapping it for object storage later only means providing another EvidenceStorage;
// the application never sees paths, only opaque keys.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// keyPattern is the only shape of key the application generates:
// "<2 hex>/<32 hex>.<pdf|jpg|png>". Everything else (separators, "..", absolute
// paths, drive letters, NULs, other extensions) is refused before touching the disk.
var keyPattern = regexp.MustCompile(`^[0-9a-f]{2}/[0-9a-f]{32}\.(pdf|jpg|png)$`)

var (
	ErrInvalidKey    = errors.New("storage: invalid key")
	ErrNotFound      = errors.New("storage: object not found")
	ErrNotConfigured = errors.New("storage: no directory configured")
)

// FS stores each object as a file below a root directory. Directories are 0700 and
// files 0600 (owner only). The directory must NOT be served by a web server.
type FS struct{ root string }

// New prepares the root directory (created if needed). An empty root yields a store
// that refuses every operation instead of silently writing to the working directory.
func New(root string) (*FS, error) {
	if root == "" {
		return &FS{}, nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve %q: %w", root, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("storage: create %q: %w", abs, err)
	}
	return &FS{root: abs}, nil
}

// path validates the key and returns the physical location, which is guaranteed to
// stay inside root.
func (s *FS) path(key string) (string, error) {
	if s.root == "" {
		return "", ErrNotConfigured
	}
	if !keyPattern.MatchString(key) {
		return "", ErrInvalidKey
	}
	return filepath.Join(s.root, filepath.FromSlash(key)), nil
}

// Put writes r to a NEW file (O_EXCL: an existing object is never overwritten). On any
// failure the partial file is removed.
func (s *FS) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	p, err := s.path(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return 0, fmt.Errorf("storage: create directory: %w", err)
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, fmt.Errorf("storage: create object: %w", err)
	}
	n, copyErr := io.Copy(f, &ctxReader{ctx: ctx, r: r})
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = os.Remove(p)
		return 0, fmt.Errorf("storage: write object: %w", err)
	}
	return n, nil
}

func (s *FS) Open(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("storage: open object: %w", err)
	}
	return f, nil
}

func (s *FS) Delete(_ context.Context, key string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("storage: delete object: %w", err)
	}
	return nil
}

// ctxReader stops a long upload when the request is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
