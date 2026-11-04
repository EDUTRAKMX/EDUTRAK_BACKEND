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
	"mime"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/jackc/pgx/v5/pgconn"
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
//
// A Content-Type other than application/json is refused with 415 (a missing one is treated as
// JSON, like the rest of the API) and a body above the limit with 413, not a generic 400.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
			return apperr.New(apperr.KindUnsupportedMedia, "unsupported_media_type", "Content-Type must be application/json")
		}
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return apperr.New(apperr.KindPayloadTooLarge, "payload_too_large", "request body too large")
		}
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
		case apperr.KindPayloadTooLarge:
			status = http.StatusRequestEntityTooLarge
		case apperr.KindUnsupportedMedia:
			status = http.StatusUnsupportedMediaType
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
			LoggerFrom(r.Context()).Error("unexpected error", "error", SafeErrorText(err))
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

// Unwrap lets http.NewResponseController reach the real connection (deadlines, flushing).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

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

// ExtendDeadlines gives ONE request more time to read its body and to write its answer than the
// server-wide ReadTimeout/WriteTimeout, which stay short for every other route. It is meant for
// file uploads over slow links. It does nothing when the connection does not support deadlines.
func ExtendDeadlines(w http.ResponseWriter, read, write time.Duration) {
	rc := http.NewResponseController(w)
	now := time.Now()
	_ = rc.SetReadDeadline(now.Add(read))
	_ = rc.SetWriteDeadline(now.Add(write))
}

// SafeErrorText is what may be logged for an unexpected error. A PostgreSQL constraint error
// carries the failing row in its detail (which could hold a tutoring note or a file name), so only
// its SQLSTATE, table and constraint are kept; any other error is logged as is.
func SafeErrorText(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return "postgres error: sqlstate=" + pgErr.Code + " table=" + pgErr.TableName + " constraint=" + pgErr.ConstraintName
	}
	return err.Error()
}
