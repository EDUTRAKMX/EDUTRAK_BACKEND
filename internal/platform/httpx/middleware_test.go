package httpx

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func apiMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, _ *http.Request) { JSON(w, 200, map[string]string{"ok": "1"}) })
	return mux
}

func TestValidateOrigin(t *testing.T) {
	for _, ok := range []string{"https://app.example.edu", "http://localhost:5173", "https://a.b.c:8443"} {
		if err := ValidateOrigin(ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"*", "", "app.example.edu", "ftp://x.edu", "https://x.edu/", "https://x.edu/path", "https://u:p@x.edu", "https://x.edu?q=1", "https://"} {
		if err := ValidateOrigin(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestCORS(t *testing.T) {
	h := CORS([]string{"https://app.example.edu"}, JSONFallback(apiMux()))

	t.Run("allowed origin on a normal request", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/ping", nil)
		r.Header.Set("Origin", "https://app.example.edu")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.edu" {
			t.Fatalf("allow-origin = %q", got)
		}
		if w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal("credentials must not be enabled")
		}
		if !strings.Contains(w.Header().Get("Vary"), "Origin") {
			t.Fatal("Vary: Origin missing")
		}
	})
	t.Run("preflight is answered without reaching routes", func(t *testing.T) {
		r := httptest.NewRequest("OPTIONS", "/api/anything", nil)
		r.Header.Set("Origin", "https://app.example.edu")
		r.Header.Set("Access-Control-Request-Method", "PATCH")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 204 || !strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), "PATCH") ||
			!strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
			t.Fatalf("preflight = %d %v", w.Code, w.Header())
		}
	})
	t.Run("foreign origin gets no CORS headers, preflight is refused", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/ping", nil)
		r.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("foreign origin must not be echoed")
		}
		r = httptest.NewRequest("OPTIONS", "/api/ping", nil)
		r.Header.Set("Origin", "https://evil.example")
		r.Header.Set("Access-Control-Request-Method", "GET")
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("foreign preflight = %d", w.Code)
		}
	})
	t.Run("a null origin and a prefix lookalike are not allowed", func(t *testing.T) {
		for _, o := range []string{"null", "https://app.example.edu.evil.com", "http://app.example.edu"} {
			r := httptest.NewRequest("GET", "/api/ping", nil)
			r.Header.Set("Origin", o)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("origin %q was allowed", o)
			}
		}
	})
	t.Run("no configured origins means no CORS headers at all", func(t *testing.T) {
		none := CORS(nil, JSONFallback(apiMux()))
		r := httptest.NewRequest("GET", "/api/ping", nil)
		r.Header.Set("Origin", "https://app.example.edu")
		w := httptest.NewRecorder()
		none.ServeHTTP(w, r)
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("unexpected CORS header")
		}
	})
}

func TestSecurityHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	SecurityHeaders(JSONFallback(apiMux())).ServeHTTP(w, httptest.NewRequest("GET", "/api/ping", nil))
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Error("CSP missing")
	}
}

func TestJSONFallback(t *testing.T) {
	h := JSONFallback(apiMux())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/nope", nil))
	if w.Code != 404 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || !strings.Contains(w.Body.String(), `"not_found"`) {
		t.Fatalf("404 = %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/ping", nil))
	if w.Code != 405 || !strings.Contains(w.Body.String(), `"method_not_allowed"`) || !strings.Contains(w.Header().Get("Allow"), "GET") {
		t.Fatalf("405 = %d %q allow=%q", w.Code, w.Body.String(), w.Header().Get("Allow"))
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/ping", nil))
	if w.Code != 200 {
		t.Fatalf("known route = %d", w.Code)
	}
}

func TestDecodeJSONContentTypeAndSize(t *testing.T) {
	var dst struct{ A string }
	do := func(ct, body string) error {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		return DecodeJSON(httptest.NewRecorder(), r, &dst)
	}
	if err := do("application/json; charset=utf-8", `{"A":"x"}`); err != nil {
		t.Fatalf("json: %v", err)
	}
	if err := do("", `{"A":"x"}`); err != nil {
		t.Fatalf("missing content-type is accepted: %v", err)
	}
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		err := do(ct, `{"A":"x"}`)
		if err == nil || !strings.Contains(err.Error(), "Content-Type") {
			t.Errorf("%s should be 415, got %v", ct, err)
		}
	}
	big := `{"A":"` + strings.Repeat("x", maxBodyBytes+10) + `"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(big))
	err := DecodeJSON(w, r, &dst)
	rec := httptest.NewRecorder()
	Error(rec, r, err)
	if rec.Code != 413 {
		t.Fatalf("oversized body = %d, want 413", rec.Code)
	}
}

// Regression: calling the handler returned by mux.Handler directly loses r.PathValue, which
// turned every {id} route into "not found".
func TestJSONFallbackKeepsPathValues(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, 200, map[string]string{"id": r.PathValue("id")})
	})
	w := httptest.NewRecorder()
	JSONFallback(mux).ServeHTTP(w, httptest.NewRequest("GET", "/api/items/abc", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"abc"`) {
		t.Fatalf("path value lost: %d %s", w.Code, w.Body.String())
	}
}

// A slow upload must survive the short server-wide ReadTimeout only on the route that asks for
// more time, and the extension must work through the Base middleware's response wrapper.
func TestExtendDeadlinesLetsASlowUploadFinish(t *testing.T) {
	slowBody := func() io.Reader {
		pr, pw := io.Pipe()
		go func() {
			for i := 0; i < 4; i++ {
				time.Sleep(150 * time.Millisecond)
				_, _ = pw.Write([]byte("chunk"))
			}
			_ = pw.Close()
		}()
		return pr
	}
	run := func(extend bool) (int, error) {
		mux := http.NewServeMux()
		mux.HandleFunc("POST /up", func(w http.ResponseWriter, r *http.Request) {
			if extend {
				ExtendDeadlines(w, 5*time.Second, 6*time.Second)
			}
			b, err := io.ReadAll(r.Body)
			if err != nil || len(b) != 20 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
		})
		srv := httptest.NewUnstartedServer(Base(slog.New(slog.NewTextHandler(io.Discard, nil)), mux))
		srv.Config.ReadTimeout = 250 * time.Millisecond
		srv.Start()
		defer srv.Close()
		resp, err := http.Post(srv.URL+"/up", "application/octet-stream", slowBody())
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return resp.StatusCode, nil
	}
	if code, err := run(false); err == nil && code == http.StatusCreated {
		t.Fatal("without the extension the short ReadTimeout should have cut the upload")
	}
	if code, err := run(true); err != nil || code != http.StatusCreated {
		t.Fatalf("with the extension the slow upload must finish: %d %v", code, err)
	}
}

func TestSafeErrorTextDropsRowDetail(t *testing.T) {
	err := fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23514", TableName: "tutoring_followups", ConstraintName: "note_check",
		Message: "new row violates check constraint", Detail: "Failing row contains (secret tutoring note text)"})
	got := SafeErrorText(err)
	if strings.Contains(got, "secret") || !strings.Contains(got, "23514") || !strings.Contains(got, "note_check") {
		t.Fatalf("unsafe or useless log text: %q", got)
	}
	if SafeErrorText(errors.New("plain")) != "plain" {
		t.Fatal("non-postgres errors are logged unchanged")
	}
}
