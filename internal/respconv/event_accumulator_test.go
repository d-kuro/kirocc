package respconv

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/kiroproto"
)

func TestAccumulator_TextDelta(t *testing.T) {
	var acc responseAccumulator
	delta := acc.ProcessEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hello"})
	if delta.TextDelta != "Hello" {
		t.Fatalf("TextDelta = %q", delta.TextDelta)
	}
	delta = acc.ProcessEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hello world"})
	if delta.TextDelta != " world" {
		t.Fatalf("TextDelta = %q", delta.TextDelta)
	}
	if !acc.HasText {
		t.Fatal("expected HasText")
	}
}

func TestAccumulator_ThinkingDelta(t *testing.T) {
	var acc responseAccumulator
	delta := acc.ProcessEvent(kiroproto.Event{Type: "reasoningContentEvent", ThinkingText: "Let me", Signature: "sig_1"})
	if delta.ThinkingDelta != "Let me" {
		t.Fatalf("ThinkingDelta = %q", delta.ThinkingDelta)
	}
	if acc.Signature != "sig_1" {
		t.Fatalf("Signature = %q", acc.Signature)
	}
	delta = acc.ProcessEvent(kiroproto.Event{Type: "reasoningContentEvent", ThinkingText: "Let me think"})
	if delta.ThinkingDelta != " think" {
		t.Fatalf("ThinkingDelta = %q", delta.ThinkingDelta)
	}
}

func TestAccumulator_RedactedContent(t *testing.T) {
	var acc responseAccumulator
	delta := acc.ProcessEvent(kiroproto.Event{Type: "reasoningContentEvent", RedactedContent: "base64data"})
	if delta.RedactedContent != "base64data" {
		t.Fatalf("RedactedContent = %q", delta.RedactedContent)
	}
}

func TestAccumulator_ToolUse(t *testing.T) {
	var acc responseAccumulator
	// Non-stop events are ignored.
	delta := acc.ProcessEvent(kiroproto.Event{Type: "toolUseEvent", ToolStop: false})
	if delta.ToolStop {
		t.Fatal("expected no tool stop")
	}
	// Stop event.
	delta = acc.ProcessEvent(kiroproto.Event{
		Type: "toolUseEvent", ToolStop: true,
		ToolUseID: "t1", ToolName: "bash", ToolInput: `{"cmd":"ls"}`,
	})
	if !delta.ToolStop {
		t.Fatal("expected tool stop")
	}
	if !acc.HasToolUse {
		t.Fatal("expected HasToolUse")
	}
	if len(acc.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d", len(acc.ToolCalls))
	}
}

func TestAccumulator_ToolParseError(t *testing.T) {
	var acc responseAccumulator
	acc.ProcessEvent(kiroproto.Event{
		Type: "toolUseEvent", ToolStop: true,
		ToolUseID: "t1", ToolName: "bash", ToolInput: `{invalid`,
	})
	if !acc.ToolParseError {
		t.Fatal("expected ToolParseError")
	}
}

func TestAccumulator_Metadata(t *testing.T) {
	var acc responseAccumulator
	acc.ProcessEvent(kiroproto.Event{
		Type: "metadataEvent", InputTokens: 100, OutputTokens: 50,
		CacheReadInputTokens: 20, CacheWriteInputTokens: 10,
	})
	if !acc.HasMetadata {
		t.Fatal("expected HasMetadata")
	}
	if acc.InputTokens != 100 || acc.OutputTokens != 50 {
		t.Fatalf("tokens = %d/%d", acc.InputTokens, acc.OutputTokens)
	}
	if acc.CacheReadInputTokens != 20 || acc.CacheWriteInputTokens != 10 {
		t.Fatalf("cache = %d/%d", acc.CacheReadInputTokens, acc.CacheWriteInputTokens)
	}
}

func TestAccumulator_MeteringFallback(t *testing.T) {
	var acc responseAccumulator
	acc.ProcessEvent(kiroproto.Event{Type: "meteringEvent", InputTokens: 30, OutputTokens: 10})
	if acc.InputTokens != 30 {
		t.Fatalf("InputTokens = %d", acc.InputTokens)
	}
	// Metadata should override metering.
	acc.ProcessEvent(kiroproto.Event{Type: "metadataEvent", InputTokens: 100, OutputTokens: 50})
	if acc.InputTokens != 100 {
		t.Fatalf("InputTokens = %d after metadata", acc.InputTokens)
	}
	// Subsequent metering should be ignored.
	acc.ProcessEvent(kiroproto.Event{Type: "meteringEvent", InputTokens: 999, OutputTokens: 999})
	if acc.InputTokens != 100 {
		t.Fatalf("InputTokens = %d, metering should be ignored", acc.InputTokens)
	}
}

