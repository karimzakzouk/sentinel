// Package logging provides Sentinel's structured logger.
//
// It wraps log/slog with two additions:
//
//   - Secret redaction: any field whose name matches a known sensitive
//     pattern (api_key, token, password, ...) is replaced with
//     "***REDACTED***" before it reaches the handler. This makes it safe
//     for contributors to log config structs without leaking credentials
//     to stdout / Loki / Datadog.
//   - Context fields: incident_id and component can be attached to a
//     context.Context and are automatically added to every log line
//     emitted from that context.
//
// The default logger writes JSON to stdout. The level is configurable
// via Init and can be hot-reloaded at runtime.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// contextKey is unexported so external packages cannot forge context values.
type contextKey int

const (
	incidentIDKey contextKey = iota
	componentKey
)

var (
	mu            sync.Mutex
	defaultLogger *slog.Logger
)

// Init configures the default logger. Safe to call multiple times; useful
// for hot-reloading the log level. An empty level defaults to INFO.
func Init(level string) {
	mu.Lock()
	defer mu.Unlock()
	if defaultLogger == nil {
		defaultLogger = newLogger(level, os.Stdout)
		slog.SetDefault(defaultLogger)
		return
	}
	// Replace the handler on the existing logger so callers holding a
	// reference see the new level.
	defaultLogger = newLogger(level, os.Stdout)
	slog.SetDefault(defaultLogger)
}

// L returns the default logger. If Init was never called, a new INFO-level
// logger is initialized on first call.
func L() *slog.Logger {
	mu.Lock()
	defer mu.Unlock()
	if defaultLogger == nil {
		defaultLogger = newLogger("INFO", os.Stdout)
		slog.SetDefault(defaultLogger)
	}
	return defaultLogger
}

// WithIncidentID returns a context that attaches an incident ID to every
// log line emitted from that context.
func WithIncidentID(ctx context.Context, incidentID string) context.Context {
	return context.WithValue(ctx, incidentIDKey, incidentID)
}

// WithComponent returns a context that attaches a component name to every
// log line emitted from that context.
func WithComponent(ctx context.Context, component string) context.Context {
	return context.WithValue(ctx, componentKey, component)
}

// Debug logs at Debug level using the default logger. Info, Warn, Error
// log at the corresponding level. They mirror slog's variadic API.
func Debug(msg string, args ...any) { L().Debug(msg, args...) }
func Info(msg string, args ...any)  { L().Info(msg, args...) }
func Warn(msg string, args ...any)  { L().Warn(msg, args...) }
func Error(msg string, args ...any) { L().Error(msg, args...) }

// DebugCtx logs at Debug level with context-derived fields. InfoCtx,
// WarnCtx, ErrorCtx behave like their contextless counterparts but also
// extract incident_id / component from ctx.
func DebugCtx(ctx context.Context, msg string, args ...any) {
	L().Debug(msg, withCtx(ctx, args)...)
}
func InfoCtx(ctx context.Context, msg string, args ...any) {
	L().Info(msg, withCtx(ctx, args)...)
}
func WarnCtx(ctx context.Context, msg string, args ...any) {
	L().Warn(msg, withCtx(ctx, args)...)
}
func ErrorCtx(ctx context.Context, msg string, args ...any) {
	L().Error(msg, withCtx(ctx, args)...)
}

func withCtx(ctx context.Context, args []any) []any {
	if ctx == nil {
		return args
	}
	var prefix []any
	if v, ok := ctx.Value(incidentIDKey).(string); ok && v != "" {
		prefix = append(prefix, "incident_id", v)
	}
	if v, ok := ctx.Value(componentKey).(string); ok && v != "" {
		prefix = append(prefix, "component", v)
	}
	if len(prefix) == 0 {
		return args
	}
	return append(prefix, args...)
}

func newLogger(level string, w *os.File) *slog.Logger {
	lvl := parseLevel(level)
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       lvl,
		ReplaceAttr: redactSecrets,
		AddSource:   false,
	})
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// secretPatterns are case-insensitive substrings that mark a field as sensitive.
var secretPatterns = []string{
	"api_key", "apikey", "token", "secret", "password", "passwd", "pwd",
	"private_key", "privatekey", "credential", "webhook", "auth",
	"bearer", "authorization", "cookie",
}

func redactSecrets(_ []string, a slog.Attr) slog.Attr {
	if a.Key == "" {
		return a
	}
	key := strings.ToLower(a.Key)
	for _, pat := range secretPatterns {
		if !strings.Contains(key, pat) {
			continue
		}
		if a.Value.Kind() == slog.KindString && a.Value.String() != "" {
			return slog.String(a.Key, "***REDACTED***")
		}
	}
	return a
}
