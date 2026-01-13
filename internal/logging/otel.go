package logging

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// otelHandler is a slog.Handler that outputs OTel-style JSON Lines.
type otelHandler struct {
	w     io.Writer
	mu    *sync.Mutex
	attrs []slog.Attr
	group string
}

// NewOTelHandler creates an OTel-style JSON Lines slog handler.
func NewOTelHandler(w io.Writer) slog.Handler {
	return &otelHandler{
		w:  w,
		mu: &sync.Mutex{},
	}
}

func (h *otelHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelDebug
}

func (h *otelHandler) Handle(ctx context.Context, r slog.Record) error {
	rec := map[string]any{
		"timestamp":      r.Time.UTC().Format(time.RFC3339Nano),
		"severityNumber": otelSeverityNumber(r.Level),
		"severityText":   otelSeverityText(r.Level),
		"body":           r.Message,
	}

	attrs := make(map[string]any)
	for _, a := range h.attrs {
		attrs[h.attrKey(a.Key)] = resolveAttrValue(a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[h.attrKey(a.Key)] = resolveAttrValue(a.Value)
		return true
	})
	if len(attrs) > 0 {
		rec["attributes"] = attrs
	}

	if traceID := TraceIDFromContext(ctx); traceID != "" {
		rec["traceId"] = OTelTraceID(traceID)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal otel log: %w", err)
	}
	b = append(b, '\n')
	_, err = h.w.Write(b)
	return err
}

func (h *otelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs), len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	newAttrs = append(newAttrs, attrs...)
	return &otelHandler{
		w:     h.w,
		mu:    h.mu,
		attrs: newAttrs,
		group: h.group,
	}
}

func (h *otelHandler) WithGroup(name string) slog.Handler {
	newGroup := name
	if h.group != "" {
		newGroup = h.group + "." + name
	}
	return &otelHandler{
		w:     h.w,
		mu:    h.mu,
		attrs: h.attrs,
		group: newGroup,
	}
}

func (h *otelHandler) attrKey(key string) string {
	if h.group != "" {
		return h.group + "." + key
	}
	return key
}

func resolveAttrValue(v slog.Value) any {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().UTC().Format(time.RFC3339Nano)
	case slog.KindGroup:
		attrs := v.Group()
		m := make(map[string]any, len(attrs))
		for _, a := range attrs {
			m[a.Key] = resolveAttrValue(a.Value)
		}
		return m
	case slog.KindAny:
		a := v.Any()
		if raw, ok := a.(jsontext.Value); ok {
			var parsed any
			if json.Unmarshal(raw, &parsed) == nil {
				return parsed
			}
			return string(raw)
		}
		if err, ok := a.(error); ok {
			return err.Error()
		}
		if m, ok := a.(map[string]any); ok {
			return m
		}
		if s, ok := a.([]any); ok {
			return s
		}
		return fmt.Sprintf("%v", a)
	default:
		return v.String()
	}
}

func otelSeverityNumber(level slog.Level) int {
	switch {
	case level < slog.LevelInfo:
		return 5 // DEBUG
	case level < slog.LevelWarn:
		return 9 // INFO
	case level < slog.LevelError:
		return 13 // WARN
	default:
		return 17 // ERROR
	}
}

func otelSeverityText(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return "DEBUG"
	case level < slog.LevelWarn:
		return "INFO"
	case level < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}
