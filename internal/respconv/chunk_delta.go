package respconv

// ComputeDelta computes the delta between a cumulative chunk and the previous text.
// Kiro API sends cumulative text, not deltas.
func ComputeDelta(chunk, previous string) string {
	if previous == "" {
		return chunk
	}
	if chunk == previous {
		return ""
	}
	if len(chunk) > len(previous) && chunk[:len(previous)] == previous {
		return chunk[len(previous):]
	}
	if len(previous) > len(chunk) && previous[:len(chunk)] == chunk {
		// text shrunk — abnormal
		return ""
	}
	// Overlap detection using KMP failure function:
	// find longest suffix of previous that is a prefix of chunk.
	overlap := kmpOverlap(previous, chunk)
	if overlap > 0 {
		return chunk[overlap:]
	}
	return chunk
}

// kmpOverlap finds the length of the longest suffix of a that is a prefix of b.
// Runs in O(len(a) + len(b)) time.
func kmpOverlap(a, b string) int {
	// Build combined string: b + sentinel + a
	// We want the longest prefix of b that matches a suffix of a.
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	// Build failure function for b.
	fail := make([]int, len(b))
	fail[0] = 0
	k := 0
	for i := 1; i < len(b); i++ {
		for k > 0 && b[k] != b[i] {
			k = fail[k-1]
		}
		if b[k] == b[i] {
			k++
		}
		fail[i] = k
	}

	// Match b against a using the failure function.
	// Must iterate over all bytes (not rune boundaries via `for i := range a`),
	// since KMP here is a byte-level match — `range` over a string skips the
	// interior bytes of multi-byte UTF-8 sequences and corrupts the overlap.
	k = 0
	for i := 0; i < len(a); i++ {
		for k > 0 && b[k] != a[i] {
			k = fail[k-1]
		}
		if b[k] == a[i] {
			k++
		}
		if k == len(b) {
			// Full match of b within a — shouldn't happen in normal cumulative mode,
			// but handle gracefully: the overlap is len(b).
			k = fail[k-1]
		}
	}
	return k
}
