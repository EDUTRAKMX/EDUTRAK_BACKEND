// Package httpx holds HTTP plumbing shared by every module's adapters: JSON
// helpers, the single error-to-status convention and base middleware.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/edutrak/backend/internal/platform/apperr"
)

const maxBodyBytes = 1 << 20 // 1 MiB for JSON bodies

type errorBody struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// DecodeJSON parses a single JSON object, rejecting unknown fields and oversized bodies.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return apperr.New(apperr.KindValidation, "invalid_body", "request body is not valid JSON")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return apperr.New(apperr.KindValidation, "invalid_body", "request body must contain a single JSON object")
	}
	return nil
}

// Error writes err using the API-wide convention:
//
//	validation -> 422, unauthenticated -> 401, forbidden -> 403,
//	not found -> 404, conflict -> 409, rate limited -> 429 (+ Retry-After),
//	anything else -> 500 (details only in logs).
//
// Malformed bodies use code invalid_body with 400.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	status, payload := http.StatusInternalServerError, errorPayload{Code: "internal_error", Message: "unexpected error"}
	if ae, ok := apperr.As(err); ok {
		payload.Code, payload.Message = ae.Code, ae.Message
		switch ae.Kind {
		case apperr.KindValidation:
			status = http.StatusUnprocessableEntity
			if ae.Code == "invalid_body" {
				status = http.StatusBadRequest
			}
		case apperr.KindUnauthenticated:
			status = http.StatusUnauthorized
		case apperr.KindForbidden:
			status = http.StatusForbidden
		case apperr.KindNotFound:
			status = http.StatusNotFound
		case apperr.KindConflict:
			status = http.StatusConflict
		case apperr.KindRateLimited:
			status = http.StatusTooManyRequests
			if secs := int(math.Ceil(ae.RetryAfter.Seconds())); secs > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(secs))
			}
		}
	} else {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status, payload = http.StatusRequestEntityTooLarge, errorPayload{Code: "payload_too_large", Message: "request body too large"}
		} else {
			LoggerFrom(r.Context()).Error("unexpected error", "error", err)
		}
	}
	payload.RequestID = RequestIDFrom(r.Context())
	JSON(w, status, errorBody{Error: payload})
}

type ctxKey int

const (
	requestIDKey ctxKey = iota
	loggerKey
)

func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// LoggerFrom returns the request-scoped logger (carrying request_id), or the default logger.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Base wraps h with request id, structured access logging and panic recovery.
// The access log records method, path and status only; never headers or bodies.
func Base(log *slog.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [8]byte
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		w.Header().Set("X-Request-Id", id)

		reqLog := log.With("request_id", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		ctx = context.WithValue(ctx, loggerKey, reqLog)
		r = r.WithContext(ctx)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		defer func() {
			if p := recover(); p != nil {
				reqLog.Error("panic recovered", "panic", p, "stack", string(debug.Stack()))
				Error(rec, r, errors.New("panic"))
			}
			reqLog.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "duration_ms", time.Since(start).Milliseconds())
		}()
		h.ServeHTTP(rec, r)
	})
}
