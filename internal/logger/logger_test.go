package logger

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	for level, want := range map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError, "WARN": slog.LevelWarn} {
		log, err := New(io.Discard, "json", level)
		if err != nil {
			t.Fatalf("%s: %v", level, err)
		}
		if !log.Enabled(context.Background(), want) || (want > slog.LevelDebug && log.Enabled(context.Background(), want-4)) {
			t.Errorf("%s: wrong level", level)
		}
	}
	if _, err := New(io.Discard, "text", "bogus"); err == nil || !strings.Contains(err.Error(), "expected debug, info, warn or error") {
		t.Errorf("invalid level: %v", err)
	}
	if _, err := New(io.Discard, "xml", "info"); err == nil {
		t.Error("expected an error for an invalid format")
	}
}
