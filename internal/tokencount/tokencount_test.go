package tokencount

import (
	"strings"
	"sync"
	"testing"
)

func TestCountBytes_Basic(t *testing.T) {
	input := []byte(`{"conversationState":{"currentMessage":{"userInputMessage":{"content":"Hello, how are you?"}}}}`)
	count, err := CountBytes(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count <= 0 {
		t.Fatalf("expected positive token count, got %d", count)
	}
}

func TestCountBytes_Deterministic(t *testing.T) {
	input := []byte(`Hello world`)
	count1, err := CountBytes(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	count2, err := CountBytes(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count1 != count2 {
		t.Fatalf("non-deterministic: got %d and %d", count1, count2)
	}
}

func TestCountBytes_Empty(t *testing.T) {
	count, err := CountBytes(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 for empty input, got %d", count)
	}

	count, err = CountBytes([]byte{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 for empty slice, got %d", count)
	}
}

func TestCountBytes_Concurrent(t *testing.T) {
	input := []byte(`The quick brown fox jumps over the lazy dog`)
	var wg sync.WaitGroup
	errs := make(chan error, 50)

	for range 50 {
		wg.Go(func() {
			_, err := CountBytes(input)
			if err != nil {
				errs <- err
			}
		})
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent call failed: %v", err)
	}
}

func TestCountBytes_ImageIgnoresBase64Size(t *testing.T) {
	build := func(n int) []byte {
		return []byte(`{"images":[{"format":"png","source":{"bytes":"` + strings.Repeat("QUJD", n) + `"}}],"content":"hi"}`)
	}
	small, err := CountBytes(build(10))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	large, err := CountBytes(build(200_000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if small != large {
		t.Fatalf("image size changed count: %d vs %d", small, large)
	}
	if small < imageTokens || small > imageTokens+100 {
		t.Fatalf("expected about %d tokens, got %d", imageTokens, small)
	}
}

func TestCountBytes_MultipleImages(t *testing.T) {
	one := `{"source":{"bytes":"QUJD"}}`
	got, err := CountBytes([]byte(`[` + one + `,` + one + `,` + one + `]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got < 3*imageTokens {
		t.Fatalf("expected at least %d tokens, got %d", 3*imageTokens, got)
	}
}