func TestAccumulator_ConversationID(t *testing.T) {
	var acc responseAccumulator
	acc.ProcessEvent(kiroproto.Event{Type: "messageMetadataEvent", ConversationID: "conv-abc"})
	if acc.ConversationID != "conv-abc" {
		t.Fatalf("ConversationID = %q", acc.ConversationID)
	}
}

func TestAccumulator_ErrorEvent(t *testing.T) {
	var acc responseAccumulator
	delta := acc.ProcessEvent(kiroproto.Event{Type: "invalidStateEvent", ErrorMessage: "bad"})
	if !delta.IsError {
		t.Fatal("expected IsError")
	}
	if delta.ErrorMessage != "bad" {
		t.Fatalf("ErrorMessage = %q", delta.ErrorMessage)
	}
}

func TestAccumulator_ExceptionEvent(t *testing.T) {
	var acc responseAccumulator
	delta := acc.ProcessEvent(kiroproto.Event{Type: "exception", ErrorMessage: "internal error"})
	if !delta.IsError {
		t.Fatal("expected IsError")
	}
}

func TestAccumulator_StopSequence(t *testing.T) {
	tests := []struct {
		name        string
		stopSeqs    []string
		chunks      []string // cumulative content chunks from Kiro
		wantDeltas  []string // expected text deltas emitted
		wantStop    bool
		wantStopSeq string
		wantTextBuf string
	}{
		{
			name:        "exact match in single chunk",
			stopSeqs:    []string{"\n\nHuman:"},
			chunks:      []string{"Hello\n\nHuman: hi"},
			wantDeltas:  []string{"Hello"},
			wantStop:    true,
			wantStopSeq: "\n\nHuman:",
			wantTextBuf: "Hello",
		},
		{
			name:        "cross-chunk boundary",
			stopSeqs:    []string{"\n\nHuman:"},
			chunks:      []string{"Hello\n", "Hello\n\nHuman: hi"},
			wantDeltas:  []string{"Hello"},
			wantStop:    true,
			wantStopSeq: "\n\nHuman:",
			wantTextBuf: "Hello",
		},
		{
			name:        "no match",
			stopSeqs:    []string{"\n\nHuman:"},
			chunks:      []string{"Hello", "Hello world"},
			wantDeltas:  []string{"Hell", "o world"},
			wantStop:    false,
			wantStopSeq: "",
			wantTextBuf: "Hello world",
		},
		{
			name:        "multiple candidates first wins",
			stopSeqs:    []string{"STOP", "END"},
			chunks:      []string{"beforeENDafter"},
			wantDeltas:  []string{"before"},
			wantStop:    true,
			wantStopSeq: "END",
			wantTextBuf: "before",
		},
		{
			name:        "empty stop sequences",
			stopSeqs:    nil,
			chunks:      []string{"Hello", "Hello world"},
			wantDeltas:  []string{"Hello", " world"},
			wantStop:    false,
			wantStopSeq: "",
			wantTextBuf: "Hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var acc responseAccumulator
			acc.initStopSequences(tt.stopSeqs)
			var deltas []string
			var lastStopSignal bool

			for _, chunk := range tt.chunks {
				if lastStopSignal {
					break
				}
				d := acc.ProcessEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: chunk})
				if d.TextDelta != "" {
					deltas = append(deltas, d.TextDelta)
				}
				lastStopSignal = d.StopSignal
			}

			// Flush remaining if no stop.
			if !acc.LocalStop {
				if remaining := acc.flushStopSeqPending(); remaining != "" {
					acc.TextBuf.WriteString(remaining)
					deltas = append(deltas, remaining)
				}
			}

			if len(deltas) != len(tt.wantDeltas) {
				t.Fatalf("deltas = %q, want %q", deltas, tt.wantDeltas)
			}
			for i, d := range deltas {
				if d != tt.wantDeltas[i] {
					t.Fatalf("deltas[%d] = %q, want %q", i, d, tt.wantDeltas[i])
				}
			}
			if acc.LocalStop != tt.wantStop {
				t.Fatalf("LocalStop = %v, want %v", acc.LocalStop, tt.wantStop)
			}
			if acc.StopSequence != tt.wantStopSeq {
				t.Fatalf("StopSequence = %q, want %q", acc.StopSequence, tt.wantStopSeq)
			}
			if acc.TextBuf.String() != tt.wantTextBuf {
				t.Fatalf("TextBuf = %q, want %q", acc.TextBuf.String(), tt.wantTextBuf)
			}
		})
	}
}

