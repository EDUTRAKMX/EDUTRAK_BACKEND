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
		"JWT_SECRET":   "k3Zp9Qv1Lm8Xt4Rw7Yb2Nc6Hd5Gf0Ja!", // fictitious but not a placeholder
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

func TestAttendanceDefaults(t *testing.T) {
	cfg, err := Load(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AttendanceLateAfterMinutes != 15 || cfg.AttendanceLatesPerAbsence != 3 || cfg.AttendanceAllowAnyNetwork {
		t.Fatalf("unexpected attendance defaults: %+v", cfg)
	}
	if cfg.InstitutionLocation == nil || cfg.InstitutionLocation.String() != "America/Mexico_City" {
		t.Fatalf("location = %v", cfg.InstitutionLocation)
	}
	if len(cfg.AllowedNetworks) != 0 {
		t.Fatal("no networks may be allowed by default")
	}
}

func TestAttendanceConfigValidation(t *testing.T) {
	abs, _ := filepath.Abs(".") // an absolute path on any OS
	prod := func(m map[string]string) { m["APP_ENV"] = "production"; m["STORAGE_DIR"] = abs }
	tests := []struct {
		name   string
		mutate func(map[string]string)
		want   string // empty = must load
	}{
		{"dev without networks loads (check-ins are simply denied)", func(m map[string]string) {}, ""},
		{"production without networks fails", prod, "ALLOWED_NETWORKS is required"},
		{"production with networks loads", func(m map[string]string) { prod(m); m["ALLOWED_NETWORKS"] = "192.0.2.0/24" }, ""},
		{"production refuses allow-any", func(m map[string]string) {
			prod(m)
			m["ALLOWED_NETWORKS"] = "192.0.2.0/24"
			m["ATTENDANCE_ALLOW_ANY_NETWORK"] = "true"
		}, "cannot be enabled"},
		{"production refuses 0.0.0.0/0", func(m map[string]string) { prod(m); m["ALLOWED_NETWORKS"] = "0.0.0.0/0" }, "matches every address"},
		{"production refuses ::/0", func(m map[string]string) { prod(m); m["ALLOWED_NETWORKS"] = "192.0.2.0/24,::/0" }, "matches every address"},
		{"development may allow any", func(m map[string]string) { m["ATTENDANCE_ALLOW_ANY_NETWORK"] = "true" }, ""},
		{"bad allow-any value", func(m map[string]string) { m["ATTENDANCE_ALLOW_ANY_NETWORK"] = "maybe" }, "ATTENDANCE_ALLOW_ANY_NETWORK"},
		{"qr ttl too short", func(m map[string]string) { m["QR_TOKEN_TTL"] = "1s" }, "QR_TOKEN_TTL"},
		{"qr ttl too long", func(m map[string]string) { m["QR_TOKEN_TTL"] = "1h" }, "QR_TOKEN_TTL"},
		{"qr ttl 60s ok", func(m map[string]string) { m["QR_TOKEN_TTL"] = "60s" }, ""},
		{"zero late minutes", func(m map[string]string) { m["ATTENDANCE_LATE_AFTER_MINUTES"] = "0" }, "ATTENDANCE_LATE_AFTER_MINUTES"},
		{"zero lates per absence", func(m map[string]string) { m["ATTENDANCE_LATES_PER_ABSENCE"] = "0" }, "ATTENDANCE_LATES_PER_ABSENCE"},
		{"zero absences threshold", func(m map[string]string) { m["ANALYTICS_ABSENCES_THRESHOLD"] = "0" }, "ANALYTICS_ABSENCES_THRESHOLD"},
		{"zero trend window", func(m map[string]string) { m["ANALYTICS_TREND_WINDOW"] = "0" }, "ANALYTICS_TREND_WINDOW"},
		{"huge trend window", func(m map[string]string) { m["ANALYTICS_TREND_WINDOW"] = "51" }, "ANALYTICS_TREND_WINDOW"},
		{"drop over 100 points", func(m map[string]string) { m["ANALYTICS_TREND_DROP_POINTS"] = "101" }, "ANALYTICS_TREND_DROP_POINTS"},
		{"custom analytics thresholds", func(m map[string]string) {
			m["ANALYTICS_ABSENCES_THRESHOLD"] = "5"
			m["ANALYTICS_TREND_WINDOW"] = "4"
			m["ANALYTICS_TREND_DROP_POINTS"] = "30"
		}, ""},
		{"bad timezone", func(m map[string]string) { m["INSTITUTION_TIMEZONE"] = "Mars/Olympus" }, "INSTITUTION_TIMEZONE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := validEnv()
			tt.mutate(m)
			_, err := Load(env(m))
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestJustificationAndStorageConfig(t *testing.T) {
	cfg, err := Load(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JustificationEvidenceRequired || cfg.MaxEvidenceMB != 10 || cfg.StorageDir == "" {
		t.Fatalf("defaults: %+v", cfg)
	}
	abs, _ := filepath.Abs(".")
	prodBase := func() map[string]string {
		m := validEnv()
		m["APP_ENV"], m["ALLOWED_NETWORKS"] = "production", "192.0.2.0/24"
		return m
	}
	m := prodBase()
	if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "STORAGE_DIR") {
		t.Fatalf("production without STORAGE_DIR must fail, got %v", err)
	}
	m["STORAGE_DIR"] = "relative/dir"
	if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "STORAGE_DIR") {
		t.Fatalf("production with a relative STORAGE_DIR must fail, got %v", err)
	}
	m["STORAGE_DIR"] = abs
	if _, err := Load(env(m)); err != nil {
		t.Fatalf("production with an absolute STORAGE_DIR: %v", err)
	}
	bad := validEnv()
	bad["JUSTIFICATION_EVIDENCE_REQUIRED"] = "sometimes"
	if _, err := Load(env(bad)); err == nil {
		t.Fatal("invalid boolean must fail")
	}
	ok := validEnv()
	ok["JUSTIFICATION_EVIDENCE_REQUIRED"] = "true"
	if c, err := Load(env(ok)); err != nil || !c.JustificationEvidenceRequired {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestCORSOriginsConfig(t *testing.T) {
	m := validEnv()
	m["CORS_ALLOWED_ORIGINS"] = "https://app.example.edu, http://localhost:5173"
	cfg, err := Load(env(m))
	if err != nil || len(cfg.CORSAllowedOrigins) != 2 || cfg.CORSAllowedOrigins[0] != "https://app.example.edu" {
		t.Fatalf("origins: %v %v", cfg.CORSAllowedOrigins, err)
	}
	if cfg, _ := Load(env(validEnv())); len(cfg.CORSAllowedOrigins) != 0 {
		t.Fatal("CORS must be off by default")
	}
	for _, bad := range []string{"*", "https://app.example.edu/", "app.example.edu", "https://a.edu/x"} {
		m := validEnv()
		m["CORS_ALLOWED_ORIGINS"] = bad
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "CORS_ALLOWED_ORIGINS") {
			t.Errorf("%q should be rejected, got %v", bad, err)
		}
	}
}

func TestProductionRejectsExampleSecrets(t *testing.T) {
	prod := func() map[string]string {
		m := validEnv()
		m["APP_ENV"] = "production"
		m["ALLOWED_NETWORKS"] = "192.0.2.0/24"
		m["STORAGE_DIR"] = filepath.Join(t.TempDir(), "evidence")
		return m
	}
	if _, err := Load(env(prod())); err != nil {
		t.Fatalf("a real-looking production config must load: %v", err)
	}
	cases := map[string]func(map[string]string){
		"change-me secret":   func(m map[string]string) { m["JWT_SECRET"] = "change-me-change-me-change-me-change-me" },
		"repeated character": func(m map[string]string) { m["JWT_SECRET"] = strings.Repeat("a", 40) },
		"dev database secret": func(m map[string]string) {
			m["DATABASE_URL"] = "postgres://edutrak:edutrak_dev_password@db:5432/edutrak"
		},
		"plain http origin": func(m map[string]string) { m["CORS_ALLOWED_ORIGINS"] = "http://app.example.edu" },
	}
	for name, mutate := range cases {
		m := prod()
		mutate(m)
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: production must refuse it", name)
		}
	}
	// The same values stay acceptable in development.
	dev := validEnv()
	dev["JWT_SECRET"] = "change-me-change-me-change-me-change-me"
	dev["CORS_ALLOWED_ORIGINS"] = "http://localhost:5173"
	if _, err := Load(env(dev)); err != nil {
		t.Fatalf("development must accept them: %v", err)
	}
}
