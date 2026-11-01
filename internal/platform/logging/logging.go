// Package logging builds the structured logger. Callers must never log
// passwords, tokens or QR secrets.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

func New(w io.Writer, env, level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.ToUpper(level))); err != nil {
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if env == "production" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