func TestAccumulator_MaxTokens(t *testing.T) {
	tests := []struct {
		name       string
		budget     int
		chunks     []string // cumulative content chunks
		wantDeltas []string
		wantStop   bool
		wantReason string
	}{
		{
			name:   "budget reached mid-stream",
			budget: 2, // 2 tokens = 8 runes
			// chunk1: "Hello" (5 runes, 5/4=1 token, ok)
			// chunk2: "Hello world!" (delta=" world!", 7 runes, total=12, 12/4=3 >= 2, stop)
			chunks:     []string{"Hello", "Hello world!"},
			wantDeltas: []string{"Hello", " wo"},
			wantStop:   true,
			wantReason: "max_tokens",
		},
		{
			name:       "budget not reached",
			budget:     100,
			chunks:     []string{"Hello", "Hello world"},
			wantDeltas: []string{"Hello", " world"},
			wantStop:   false,
		},
		{
			name:       "budget zero means no enforcement",
			budget:     0,
			chunks:     []string{"Hello", "Hello world"},
			wantDeltas: []string{"Hello", " world"},
			wantStop:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := responseAccumulator{maxTokensBudget: tt.budget}
			var deltas []string
			for _, chunk := range tt.chunks {
				if acc.LocalStop {
					break
				}
				d := acc.ProcessEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: chunk})
				if d.TextDelta != "" {
					deltas = append(deltas, d.TextDelta)
				}
			}
			if len(deltas) != len(tt.wantDeltas) {
				t.Fatalf("deltas = %q, want %q", deltas, tt.wantDeltas)
			}
			for i, d := range deltas {
				if d != tt.wantDeltas[i] {
					t.Fatalf("deltas[%d] = %q, want %q", i, d, tt.wantDeltas[i])
				}
			}
			if acc.LocalStop != tt.wantStop {
				t.Fatalf("LocalStop = %v, want %v", acc.LocalStop, tt.wantStop)
			}
			if tt.wantReason != "" && acc.StopReason != tt.wantReason {
				t.Fatalf("StopReason = %q, want %q", acc.StopReason, tt.wantReason)
			}
		})
	}
}

func TestAccumulator_MaxTokens_ThinkingCountsTowardBudget(t *testing.T) {
	acc := responseAccumulator{maxTokensBudget: 2} // 2 tokens = 8 runes
	// Thinking: "Think!!!" = 8 runes = 2 tokens, exactly at budget.
	d := acc.ProcessEvent(kiroproto.Event{Type: "reasoningContentEvent", ThinkingText: "Think!!!"})
	if d.ThinkingDelta != "Think!!!" {
		t.Fatalf("ThinkingDelta = %q", d.ThinkingDelta)
	}
	if !acc.LocalStop {
		t.Fatal("expected LocalStop after thinking exhausted budget")
	}
	// Subsequent text should be suppressed.
	d = acc.ProcessEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hello"})
	if d.TextDelta != "" {
		t.Fatalf("expected empty TextDelta, got %q", d.TextDelta)
	}
}

func TestAccumulator_ThinkingViaTags(t *testing.T) {
	var acc responseAccumulator
	// assistantResponseEvent with <thinking>...</thinking> tags followed by text.
	d := acc.ProcessEvent(kiroproto.Event{
		Type:    "assistantResponseEvent",
		Content: "<thinking>I need to analyze this step by step</thinking>The answer is 42",
	})
	if d.ThinkingDelta != "I need to analyze this step by step" {
		t.Fatalf("ThinkingDelta = %q", d.ThinkingDelta)
	}
	if d.TextDelta != "The answer is 42" {
		t.Fatalf("TextDelta = %q", d.TextDelta)
	}
	if acc.ThinkingBuf.String() != "I need to analyze this step by step" {
		t.Fatalf("ThinkingBuf = %q", acc.ThinkingBuf.String())
	}
	if !acc.HasText {
		t.Fatal("expected HasText")
	}
}

