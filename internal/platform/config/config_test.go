package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://u:p@localhost:5432/db",
		"JWT_SECRET":   strings.Repeat("x", 32),
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LoginMaxFailuresPerAccount != 5 || cfg.LoginMaxFailuresPerIP != 150 || cfg.LoginRateWindow != 15*time.Minute {
		t.Fatalf("unexpected login rate defaults: %+v", cfg)
	}
	if cfg.HTTPPort != "8080" || cfg.AccessTokenTTL != 15*time.Minute || cfg.QRTokenTTL != 30*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadFailsFast(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]string)
		want   string
	}{
		{"missing database", func(m map[string]string) { delete(m, "DATABASE_URL") }, "DATABASE_URL"},
		{"missing secret", func(m map[string]string) { delete(m, "JWT_SECRET") }, "JWT_SECRET"},
		{"short secret", func(m map[string]string) { m["JWT_SECRET"] = "short" }, "at least"},
		{"bad ttl", func(m map[string]string) { m["ACCESS_TOKEN_TTL"] = "abc" }, "ACCESS_TOKEN_TTL"},
		{"bad port", func(m map[string]string) { m["HTTP_PORT"] = "99999" }, "HTTP_PORT"},
		{"bad network", func(m map[string]string) { m["ALLOWED_NETWORKS"] = "not-a-cidr" }, "ALLOWED_NETWORKS"},
		{"zero login failures", func(m map[string]string) { m["LOGIN_MAX_FAILURES_PER_ACCOUNT"] = "0" }, "LOGIN_MAX_FAILURES_PER_ACCOUNT"},
		{"bad login window", func(m map[string]string) { m["LOGIN_RATE_WINDOW"] = "soon" }, "LOGIN_RATE_WINDOW"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := validEnv()
			tt.mutate(m)
			_, err := Load(env(m))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestLoadDotEnv(t *testing.T) {
	// t.Setenv registers restoration of the original values; unset afterwards so
	// "not set" is what LoadDotEnv sees.
	for _, k := range []string{"EDUTRAK_T_NEW", "EDUTRAK_T_QUOTED", "EDUTRAK_T_EXISTING", "EDUTRAK_T_DOLLAR"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("EDUTRAK_T_EXISTING", "from-process")

	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\n\nEDUTRAK_T_NEW=plain\nEDUTRAK_T_QUOTED=\"with spaces\"\nEDUTRAK_T_EXISTING=from-file\nEDUTRAK_T_DOLLAR=ab$Bcd\nnot a pair\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"EDUTRAK_T_NEW": "plain", "EDUTRAK_T_QUOTED": "with spaces",
		"EDUTRAK_T_EXISTING": "from-process", // process environment wins
		"EDUTRAK_T_DOLLAR":   "ab$Bcd",       // no shell-style interpolation
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "missing.env")); err != nil {
		t.Fatalf("missing file must not be an error: %v", err)
	}
}

func TestParsePrefixes(t *testing.T) {
	got, err := ParsePrefixes("10.0.0.0/8, 192.168.1.5")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	if got[1].Bits() != 32 {
		t.Fatalf("single IP should become /32, got %v", got[1])
	}
}
