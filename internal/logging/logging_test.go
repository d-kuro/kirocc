package logging

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"testing"
	"time"
)

func TestShortTraceID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"normal uuid", "550e8400-e29b-41d4-a716-446655440000", "550e8400"},
		{"exactly 8 chars", "abcdefgh", "abcdefgh"},
		{"shorter than 8", "abc", "abc"},
		{"empty", "", ""},
		{"single char", "x", "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShortTraceID(tt.id)
			if got != tt.want {
				t.Errorf("ShortTraceID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestOTelTraceID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"uuid with hyphens", "550e8400-e29b-41d4-a716-446655440000", "550e8400e29b41d4a716446655440000"},
		{"no hyphens", "abcdef1234567890", "abcdef1234567890"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := OTelTraceID(tt.id)
			if got != tt.want {
				t.Errorf("OTelTraceID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestTraceIDContext(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		ctx := context.Background()
		id := "test-trace-id"
		ctx = WithTraceID(ctx, id)
		got := TraceIDFromContext(ctx)
		if got != id {
			t.Errorf("TraceIDFromContext = %q, want %q", got, id)
		}
	})

	t.Run("missing returns empty", func(t *testing.T) {
		got := TraceIDFromContext(context.Background())
		if got != "" {
			t.Errorf("TraceIDFromContext = %q, want empty", got)
		}
	})
}

func TestNewTraceID(t *testing.T) {
	id := NewTraceID()
	if len(id) != 36 { // UUID v4 format: 8-4-4-4-12
		t.Errorf("NewTraceID() length = %d, want 36", len(id))
	}
	// Ensure uniqueness.
	id2 := NewTraceID()
	if id == id2 {
		t.Error("NewTraceID() returned duplicate IDs")
	}
}

func TestOTelHandler_JSONStructure(t *testing.T) {
	var buf bytes.Buffer
	h := NewOTelHandler(&buf)
	logger := slog.New(h)

	logger.Info("test message", "key1", "value1", "key2", 42)

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, buf.String())
	}

	// Check required fields.
	for _, field := range []string{"timestamp", "severityNumber", "severityText", "body"} {
		if _, ok := rec[field]; !ok {
			t.Errorf("missing field %q in JSON output", field)
		}
	}

	if rec["body"] != "test message" {
		t.Errorf("body = %v, want %q", rec["body"], "test message")
	}
	if rec["severityText"] != "INFO" {
		t.Errorf("severityText = %v, want %q", rec["severityText"], "INFO")
	}
	if rec["severityNumber"] != float64(9) {
		t.Errorf("severityNumber = %v, want 9", rec["severityNumber"])
	}

	attrs, ok := rec["attributes"].(map[string]any)
	if !ok {
		t.Fatal("attributes is not a map")
	}
	if attrs["key1"] != "value1" {
		t.Errorf("attrs[key1] = %v, want %q", attrs["key1"], "value1")
	}
	if attrs["key2"] != float64(42) {
		t.Errorf("attrs[key2] = %v, want 42", attrs["key2"])
	}
}

func TestOTelHandler_TraceID(t *testing.T) {
	t.Run("with trace ID in context", func(t *testing.T) {
		var buf bytes.Buffer
		h := NewOTelHandler(&buf)

		ctx := WithTraceID(context.Background(), "550e8400-e29b-41d4-a716-446655440000")
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "test", 0)
		if err := h.Handle(ctx, r); err != nil {
			t.Fatal(err)
		}

		var rec map[string]any
		if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if rec["traceId"] != "550e8400e29b41d4a716446655440000" {
			t.Errorf("traceId = %v, want %q", rec["traceId"], "550e8400e29b41d4a716446655440000")
		}
	})

	t.Run("without trace ID", func(t *testing.T) {
		var buf bytes.Buffer
		h := NewOTelHandler(&buf)

		r := slog.NewRecord(time.Now(), slog.LevelInfo, "test", 0)
		if err := h.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}

		var rec map[string]any
		if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if _, ok := rec["traceId"]; ok {
			t.Error("traceId should be omitted when not set")
		}
	})
}

func TestOTelHandler_AttrTypes(t *testing.T) {
	var buf bytes.Buffer
	h := NewOTelHandler(&buf)

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "types", 0)
	r.AddAttrs(
		slog.String("str", "hello"),
		slog.Int("num", 123),
		slog.Bool("flag", true),
		slog.Float64("pi", 3.14),
		slog.Duration("dur", 5*time.Second),
		slog.Any("err", errors.New("boom")),
	)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	attrs := rec["attributes"].(map[string]any)
	if attrs["str"] != "hello" {
		t.Errorf("str = %v", attrs["str"])
	}
	if attrs["num"] != float64(123) {
		t.Errorf("num = %v", attrs["num"])
	}
	if attrs["flag"] != true {
		t.Errorf("flag = %v", attrs["flag"])
	}
	if attrs["pi"] != 3.14 {
		t.Errorf("pi = %v", attrs["pi"])
	}
	if attrs["dur"] != "5s" {
		t.Errorf("dur = %v", attrs["dur"])
	}
	if attrs["err"] != "boom" {
		t.Errorf("err = %v", attrs["err"])
	}
}

