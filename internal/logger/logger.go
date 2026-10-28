// Package logger builds the slog logger used across the application.
package logger

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// New returns a logger writing to w in the given format ("text" or "json") at the given level.
func New(w io.Writer, format, level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid log level %s: expected debug, info, warn or error", level)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	switch strings.ToLower(format) {
	case "text", "":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("invalid log format %s: expected text or json", format)
	}
}