func TestAccumulator_ThinkingViaTags_OnlyThinking(t *testing.T) {
	var acc responseAccumulator
	d := acc.ProcessEvent(kiroproto.Event{
		Type:    "assistantResponseEvent",
		Content: "<thinking>reasoning only</thinking>",
	})
	if d.ThinkingDelta != "reasoning only" {
		t.Fatalf("ThinkingDelta = %q", d.ThinkingDelta)
	}
	if d.TextDelta != "" {
		t.Fatalf("TextDelta = %q, want empty", d.TextDelta)
	}
	if acc.HasText {
		t.Fatal("should not have HasText for thinking-only")
	}
	if !acc.IsEmptyVisibleEndTurn() {
		t.Fatal("expected IsEmptyVisibleEndTurn for thinking-only")
	}
}

func TestAccumulator_ThinkingViaTags_SplitAcrossChunks(t *testing.T) {
	var acc responseAccumulator
	// Chunk 1: partial open tag + thinking content
	d1 := acc.ProcessEvent(kiroproto.Event{
		Type:    "assistantResponseEvent",
		Content: "<thinking>Step 1",
	})
	// Chunk 2: more thinking + close tag + text
	d2 := acc.ProcessEvent(kiroproto.Event{
		Type:    "assistantResponseEvent",
		Content: "<thinking>Step 1: analyze</thinking>Result",
	})
	// Combine deltas.
	allThinking := d1.ThinkingDelta + d2.ThinkingDelta
	allText := d1.TextDelta + d2.TextDelta
	if allThinking != "Step 1: analyze" {
		t.Fatalf("combined ThinkingDelta = %q", allThinking)
	}
	if allText != "Result" {
		t.Fatalf("combined TextDelta = %q", allText)
	}
}

func TestAccumulator_StopSequence_EmptyStringIgnored(t *testing.T) {
	// An empty string in stop_sequences should be ignored, not cause immediate stop.
	// strings.Index(x, "") == 0 is always true, so empty strings must be filtered.
	var acc responseAccumulator
	acc.initStopSequences([]string{""})
	d := acc.ProcessEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hello world"})
	if d.TextDelta != "Hello world" {
		t.Fatalf("TextDelta = %q, want full text (empty stop seq should be ignored)", d.TextDelta)
	}
	if acc.LocalStop {
		t.Fatal("empty stop sequence should not trigger LocalStop")
	}
}

func TestAccumulator_StopSequence_EmptyStringMixedWithValid(t *testing.T) {
	// Empty strings mixed with valid stop sequences: only valid ones should work.
	var acc responseAccumulator
	acc.initStopSequences([]string{"", "STOP", ""})
	d := acc.ProcessEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "beforeSTOPafter"})
	if d.TextDelta != "before" {
		t.Fatalf("TextDelta = %q, want %q", d.TextDelta, "before")
	}
	if !acc.LocalStop {
		t.Fatal("expected LocalStop from valid stop sequence")
	}
	if acc.StopSequence != "STOP" {
		t.Fatalf("StopSequence = %q, want STOP", acc.StopSequence)
	}
}

func TestAccumulator_PreCountedInputTokens(t *testing.T) {
	tests := []struct {
		name            string
		preCounted      int
		events          []kiroproto.Event
		wantInput       int
		wantOutputAbove int // output should be >= this (0 means no constraint)
	}{
		{
			name:       "pre-counted used when no metadata/metering",
			preCounted: 500,
			events: []kiroproto.Event{
				{Type: "assistantResponseEvent", Content: "Hello world"},
			},
			wantInput: 500,
		},
		{
			name:       "metadata overrides pre-counted",
			preCounted: 500,
			events: []kiroproto.Event{
				{Type: "assistantResponseEvent", Content: "Hi"},
				{Type: "metadataEvent", InputTokens: 100, OutputTokens: 50},
			},
			wantInput: 100,
		},
		{
			name:       "metering overrides pre-counted",
			preCounted: 500,
			events: []kiroproto.Event{
				{Type: "assistantResponseEvent", Content: "Hi"},
				{Type: "meteringEvent", InputTokens: 200, OutputTokens: 80},
			},
			wantInput: 200,
		},
		{
			name:       "pre-counted zero falls through to 0,0",
			preCounted: 0,
			events: []kiroproto.Event{
				{Type: "assistantResponseEvent", Content: "Hello"},
			},
			wantInput: 0,
		},
		{
			name:       "pre-counted with output estimation",
			preCounted: 1000,
			events: []kiroproto.Event{
				{Type: "assistantResponseEvent", Content: "Hello world"}, // 11 runes -> ~2 tokens
			},
			wantInput:       1000,
			wantOutputAbove: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := newAccumulator(200000, nil, 0, tt.preCounted)
			for _, e := range tt.events {
				acc.ProcessEvent(e)
			}
			input, output := acc.resolvedUsage()
			if input != tt.wantInput {
				t.Fatalf("input_tokens = %d, want %d", input, tt.wantInput)
			}
			if tt.wantOutputAbove > 0 && output < tt.wantOutputAbove {
				t.Fatalf("output_tokens = %d, want >= %d", output, tt.wantOutputAbove)
			}
		})
	}
}

