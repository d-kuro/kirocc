package tokencount

import (
	"bytes"
	"encoding/base64"
	json "encoding/json/v2"
	"image"
	"sync"

	// Register decoders so image.DecodeConfig can read dimensions from the
	// image header without decoding the full pixel data.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/d-kuro/kirocc/internal/kiroproto"
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

// fallbackImageTokens is the per-image estimate used when image dimensions
// cannot be read from the header (unsupported format or corrupt data). It
// matches Anthropic's rough ceiling for a downscaled image.
const fallbackImageTokens = 1600

// maxImageTokens caps the per-image estimate, mirroring Anthropic's practice of
// downscaling large images before metering so a single image cannot be reported
// as more than roughly this many tokens.
const maxImageTokens = 1600

// CountPayload counts the prompt tokens for a Kiro payload while excluding the
// base64 image bytes from the tiktoken pass. The raw base64 of an image tokenizes
// into millions of tokens, which does not reflect how the backend meters images,
// so each image is blanked out and replaced with a dimension-based estimate.
//
// The estimate is ceil(width*height/750) capped at maxImageTokens, matching
// Anthropic's image token formula; images whose dimensions cannot be read fall
// back to fallbackImageTokens.
func CountPayload(p *kiroproto.Payload) (int, error) {
	if p == nil {
		return 0, nil
	}

	stripped, imageBytes := stripImageBytes(p)

	data, err := marshalPayload(stripped)
	if err != nil {
		return 0, err
	}
	n, err := CountBytes(data)
	if err != nil {
		return 0, err
	}
	for _, b := range imageBytes {
		n += estimateImageTokens(b)
	}
	return n, nil
}

// marshalPayload serializes the payload with encoding/json/v2 so the kiroproto
// union types' MarshalJSONTo methods produce the same wire bytes the client
// sends.
func marshalPayload(p *kiroproto.Payload) ([]byte, error) {
	return json.Marshal(p)
}

// stripImageBytes returns a copy of the payload with every image's base64 bytes
// cleared, along with the collected original base64 strings in payload order so
// each can be given a dimension-based estimate. The input payload is not mutated.
func stripImageBytes(p *kiroproto.Payload) (*kiroproto.Payload, []string) {
	clone := *p
	var collected []string

	stripList := func(images []kiroproto.Image) []kiroproto.Image {
		if len(images) == 0 {
			return images
		}
		out := make([]kiroproto.Image, len(images))
		for i, img := range images {
			collected = append(collected, img.Source.Bytes)
			img.Source.Bytes = ""
			out[i] = img
		}
		return out
	}

	cur := clone.ConversationState.CurrentMessage.UserInputMessage
	cur.Images = stripList(cur.Images)
	clone.ConversationState.CurrentMessage.UserInputMessage = cur

	if len(clone.ConversationState.History) > 0 {
		history := make([]kiroproto.HistoryEntry, len(clone.ConversationState.History))
		copy(history, clone.ConversationState.History)
		for i := range history {
			if history[i].UserInputMessage == nil || len(history[i].UserInputMessage.Images) == 0 {
				continue
			}
			msg := *history[i].UserInputMessage
			msg.Images = stripList(msg.Images)
			history[i].UserInputMessage = &msg
		}
		clone.ConversationState.History = history
	}

	return &clone, collected
}

// estimateImageTokens approximates the token cost of a single base64 image using
// its decoded dimensions, falling back to a flat estimate when the header cannot
// be read.
func estimateImageTokens(b64 string) int {
	if b64 == "" {
		return 0
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return fallbackImageTokens
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return fallbackImageTokens
	}
	// ceil(width*height/750), matching Anthropic's documented image formula.
	tokens := (cfg.Width*cfg.Height + 749) / 750
	if tokens < 1 {
		tokens = 1
	}
	if tokens > maxImageTokens {
		tokens = maxImageTokens
	}
	return tokens
}
