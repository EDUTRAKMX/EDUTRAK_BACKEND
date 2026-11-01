package httpx

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edutrak/backend/internal/platform/apperr"
)

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"validation", apperr.New(apperr.KindValidation, "x", "m"), 422},
		{"invalid body", apperr.New(apperr.KindValidation, "invalid_body", "m"), 400},
		{"unauthenticated", apperr.New(apperr.KindUnauthenticated, "x", "m"), 401},
		{"forbidden", apperr.New(apperr.KindForbidden, "x", "m"), 403},
		{"not found", apperr.New(apperr.KindNotFound, "x", "m"), 404},
		{"conflict", apperr.New(apperr.KindConflict, "x", "m"), 409},
		{"rate limited", &apperr.Error{Kind: apperr.KindRateLimited, Code: "x", Message: "m", RetryAfter: 90 * time.Second}, 429},
		{"unexpected", errors.New("pq: syntax error at SELECT"), 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Error(rec, httptest.NewRequest("GET", "/", nil), tt.err)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want == 500 && strings.Contains(rec.Body.String(), "syntax") {
				t.Fatal("internal error details leaked to client")
			}
			if tt.want == 429 && rec.Header().Get("Retry-After") != "90" {
				t.Fatalf("Retry-After = %q, want 90", rec.Header().Get("Retry-After"))
			}
		})
	}
}

func TestBaseRecoversPanic(t *testing.T) {
	h := Base(slog.New(slog.NewTextHandler(io.Discard, nil)), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 || rec.Header().Get("X-Request-Id") == "" {
		t.Fatalf("status=%d reqid=%q", rec.Code, rec.Header().Get("X-Request-Id"))
	}
}

func TestDecodeJSONRejectsUnknownFields(t *testing.T) {
	var dst struct{ A string }
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"A":"1","B":2}`))
	if err := DecodeJSON(httptest.NewRecorder(), r, &dst); err == nil {
		t.Fatal("expected error for unknown field")
	}
}
