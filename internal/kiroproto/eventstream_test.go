package kiroproto

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"hash/crc32"
	"io"
	"testing"

	tu "github.com/d-kuro/kirocc/internal/testutil"
)

// crc32Test is the CRC-32 (IEEE) table for test frame building.
var crc32Test = crc32.IEEETable

func TestParseStream_SingleEvents(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		payload   any
		check     func(t *testing.T, e Event)
	}{
		{
			name:      "assistantResponseEvent",
			eventType: "assistantResponseEvent",
			payload:   map[string]string{"content": "Hello, world!"},
			check: func(t *testing.T, e Event) {
				if e.Type != "assistantResponseEvent" {
					t.Errorf("Type = %q", e.Type)
				}
				if e.Content != "Hello, world!" {
					t.Errorf("Content = %q", e.Content)
				}
			},
		},
		{
			name:      "reasoningContentEvent",
			eventType: "reasoningContentEvent",
			payload:   map[string]string{"text": "I am thinking..."},
			check: func(t *testing.T, e Event) {
				if e.ThinkingText != "I am thinking..." {
					t.Errorf("ThinkingText = %q", e.ThinkingText)
				}
			},
		},
		{
			name:      "meteringEvent",
			eventType: "meteringEvent",
			payload:   map[string]any{"usage": 1.5, "inputTokens": 100, "outputTokens": 50},
			check: func(t *testing.T, e Event) {
				if e.Credits != 1.5 {
					t.Errorf("Credits = %v", e.Credits)
				}
				if e.InputTokens != 100 {
					t.Errorf("InputTokens = %d", e.InputTokens)
				}
				if e.OutputTokens != 50 {
					t.Errorf("OutputTokens = %d", e.OutputTokens)
				}
			},
		},
		{
			name:      "metadataEvent",
			eventType: "metadataEvent",
			payload: map[string]any{
				"tokenUsage": map[string]any{
					"uncachedInputTokens":  200,
					"outputTokens":         80,
					"totalTokens":          300,
					"cacheReadInputTokens": 20,
				},
			},
			check: func(t *testing.T, e Event) {
				if e.Type != "metadataEvent" {
					t.Errorf("Type = %q", e.Type)
				}
				if e.UncachedInputTokens != 200 {
					t.Errorf("UncachedInputTokens = %d", e.UncachedInputTokens)
				}
				if e.CacheReadInputTokens != 20 {
					t.Errorf("CacheReadInputTokens = %d", e.CacheReadInputTokens)
				}
				if e.OutputTokens != 80 {
					t.Errorf("OutputTokens = %d", e.OutputTokens)
				}
				if e.TotalTokens != 300 {
					t.Errorf("TotalTokens = %d", e.TotalTokens)
				}
				if e.InputTokens != 220 {
					t.Errorf("InputTokens = %d, want 220 (uncached+cacheRead)", e.InputTokens)
				}
			},
		},
		{
			name:      "invalidStateEvent",
			eventType: "invalidStateEvent",
			payload:   map[string]string{"reason": "MONTHLY_REQUEST_COUNT", "message": "Monthly limit exceeded"},
			check: func(t *testing.T, e Event) {
				if e.Type != "invalidStateEvent" {
					t.Errorf("Type = %q", e.Type)
				}
				if e.InvalidStateReason != "MONTHLY_REQUEST_COUNT" {
					t.Errorf("InvalidStateReason = %q", e.InvalidStateReason)
				}
				if e.ErrorMessage != "Monthly limit exceeded" {
					t.Errorf("ErrorMessage = %q", e.ErrorMessage)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, _ := json.Marshal(tt.payload)
			frame := tu.BuildFrame(tt.eventType, payload)

			var events []Event
			err := ParseStream(context.Background(), bytes.NewReader(frame), func(e Event) bool {
				events = append(events, e)
				return false
			})
			if err != nil {
				t.Fatalf("ParseStream error: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("expected 1 event, got %d", len(events))
			}
			tt.check(t, events[0])
		})
	}
}

func TestParseStream_ExceptionFrame(t *testing.T) {
	buildExceptionFrame := func(exType string, payload []byte) []byte {
		addHeader := func(buf []byte, name string, value string) []byte {
			nameB := []byte(name)
			valueB := []byte(value)
			buf = append(buf, byte(len(nameB)))
			buf = append(buf, nameB...)
			buf = append(buf, 7) // string type
			buf = append(buf, byte(len(valueB)>>8), byte(len(valueB)))
			buf = append(buf, valueB...)
			return buf
		}
		var headers []byte
		headers = addHeader(headers, ":message-type", "exception")
		headers = addHeader(headers, ":exception-type", exType)
		return tu.AssembleFrame(headers, payload)
	}

	payload, _ := json.Marshal(map[string]string{"message": "internal error"})
	frame := buildExceptionFrame("InternalServerError", payload)

	var events []Event
	err := ParseStream(context.Background(), bytes.NewReader(frame), func(e Event) bool {
		events = append(events, e)
		return false
	})
	if err != nil {
		t.Fatalf("ParseStream error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	e := events[0]
	if e.Type != "exception" {
		t.Errorf("Type = %q, want exception", e.Type)
	}
	if e.ErrorMessage != "internal error" {
		t.Errorf("ErrorMessage = %q", e.ErrorMessage)
	}
}

func TestParseStream_MultipleFrames(t *testing.T) {
	p1, _ := json.Marshal(map[string]string{"content": "Hello"})
	p2, _ := json.Marshal(map[string]string{"content": " world"})

	var buf bytes.Buffer
	buf.Write(tu.BuildFrame("assistantResponseEvent", p1))
	buf.Write(tu.BuildFrame("assistantResponseEvent", p2))

	var events []Event
	err := ParseStream(context.Background(), &buf, func(e Event) bool {
		events = append(events, e)
		return false
	})
	if err != nil {
		t.Fatalf("ParseStream error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Content != "Hello" {
		t.Errorf("events[0].Content = %q", events[0].Content)
	}
	if events[1].Content != " world" {
		t.Errorf("events[1].Content = %q", events[1].Content)
	}
}

func TestParseStream_VariousHeaderTypes(t *testing.T) {
	var extra []byte

	appendHeader := func(name string, typeID byte, value []byte) {
		extra = append(extra, byte(len(name)))
		extra = append(extra, []byte(name)...)
		extra = append(extra, typeID)
		extra = append(extra, value...)
	}

	appendHeader("h-bool-true", 0, nil)
	appendHeader("h-bool-false", 1, nil)
	appendHeader("h-byte", 2, []byte{0x42})
	appendHeader("h-short", 3, []byte{0x00, 0x01})
	appendHeader("h-int", 4, []byte{0x00, 0x00, 0x00, 0x01})
	appendHeader("h-long", 5, []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01})
	appendHeader("h-bytes", 6, []byte{0x00, 0x03, 0xAA, 0xBB, 0xCC})
	appendHeader("h-str", 7, []byte{0x00, 0x03, 'f', 'o', 'o'})
	appendHeader("h-ts", 8, []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01})
	appendHeader("h-uuid", 9, make([]byte, 16))

	payload, _ := json.Marshal(map[string]string{"content": "ok"})
	frame := tu.BuildFrameWithExtraHeaders(extra, "assistantResponseEvent", payload)

	var events []Event
	err := ParseStream(context.Background(), bytes.NewReader(frame), func(e Event) bool {
		events = append(events, e)
		return false
	})
	if err != nil {
		t.Fatalf("ParseStream error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Content != "ok" {
		t.Errorf("Content = %q", events[0].Content)
	}
}

func TestReadFrame(t *testing.T) {
	t.Run("valid frame", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]string{"content": "hello"})
		frame := tu.BuildFrame("assistantResponseEvent", payload)
		br := bufio.NewReader(bytes.NewReader(frame))
		headers, body, err := readFrame(br)
		if err != nil {
			t.Fatalf("readFrame error: %v", err)
		}
		if len(headers) == 0 {
			t.Fatal("expected headers")
		}
		if string(body) != string(payload) {
			t.Fatalf("body = %q, want %q", body, payload)
		}
	})

	t.Run("EOF", func(t *testing.T) {
		br := bufio.NewReader(bytes.NewReader(nil))
		_, _, err := readFrame(br)
		if err != io.EOF {
			t.Fatalf("expected io.EOF, got %v", err)
		}
	})

	t.Run("frame too small", func(t *testing.T) {
		var prelude [8]byte
		binary.BigEndian.PutUint32(prelude[0:4], 10) // totalLen = 10 < 16
		binary.BigEndian.PutUint32(prelude[4:8], 0)
		preludeCRC := crc32.Checksum(prelude[:], crc32Test)

		var buf [12]byte
		copy(buf[:8], prelude[:])
		binary.BigEndian.PutUint32(buf[8:12], preludeCRC)

		br := bufio.NewReader(bytes.NewReader(buf[:]))
		_, _, err := readFrame(br)
		if err == nil {
			t.Fatal("expected error for small frame")
		}
	})
}

