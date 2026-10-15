// Package logx wraps log/slog with project conventions: structured JSON by
// default, level from config, request-scoped fields via context.
package logx

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
)

// Level mirrors slog levels but is configurable from string config.
type Level = slog.Level

var current atomic.Pointer[slog.Logger]

func init() {
	l := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	current.Store(l)
}

// Configure sets the global logger from a string level ("debug","info","warn","error")
// and format ("json"|"text"). Safe to call once at startup.
func Configure(level, format string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl, AddSource: false}
	var h slog.Handler
	if strings.ToLower(format) == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	current.Store(slog.New(h))
	slog.SetDefault(current.Load())
}

// L returns the configured logger.
func L() *slog.Logger { return current.Load() }

// With returns a child logger with the given attributes.
func With(args ...any) *slog.Logger { return L().With(args...) }

// ctx-aware helpers
func Debug(ctx context.Context, msg string, args ...any) { L().DebugContext(ctx, msg, args...) }
func Info(ctx context.Context, msg string, args ...any)  { L().InfoContext(ctx, msg, args...) }
func Warn(ctx context.Context, msg string, args ...any)  { L().WarnContext(ctx, msg, args...) }
func Error(ctx context.Context, msg string, args ...any) { L().ErrorContext(ctx, msg, args...) }
