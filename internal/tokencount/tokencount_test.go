package tokencount

import (
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
