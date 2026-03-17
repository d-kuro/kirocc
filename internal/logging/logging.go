package logging

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/lmittmann/tint"
	"gopkg.in/natefinch/lumberjack.v2"
)

type traceIDKey struct{}

// NewTraceID generates a new UUID v4 trace ID.
func NewTraceID() string {
	return uuid.New().String()
}

// ShortTraceID returns the first 8 characters of a trace ID.
// If the ID is shorter than 8 characters, it is returned as-is.
func ShortTraceID(id string) string {
	if len(id) < 8 {
		return id
	}
	return id[:8]
}

// WithTraceID stores a trace ID in the context.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, id)
}

// TraceIDFromContext retrieves the trace ID from the context.
// Returns "" if not set.
func TraceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(traceIDKey{}).(string)
	return id
}

// OTelTraceID converts a UUID trace ID to OTel format (32 hex chars, no hyphens).
func OTelTraceID(id string) string {
	return strings.ReplaceAll(id, "-", "")
}

// sensitiveHeaders lists header names whose values must be redacted in logs.
var sensitiveHeaders = map[string]bool{
	"Authorization":        true,
	"Proxy-Authorization":  true,
	"X-Api-Key":            true,
	"Cookie":               true,
	"Set-Cookie":           true,
	"X-Amz-Security-Token": true,
}

// IsSensitiveHeader returns true if the canonicalized header name should be redacted.
func IsSensitiveHeader(name string) bool {
	if sensitiveHeaders[name] {
		return true
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, "-token") || strings.HasSuffix(lower, "-secret")
}

// SafeHeaders wraps http.Header as a slog.LogValuer so that sanitization
// (map allocation + string joins) is deferred until the log record is
// actually emitted. When debug logging is disabled, zero work is done.
type SafeHeaders struct{ H http.Header }

// LogValue implements slog.LogValuer.
func (s SafeHeaders) LogValue() slog.Value {
	attrs := make([]slog.Attr, 0, len(s.H))
	for k, vs := range s.H {
		if IsSensitiveHeader(k) {
			attrs = append(attrs, slog.String(k, "[REDACTED]"))
		} else {
			attrs = append(attrs, slog.String(k, strings.Join(vs, ", ")))
		}
	}
	return slog.GroupValue(attrs...)
}

const (
	DefaultLogMaxSize    = 10 // MB - small enough for coding agents to read
	DefaultLogMaxBackups = 5
	DefaultLogMaxAge     = 7 // days
)

// LogFileConfig configures optional file logging with rotation.
// Zero values for MaxSize, MaxBackups, and MaxAge fall back to package defaults.
type LogFileConfig struct {
	Path       string
	MaxSize    int // megabytes
	MaxBackups int
	MaxAge     int // days
	Compress   bool
}

// NewHandler creates a slog handler. When logCfg.Path is set, it returns a
// MultiHandler that writes to both the console and a rotating log file.
// The returned io.Closer must be called on shutdown to flush the log file.
func NewHandler(debug bool, logCfg LogFileConfig) (slog.Handler, io.Closer) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	var consoleHandler slog.Handler
	if debug {
		consoleHandler = NewOTelHandler(os.Stderr, level)
	} else {
		consoleHandler = tint.NewHandler(os.Stderr, &tint.Options{
			Level:      level,
			TimeFormat: "2006-01-02 15:04:05",
		})
	}

	if logCfg.Path == "" {
		return consoleHandler, nopCloser{}
	}

	maxSize := logCfg.MaxSize
	if maxSize == 0 {
		maxSize = DefaultLogMaxSize
	}
	maxBackups := logCfg.MaxBackups
	if maxBackups == 0 {
		maxBackups = DefaultLogMaxBackups
	}
	maxAge := logCfg.MaxAge
	if maxAge == 0 {
		maxAge = DefaultLogMaxAge
	}

	lj := &lumberjack.Logger{
		Filename:   logCfg.Path,
		MaxSize:    maxSize,
		MaxBackups: maxBackups,
		MaxAge:     maxAge,
		Compress:   logCfg.Compress,
	}
	fileHandler := NewOTelHandler(lj, level)

	return slog.NewMultiHandler(consoleHandler, fileHandler), lj
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