func TestParseStream_ToolUseEvent(t *testing.T) {
	t.Run("string input accumulation", func(t *testing.T) {
		p1, _ := json.Marshal(map[string]string{"name": "bash"})
		p2, _ := json.Marshal(map[string]string{"input": `{"cmd`})
		p3, _ := json.Marshal(map[string]string{"input": `{"cmd": "ls"}`})
		p4, _ := json.Marshal(map[string]any{"stop": true})

		var buf bytes.Buffer
		buf.Write(tu.BuildFrame("toolUseEvent", p1))
		buf.Write(tu.BuildFrame("toolUseEvent", p2))
		buf.Write(tu.BuildFrame("toolUseEvent", p3))
		buf.Write(tu.BuildFrame("toolUseEvent", p4))

		var events []Event
		err := ParseStream(context.Background(), &buf, func(e Event) bool {
			events = append(events, e)
			return false
		})
		if err != nil {
			t.Fatalf("ParseStream error: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 event (on stop), got %d", len(events))
		}
		e := events[0]
		if e.Type != "toolUseEvent" {
			t.Errorf("Type = %q", e.Type)
		}
		if e.ToolName != "bash" {
			t.Errorf("ToolName = %q", e.ToolName)
		}
		if !e.ToolStop {
			t.Error("ToolStop should be true")
		}
		if e.ToolUseID == "" {
			t.Error("ToolUseID should not be empty")
		}
	})

	t.Run("object input", func(t *testing.T) {
		p1, _ := json.Marshal(map[string]string{"name": "read_file"})
		inputObj := map[string]string{"path": "/tmp/test.txt"}
		inputJSON, _ := json.Marshal(inputObj)
		p2, _ := json.Marshal(map[string]jsontext.Value{"input": inputJSON})
		p3, _ := json.Marshal(map[string]any{"stop": true})

		var buf bytes.Buffer
		buf.Write(tu.BuildFrame("toolUseEvent", p1))
		buf.Write(tu.BuildFrame("toolUseEvent", p2))
		buf.Write(tu.BuildFrame("toolUseEvent", p3))

		var events []Event
		err := ParseStream(context.Background(), &buf, func(e Event) bool {
			events = append(events, e)
			return false
		})
		if err != nil {
			t.Fatalf("ParseStream error: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}
		if events[0].ToolInput != string(inputJSON) {
			t.Errorf("ToolInput = %q, want %q", events[0].ToolInput, string(inputJSON))
		}
	})

	t.Run("API-provided ID", func(t *testing.T) {
		p1, _ := json.Marshal(map[string]string{"name": "bash", "toolUseId": "api-provided-id"})
		p2, _ := json.Marshal(map[string]any{"stop": true})

		var buf bytes.Buffer
		buf.Write(tu.BuildFrame("toolUseEvent", p1))
		buf.Write(tu.BuildFrame("toolUseEvent", p2))

		var events []Event
		err := ParseStream(context.Background(), &buf, func(e Event) bool {
			events = append(events, e)
			return false
		})
		if err != nil {
			t.Fatalf("ParseStream error: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}
		if events[0].ToolUseID != "api-provided-id" {
			t.Errorf("ToolUseID = %q", events[0].ToolUseID)
		}
	})
}

func TestParseStream_ToolUseEvent_MissingStop(t *testing.T) {
	t.Run("EOF flushes tool without stop frame", func(t *testing.T) {
		// Simulate ExitPlanMode: name+id frame, then input frame, then EOF (no stop).
		p1, _ := json.Marshal(map[string]string{"name": "ExitPlanMode", "toolUseId": "tool-1"})
		p2, _ := json.Marshal(map[string]string{"input": "", "name": "ExitPlanMode", "toolUseId": "tool-1"})

		var buf bytes.Buffer
		buf.Write(tu.BuildFrame("toolUseEvent", p1))
		buf.Write(tu.BuildFrame("toolUseEvent", p2))

		var events []Event
		err := ParseStream(context.Background(), &buf, func(e Event) bool {
			events = append(events, e)
			return false
		})
		if err != nil {
			t.Fatalf("ParseStream error: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 flushed event, got %d", len(events))
		}
		if events[0].ToolName != "ExitPlanMode" || events[0].ToolUseID != "tool-1" || !events[0].ToolStop {
			t.Fatalf("event = %+v", events[0])
		}
	})

	t.Run("no flush when accumulator is empty", func(t *testing.T) {
		var buf bytes.Buffer // empty stream
		var events []Event
		err := ParseStream(context.Background(), &buf, func(e Event) bool {
			events = append(events, e)
			return false
		})
		if err != nil {
			t.Fatalf("ParseStream error: %v", err)
		}
		if len(events) != 0 {
			t.Fatalf("expected 0 events, got %d", len(events))
		}
	})
}

func TestToolUseAccumulator(t *testing.T) {
	t.Run("new tool", func(t *testing.T) {
		acc := &toolUseAccumulator{}
		raw := map[string]jsontext.Value{
			"name":      jsontext.Value(`"bash"`),
			"toolUseId": jsontext.Value(`"id-1"`),
		}
		acc.update(raw)
		if acc.toolUseID != "id-1" || acc.toolName != "bash" {
			t.Fatalf("acc = name:%q id:%q", acc.toolName, acc.toolUseID)
		}
	})

	t.Run("input accumulation", func(t *testing.T) {
		acc := &toolUseAccumulator{toolUseID: "id-1", toolName: "bash"}
		acc.update(map[string]jsontext.Value{"input": jsontext.Value(`"chunk1"`)})
		acc.update(map[string]jsontext.Value{"input": jsontext.Value(`"chunk2"`)})
		if acc.toolInput.String() != "chunk1chunk2" {
			t.Fatalf("toolInput = %q", acc.toolInput.String())
		}
	})

	t.Run("object input", func(t *testing.T) {
		acc := &toolUseAccumulator{toolUseID: "id-1", toolName: "bash"}
		acc.update(map[string]jsontext.Value{"input": jsontext.Value(`{"key":"val"}`)})
		if acc.toolInput.String() != `{"key":"val"}` {
			t.Fatalf("toolInput = %q", acc.toolInput.String())
		}
	})

	t.Run("new tool resets name", func(t *testing.T) {
		acc := &toolUseAccumulator{}
		acc.update(map[string]jsontext.Value{
			"name":      jsontext.Value(`"toolA"`),
			"toolUseId": jsontext.Value(`"id-A"`),
		})
		acc.update(map[string]jsontext.Value{"stop": jsontext.Value(`true`)})

		acc.update(map[string]jsontext.Value{"toolUseId": jsontext.Value(`"id-B"`)})
		if acc.toolName != "" {
			t.Fatalf("toolName should be empty after new tool without name, got %q", acc.toolName)
		}

		acc.update(map[string]jsontext.Value{"name": jsontext.Value(`"toolB"`)})
		if acc.toolName != "toolB" {
			t.Fatalf("toolName = %q, want toolB", acc.toolName)
		}
	})

	t.Run("stop resets accumulator", func(t *testing.T) {
		acc := &toolUseAccumulator{toolUseID: "id-1", toolName: "bash"}
		acc.toolInput.WriteString(`{"cmd":"ls"}`)
		events := acc.update(map[string]jsontext.Value{"stop": jsontext.Value(`true`)})
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}
		event := events[0]
		if event.ToolName != "bash" || event.ToolUseID != "id-1" || event.ToolInput != `{"cmd":"ls"}` {
			t.Fatalf("event = %+v", event)
		}
		if acc.toolName != "" || acc.toolUseID != "" || acc.toolInput.String() != "" {
			t.Fatal("accumulator should be reset after stop")
		}
	})

	t.Run("new tool flushes previous without stop", func(t *testing.T) {
		acc := &toolUseAccumulator{}
		// Start tool A without stop.
		acc.update(map[string]jsontext.Value{
			"name":      jsontext.Value(`"toolA"`),
			"toolUseId": jsontext.Value(`"id-A"`),
		})
		acc.update(map[string]jsontext.Value{"input": jsontext.Value(`""`)})
		// Start tool B — should flush tool A.
		events := acc.update(map[string]jsontext.Value{
			"name":      jsontext.Value(`"toolB"`),
			"toolUseId": jsontext.Value(`"id-B"`),
		})
		if len(events) != 1 {
			t.Fatalf("expected 1 flushed event, got %d", len(events))
		}
		if events[0].ToolName != "toolA" || events[0].ToolUseID != "id-A" {
			t.Fatalf("flushed event = %+v", events[0])
		}
	})
}