func TestAccumulator_ThinkingViaTags_MixedWithRegularTool(t *testing.T) {
	var acc responseAccumulator
	// First: thinking via tags in assistant response.
	d := acc.ProcessEvent(kiroproto.Event{
		Type:    "assistantResponseEvent",
		Content: "<thinking>reasoning</thinking>",
	})
	if d.ThinkingDelta != "reasoning" {
		t.Fatalf("ThinkingDelta = %q", d.ThinkingDelta)
	}
	// Second: regular tool.
	d = acc.ProcessEvent(kiroproto.Event{
		Type: "toolUseEvent", ToolStop: true,
		ToolUseID: "t2", ToolName: "bash",
		ToolInput: `{"cmd":"ls"}`,
	})
	if !d.ToolStop {
		t.Fatal("regular tool should set ToolStop")
	}
	if !acc.HasToolUse {
		t.Fatal("regular tool should set HasToolUse")
	}
	if len(acc.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1 (only regular tool)", len(acc.ToolCalls))
	}
}

func TestAccumulator_IsEmptyVisibleEndTurn(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(a *responseAccumulator)
		expect bool
	}{
		{
			name: "thinking only via tags",
			setup: func(a *responseAccumulator) {
				a.ProcessEvent(kiroproto.Event{
					Type:    "assistantResponseEvent",
					Content: "<thinking>reasoning</thinking>",
				})
			},
			expect: true,
		},
		{
			name: "thinking only via reasoning event",
			setup: func(a *responseAccumulator) {
				a.ProcessEvent(kiroproto.Event{Type: "reasoningContentEvent", ThinkingText: "Thinking..."})
			},
			expect: true,
		},
		{
			name: "thinking with text",
			setup: func(a *responseAccumulator) {
				a.ProcessEvent(kiroproto.Event{
					Type:    "assistantResponseEvent",
					Content: "<thinking>reasoning</thinking>Hello",
				})
			},
			expect: false,
		},
		{
			name: "thinking with tool use",
			setup: func(a *responseAccumulator) {
				a.ProcessEvent(kiroproto.Event{
					Type:    "assistantResponseEvent",
					Content: "<thinking>reasoning</thinking>",
				})
				a.ProcessEvent(kiroproto.Event{
					Type: "toolUseEvent", ToolStop: true,
					ToolUseID: "t2", ToolName: "bash",
					ToolInput: `{"cmd":"ls"}`,
				})
			},
			expect: false,
		},
		{
			name: "no thinking no text",
			setup: func(a *responseAccumulator) {
				// empty accumulator
			},
			expect: false,
		},
		{
			name: "text only no thinking",
			setup: func(a *responseAccumulator) {
				a.ProcessEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hello"})
			},
			expect: false,
		},
		{
			name: "thinking with local stop",
			setup: func(a *responseAccumulator) {
				a.ProcessEvent(kiroproto.Event{
					Type:    "assistantResponseEvent",
					Content: "<thinking>reasoning</thinking>",
				})
				a.LocalStop = true
				a.StopReason = StopReasonMaxTokens
			},
			expect: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var acc responseAccumulator
			tt.setup(&acc)
			if got := acc.IsEmptyVisibleEndTurn(); got != tt.expect {
				t.Errorf("IsEmptyVisibleEndTurn() = %v, want %v", got, tt.expect)
			}
		})
	}
}