func TestOTelHandler_WithAttrs(t *testing.T) {
	var buf bytes.Buffer
	h := NewOTelHandler(&buf)
	h2 := h.WithAttrs([]slog.Attr{slog.String("service", "test")})

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "msg", 0)
	r.AddAttrs(slog.String("key", "val"))
	if err := h2.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	attrs := rec["attributes"].(map[string]any)
	if attrs["service"] != "test" {
		t.Errorf("service = %v, want %q", attrs["service"], "test")
	}
	if attrs["key"] != "val" {
		t.Errorf("key = %v, want %q", attrs["key"], "val")
	}
}

func TestOTelHandler_WithGroup(t *testing.T) {
	var buf bytes.Buffer
	h := NewOTelHandler(&buf)
	h2 := h.WithGroup("grp")

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "msg", 0)
	r.AddAttrs(slog.String("key", "val"))
	if err := h2.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	attrs := rec["attributes"].(map[string]any)
	if attrs["grp.key"] != "val" {
		t.Errorf("grp.key = %v, want %q", attrs["grp.key"], "val")
	}
}

func TestOTelHandler_SeverityLevels(t *testing.T) {
	tests := []struct {
		level      slog.Level
		wantNumber float64
		wantText   string
	}{
		{slog.LevelDebug, 5, "DEBUG"},
		{slog.LevelInfo, 9, "INFO"},
		{slog.LevelWarn, 13, "WARN"},
		{slog.LevelError, 17, "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.wantText, func(t *testing.T) {
			var buf bytes.Buffer
			h := NewOTelHandler(&buf)

			r := slog.NewRecord(time.Now(), tt.level, "test", 0)
			if err := h.Handle(context.Background(), r); err != nil {
				t.Fatal(err)
			}

			var rec map[string]any
			if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if rec["severityNumber"] != tt.wantNumber {
				t.Errorf("severityNumber = %v, want %v", rec["severityNumber"], tt.wantNumber)
			}
			if rec["severityText"] != tt.wantText {
				t.Errorf("severityText = %v, want %q", rec["severityText"], tt.wantText)
			}
		})
	}
}

func TestOTelHandler_Enabled(t *testing.T) {
	h := NewOTelHandler(&bytes.Buffer{})
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("should be enabled for DEBUG")
	}
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("should be enabled for INFO")
	}
}

func TestOTelHandler_NoAttrsOmitsField(t *testing.T) {
	var buf bytes.Buffer
	h := NewOTelHandler(&buf)

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "no attrs", 0)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := rec["attributes"]; ok {
		t.Error("attributes should be omitted when empty")
	}
}

func TestOTelHandler_JSONTextValue(t *testing.T) {
	t.Run("valid JSON is embedded as structured data", func(t *testing.T) {
		var buf bytes.Buffer
		h := NewOTelHandler(&buf)

		r := slog.NewRecord(time.Now(), slog.LevelWarn, "capture", 0)
		r.AddAttrs(slog.Any("data", jsontext.Value(`{"key":"val","num":42}`)))
		if err := h.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}

		var rec map[string]any
		if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		attrs := rec["attributes"].(map[string]any)
		data, ok := attrs["data"].(map[string]any)
		if !ok {
			t.Fatalf("data should be a map, got %T: %v", attrs["data"], attrs["data"])
		}
		if data["key"] != "val" {
			t.Errorf("data.key = %v, want %q", data["key"], "val")
		}
		if data["num"] != float64(42) {
			t.Errorf("data.num = %v, want 42", data["num"])
		}
	})

	t.Run("invalid JSON falls back to string", func(t *testing.T) {
		var buf bytes.Buffer
		h := NewOTelHandler(&buf)

		r := slog.NewRecord(time.Now(), slog.LevelWarn, "capture", 0)
		r.AddAttrs(slog.Any("bad", jsontext.Value(`{broken`)))
		if err := h.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}

		var rec map[string]any
		if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		attrs := rec["attributes"].(map[string]any)
		if attrs["bad"] != "{broken" {
			t.Errorf("bad = %v, want %q", attrs["bad"], "{broken")
		}
	})
}
