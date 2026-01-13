package tokencount

import (
	"sync"

	tiktoken "github.com/pkoukk/tiktoken-go"
)

const encodingName = "cl100k_base"

var (
	enc  *tiktoken.Tiktoken
	mu   sync.Mutex
	once sync.Once
)

func getEncoding() (*tiktoken.Tiktoken, error) {
	// Fast path: already initialized successfully.
	once.Do(func() {
		e, err := tiktoken.GetEncoding(encodingName)
		if err == nil {
			enc = e
		}
	})
	if enc != nil {
		return enc, nil
	}

	// Slow path: first init failed, retry under mutex.
	mu.Lock()
	defer mu.Unlock()
	if enc != nil {
		return enc, nil
	}
	e, err := tiktoken.GetEncoding(encodingName)
	if err != nil {
		return nil, err
	}
	enc = e
	return enc, nil
}

// Preload initializes the tokenizer eagerly so that the first call to
// CountBytes does not block on a BPE data fetch. Safe to call multiple times.
func Preload() {
	_, _ = getEncoding()
}

// CountBytes tokenizes the provided bytes and returns the token count.
// Returns (0, err) if the tokenizer is unavailable.
func CountBytes(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	e, err := getEncoding()
	if err != nil {
		return 0, err
	}
	return len(e.Encode(string(data), nil, nil)), nil
}
