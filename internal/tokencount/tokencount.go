package tokencount

import (
	"bytes"
	"sync"

	tiktoken "github.com/pkoukk/tiktoken-go"
)

const encodingName = "cl100k_base"

// imageTokens approximates the prompt cost of one image. Base64 image data is
// excluded from tokenization because it inflates the count by 100x or more.
const imageTokens = 1600

var imageBytesKey = []byte(`"bytes":"`)

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
	text, images := stripImages(data)
	return len(e.Encode(string(text), nil, nil)) + images*imageTokens, nil
}

// stripImages removes base64 image payloads ("bytes" values in the Kiro
// payload) and returns the remaining data with the number of images removed.
func stripImages(data []byte) ([]byte, int) {
	var out []byte
	images := 0
	for {
		i := bytes.Index(data, imageBytesKey)
		if i < 0 {
			break
		}
		start := i + len(imageBytesKey)
		end := bytes.IndexByte(data[start:], '"')
		if end < 0 {
			break
		}
		out = append(out, data[:start]...)
		data = data[start+end:]
		images++
	}
	if images == 0 {
		return data, 0
	}
	return append(out, data...), images
}
