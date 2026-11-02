// Package config loads and validates process configuration from environment
// variables. Missing critical values make startup fail fast.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // the institution timezone must load on hosts without a zoneinfo database
)

type Config struct {
	Env             string
	LogLevel        string
	HTTPPort        string
	DatabaseURL     string
	JWTSecret       []byte
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	QRTokenTTL      time.Duration
	// AllowedNetworks are the institutional networks from which attendance may be registered.
	AllowedNetworks []netip.Prefix
	// TrustedProxies are the only peers whose X-Forwarded-For header is honoured.
	TrustedProxies []netip.Prefix
	StorageDir     string
	MaxEvidenceMB  int

	// Attendance. AttendanceAllowAnyNetwork disables the network check for QR check-ins and
	// is rejected when APP_ENV=production; an empty ALLOWED_NETWORKS otherwise allows nothing.
	AttendanceAllowAnyNetwork  bool
	AttendanceLateAfterMinutes int // default late threshold copied into each new session
	AttendanceLatesPerAbsence  int // default "N lates = 1 absence" copied into each new session
	InstitutionTimezone        string
	InstitutionLocation        *time.Location // used to date attendance sessions

	// Login throttling (in-memory, per process).
	LoginMaxFailuresPerAccount int // failures per (account, client IP) per window
	LoginMaxFailuresPerIP      int // failures per client IP per window
	LoginRateWindow            time.Duration
}

const minJWTSecretBytes = 32

// Load reads configuration using getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	var errs []error
	req := func(key string) string {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", key))
		}
		return v
	}
	opt := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	dur := func(key, def string) time.Duration {
		d, err := time.ParseDuration(opt(key, def))
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be a positive duration (e.g. 15m)", key))
		}
		return d
	}
	nets := func(key string) []netip.Prefix {
		p, err := ParsePrefixes(getenv(key))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
		return p
	}

	cfg := Config{
		Env:             opt("APP_ENV", "development"),
		LogLevel:        opt("LOG_LEVEL", "info"),
		HTTPPort:        opt("HTTP_PORT", "8080"),
		DatabaseURL:     req("DATABASE_URL"),
		AccessTokenTTL:  dur("ACCESS_TOKEN_TTL", "15m"),
		RefreshTokenTTL: dur("REFRESH_TOKEN_TTL", "168h"),
		QRTokenTTL:      dur("QR_TOKEN_TTL", "30s"),
		AllowedNetworks: nets("ALLOWED_NETWORKS"),
		TrustedProxies:  nets("TRUSTED_PROXIES"),
		StorageDir:      opt("STORAGE_DIR", "./data/evidence"),
	}

	if secret := req("JWT_SECRET"); secret != "" {
		if len(secret) < minJWTSecretBytes {
			errs = append(errs, fmt.Errorf("JWT_SECRET must be at least %d bytes", minJWTSecretBytes))
		}
		cfg.JWTSecret = []byte(secret)
	}
	if port, err := strconv.Atoi(cfg.HTTPPort); err != nil || port < 1 || port > 65535 {
		errs = append(errs, errors.New("HTTP_PORT must be a valid port"))
	}
	mb, err := strconv.Atoi(opt("MAX_EVIDENCE_MB", "10"))
	if err != nil || mb < 1 || mb > 10 {
		errs = append(errs, errors.New("MAX_EVIDENCE_MB must be between 1 and 10"))
	}
	cfg.MaxEvidenceMB = mb

	positiveInt := func(key, def string) int {
		n, err := strconv.Atoi(opt(key, def))
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("%s must be a positive integer", key))
		}
		return n
	}
	cfg.LoginMaxFailuresPerAccount = positiveInt("LOGIN_MAX_FAILURES_PER_ACCOUNT", "5")
	// The IP limit is deliberately loose: a whole campus may share one public IP (NAT/proxy).
	cfg.LoginMaxFailuresPerIP = positiveInt("LOGIN_MAX_FAILURES_PER_IP", "150")
	cfg.LoginRateWindow = dur("LOGIN_RATE_WINDOW", "15m")

	cfg.AttendanceLateAfterMinutes = positiveInt("ATTENDANCE_LATE_AFTER_MINUTES", "15")
	cfg.AttendanceLatesPerAbsence = positiveInt("ATTENDANCE_LATES_PER_ABSENCE", "3")
	cfg.InstitutionTimezone = opt("INSTITUTION_TIMEZONE", "America/Mexico_City")
	if loc, err := time.LoadLocation(cfg.InstitutionTimezone); err != nil {
		errs = append(errs, fmt.Errorf("INSTITUTION_TIMEZONE %q is not a valid IANA timezone", cfg.InstitutionTimezone))
	} else {
		cfg.InstitutionLocation = loc
	}
	if cfg.QRTokenTTL < 5*time.Second || cfg.QRTokenTTL > 5*time.Minute {
		errs = append(errs, errors.New("QR_TOKEN_TTL must be between 5s and 5m"))
	}
	allowAny, err := strconv.ParseBool(opt("ATTENDANCE_ALLOW_ANY_NETWORK", "false"))
	if err != nil {
		errs = append(errs, errors.New("ATTENDANCE_ALLOW_ANY_NETWORK must be true or false"))
	}
	cfg.AttendanceAllowAnyNetwork = allowAny
	if cfg.Env == "production" {
		// Production must never fall back to "any network": it needs explicit institutional ranges.
		if allowAny {
			errs = append(errs, errors.New("ATTENDANCE_ALLOW_ANY_NETWORK cannot be enabled when APP_ENV=production"))
		}
		if len(cfg.AllowedNetworks) == 0 {
			errs = append(errs, errors.New("ALLOWED_NETWORKS is required when APP_ENV=production"))
		}
		for _, n := range cfg.AllowedNetworks {
			if n.Bits() == 0 {
				errs = append(errs, fmt.Errorf("ALLOWED_NETWORKS contains %s, which matches every address", n))
			}
		}
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// ParsePrefixes parses a comma-separated list of CIDRs or single IPs.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
			continue
		}
		addr, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("invalid network %q", part)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

// LoadDotEnv sets variables from a KEY=VALUE file without overriding variables
// already present in the environment. A missing file is not an error.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if _, set := os.LookupEnv(key); !set {
			if err := os.Setenv(key, val); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
