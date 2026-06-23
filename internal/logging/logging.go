// Package logging provides structured logging for Sentinel.
//
// v3.4.0: Replaces the stdlib log.Printf("[component] message") pattern with
// log/slog structured JSON logging. This enables:
//   - Level filtering (Debug/Info/Warn/Error)
//   - Structured fields (incident_id, service, namespace, etc.)
//   - JSON output for ingestion by Datadog/Loki/ELK
//   - Correlation of log lines by incident ID across components
//
// The package provides a global default logger (JSON handler to stdout) and
// context-aware helpers that attach incident IDs to log lines.
//
// Migration pattern:
//
//	// OLD
//	log.Printf("[investigation] %s/%s — service=%s", crNamespace, crName, cluster.Service)
//
//	// NEW
//	logging.Info("investigation started",
//	    "namespace", crNamespace,
//	    "cr_name", crName,
//	    "service", cluster.Service,
//	)
//
// Or with context (for incident_id correlation):
//
//	ctx = logging.WithIncidentID(ctx, incident.IncidentID.String())
//	logging.InfoCtx(ctx, "pipeline started")
//	// → {"level":"INFO","msg":"pipeline started","incident_id":"abc-123"}
package logging

import (
	"context"
	"log/slog"
	"os"
	"sync"
)

// contextKey is unexported so callers can't construct their own keys.
type contextKey int

const (
	incidentIDKey contextKey = iota
	componentKey
)

var (
	defaultLogger *slog.Logger
	once          sync.Once
)

// Init initializes the default logger. Safe to call multiple times — only the
// first call takes effect. Subsequent calls are no-ops.
//
// If level is empty, defaults to INFO. Set SENTINEL_LOG_LEVEL=DEBUG for verbose.
func Init(level string) {
	once.Do(func() {
		var lvl slog.Level
		switch level {
		case "DEBUG", "debug":
			lvl = slog.LevelDebug
		case "WARN", "warn":
			lvl = slog.LevelWarn
		case "ERROR", "error":
			lvl = slog.LevelError
		default:
			lvl = slog.LevelInfo
		}
		handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level:     lvl,
			AddSource: false, // source is noisy in Go's slog format
		})
		defaultLogger = slog.New(handler)
		slog.SetDefault(defaultLogger)
	})
}

// L returns the default logger. Initializes with INFO level if not yet initialized.
func L() *slog.Logger {
	if defaultLogger == nil {
		Init("")
	}
	return defaultLogger
}

// WithIncidentID attaches an incident ID to the context. All log calls using
// this context will include "incident_id" as a field.
func WithIncidentID(ctx context.Context, incidentID string) context.Context {
	return context.WithValue(ctx, incidentIDKey, incidentID)
}

// WithComponent attaches a component name to the context. All log calls using
// this context will include "component" as a field.
func WithComponent(ctx context.Context, component string) context.Context {
	return context.WithValue(ctx, componentKey, component)
}

// incidentIDFrom extracts the incident ID from context, if present.
func incidentIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(incidentIDKey).(string); ok {
		return v
	}
	return ""
}

func componentFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(componentKey).(string); ok {
		return v
	}
	return ""
}

// argsFromCtx builds slog args for fields stored in the context.
func argsFromCtx(ctx context.Context) []any {
	var args []any
	if id := incidentIDFrom(ctx); id != "" {
		args = append(args, "incident_id", id)
	}
	if c := componentFrom(ctx); c != "" {
		args = append(args, "component", c)
	}
	return args
}

// Convenience functions — these mirror slog's API but auto-attach context fields.

func Debug(msg string, args ...any) { L().Debug(msg, args...) }
func Info(msg string, args ...any)  { L().Info(msg, args...) }
func Warn(msg string, args ...any)  { L().Warn(msg, args...) }
func Error(msg string, args ...any) { L().Error(msg, args...) }

// DebugCtx / InfoCtx / WarnCtx / ErrorCtx include context-derived fields.
func DebugCtx(ctx context.Context, msg string, args ...any) {
	L().Debug(msg, append(argsFromCtx(ctx), args...)...)
}
func InfoCtx(ctx context.Context, msg string, args ...any) {
	L().Info(msg, append(argsFromCtx(ctx), args...)...)
}
func WarnCtx(ctx context.Context, msg string, args ...any) {
	L().Warn(msg, append(argsFromCtx(ctx), args...)...)
}
func ErrorCtx(ctx context.Context, msg string, args ...any) {
	L().Error(msg, append(argsFromCtx(ctx), args...)...)
}
