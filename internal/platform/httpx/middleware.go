package httpx

import (
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/edutrak/backend/internal/platform/apperr"
)

// SecurityHeaders sets conservative response headers on every response. This is a JSON API: it
// never serves HTML, so the CSP forbids everything and no response may be cached (they carry
// tokens or private academic data). Handlers may still override a header they set explicitly.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// ValidateOrigin reports whether s is an explicit CORS origin: scheme://host[:port] with an
// http or https scheme and nothing else (no path, query, userinfo) and never the wildcard.
func ValidateOrigin(s string) error {
	if s == "*" {
		return apperr.New(apperr.KindValidation, "invalid_origin", "the wildcard origin is not allowed")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(s, "/") {
		return apperr.New(apperr.KindValidation, "invalid_origin", "origin must look like https://host[:port] with no path")
	}
	return nil
}

const corsMaxAge = 10 * time.Minute

// CORS allows cross-origin browser calls only from the explicit origins given (exact,
// case-sensitive match against the Origin header). With no origins it adds nothing, so the
// browser's same-origin policy applies. Authentication uses a Bearer header, never cookies, so
// credentials are not enabled. Preflight (OPTIONS with Access-Control-Request-Method) is answered
// here with 204 and never reaches the routes.
func CORS(origins []string, next http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || len(allowed) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		if _, ok := allowed[origin]; !ok {
			// Not an allowed origin: no CORS headers, so the browser blocks the response.
			if isPreflight(r) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Expose-Headers", "X-Request-Id, Retry-After, Content-Disposition, WWW-Authenticate")
		if isPreflight(r) {
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", strconv.Itoa(int(corsMaxAge.Seconds())))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

// JSONFallback makes unknown routes (404) and wrong methods (405) answer with the standard JSON
// error instead of net/http's plain text, so a client can always parse the body. The Allow header
// of a 405 is kept.
func JSONFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			// Serve through the mux itself: only ServeMux.ServeHTTP fills r.PathValue.
			mux.ServeHTTP(w, r)
			return
		}
		probe := &probeWriter{header: http.Header{}}
		h.ServeHTTP(probe, r)
		switch probe.status {
		case http.StatusMethodNotAllowed:
			if allow := probe.header.Get("Allow"); allow != "" {
				w.Header().Set("Allow", allow)
			}
			JSON(w, http.StatusMethodNotAllowed, errorBody{Error: errorPayload{Code: "method_not_allowed", Message: "method not allowed", RequestID: RequestIDFrom(r.Context())}})
		case http.StatusNotFound, 0:
			JSON(w, http.StatusNotFound, errorBody{Error: errorPayload{Code: "not_found", Message: "route not found", RequestID: RequestIDFrom(r.Context())}})
		default:
			// A redirect (e.g. path cleaning): replay it as the mux decided.
			maps.Copy(w.Header(), probe.header)
			w.WriteHeader(probe.status)
		}
	})
}

// probeWriter records what the mux's own fallback handler would send, discarding the body.
type probeWriter struct {
	header http.Header
	status int
}

func (p *probeWriter) Header() http.Header         { return p.header }
func (p *probeWriter) Write(b []byte) (int, error) { return len(b), nil }
func (p *probeWriter) WriteHeader(code int)        { p.status = code }
