package kiroproto

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/d-kuro/kirocc/internal/testutil"
)

func TestParseStream_ErrorFrame(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		message string
		prefix  []byte
	}{
		{name: "headers only", code: "InternalFailure", message: "backend unavailable"},
		{name: "unknown code", code: "UnexpectedBackendError", message: "backend unavailable"},
		{name: "missing message", code: "InternalFailure"},
		{name: "missing code", message: "backend unavailable"},
		{
			name: "unfinished tool call", code: "InternalFailure", message: "backend unavailable",
			prefix: testutil.BuildFrame(EventToolUse, []byte(`{"toolUseId":"pending","name":"lookup","input":"{\"q\":"}`)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames := append([]byte(nil), tt.prefix...)
			frames = append(frames, buildErrorFrame(tt.code, tt.message)...)
			// Even a callback that returns false must not allow events after
			// a terminal error, or flush an unfinished tool call as successful.
			frames = append(frames, testutil.BuildFrame(EventAssistantResponse, []byte(`{"content":"must not be emitted"}`))...)
			var events []Event
			if err := ParseStream(t.Context(), bytes.NewReader(frames), func(e Event) bool {
				events = append(events, e)
				return false
			}); err != nil {
				t.Fatalf("ParseStream: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("events = %+v, want exactly one exception", events)
			}
			wantMessage := tt.message
			if wantMessage == "" {
				wantMessage = "upstream error frame"
			}
			if got := events[0]; got.Type != EventException || got.InvalidStateReason != tt.code || got.ErrorMessage != wantMessage {
				t.Fatalf("event = %+v, want exception with code %q and message %q", got, tt.code, wantMessage)
			}
		})
	}
}

func buildErrorFrame(code, message string) []byte {
	var headers []byte
	for _, h := range [][2]string{{":message-type", "error"}, {":error-code", code}, {":error-message", message}} {
		if h[1] == "" {
			continue
		}
		headers = append(headers, byte(len(h[0])))
		headers = append(headers, h[0]...)
		headers = append(headers, 7)
		headers = binary.BigEndian.AppendUint16(headers, uint16(len(h[1])))
		headers = append(headers, h[1]...)
	}
	return testutil.AssembleFrame(headers, nil)
}
