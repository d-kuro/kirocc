package respconv

import (
	"strings"
	"unicode/utf8"
)

// applyStopSequenceFilter checks for stop sequences in the pending buffer + new delta.
// Returns the text to emit. If a stop sequence is found, sets LocalStop and truncates.
func (a *responseAccumulator) applyStopSequenceFilter(delta string) string {
	a.stopSeqPending += delta

	// Check for full match.
	for _, s := range a.stopSequences {
		if idx := strings.Index(a.stopSeqPending, s); idx >= 0 {
			emit := a.stopSeqPending[:idx]
			a.stopSeqPending = ""
			a.LocalStop = true
			a.StopReason = StopReasonStopSequence
			a.StopSequence = s
			return emit
		}
	}

	// Keep a trailing suffix that could be a partial match prefix.
	// Use rune-aware splitting to avoid cutting multi-byte UTF-8 characters.
	runeCount := utf8.RuneCountInString(a.stopSeqPending)
	if runeCount <= a.stopSeqMaxKeep {
		return ""
	}
	// Find the byte offset after (runeCount - stopSeqMaxKeep) runes.
	splitAt := 0
	for range runeCount - a.stopSeqMaxKeep {
		_, size := utf8.DecodeRuneInString(a.stopSeqPending[splitAt:])
		splitAt += size
	}
	emit := a.stopSeqPending[:splitAt]
	a.stopSeqPending = a.stopSeqPending[splitAt:]
	return emit
}

// flushStopSeqPending returns any remaining text in the stop sequence pending buffer.
// Called when the stream ends without a stop sequence match.
func (a *responseAccumulator) flushStopSeqPending() string {
	out := a.stopSeqPending
	a.stopSeqPending = ""
	return out
}

// resolveStopReason returns the stop_reason and stop_sequence for the Anthropic response.
func (a *responseAccumulator) resolveStopReason() (stopReason string, stopSequence any) {
	stopReason = StopReasonEndTurn
	if a.StopReason != "" {
		stopReason = a.StopReason
		if a.StopReason == StopReasonStopSequence {
			return stopReason, a.StopSequence
		}
		return stopReason, nil
	}
	if a.HasToolUse {
		return StopReasonToolUse, nil
	}
	return stopReason, nil
}

// initStopSequences sets the stop sequences and precomputes maxKeep (in runes).
// Empty strings are filtered out since strings.Index(x, "") == 0 is always true.
func (a *responseAccumulator) initStopSequences(seqs []string) {
	a.stopSequences = nil
	a.stopSeqMaxKeep = 0
	for _, s := range seqs {
		if s == "" {
			continue
		}
		a.stopSequences = append(a.stopSequences, s)
		runeLen := utf8.RuneCountInString(s)
		if runeLen-1 > a.stopSeqMaxKeep {
			a.stopSeqMaxKeep = runeLen - 1
		}
	}
}
