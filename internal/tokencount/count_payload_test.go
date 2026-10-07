package tokencount

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/kiroproto"
)

// pngBase64 builds a solid-color PNG of the given size and returns its base64.
func pngBase64(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func payloadWithImage(b64 string) *kiroproto.Payload {
	return &kiroproto.Payload{
		ConversationState: kiroproto.ConversationState{
			CurrentMessage: kiroproto.CurrentMessage{
				UserInputMessage: kiroproto.UserInputMessage{
					Content: "describe this",
					Images: []kiroproto.Image{
						{Format: "png", Source: kiroproto.ImageSource{Bytes: b64}},
					},
				},
			},
		},
	}
}

// A large image must not inflate the count by its base64 length: the bytes are
// excluded from tiktoken and replaced with a bounded per-image estimate.
func TestCountPayload_ExcludesImageBytes(t *testing.T) {
	b64 := pngBase64(t, 200, 200) // 200*200/750 = ~54 tokens, well under the cap
	p := payloadWithImage(b64)

	got, err := CountPayload(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Counting the raw bytes directly would tokenize the base64 blob and dwarf
	// the dimension estimate. Prove the image bytes were excluded.
	rawCount := len(b64) // base64 chars; tiktoken count is on this order or higher
	if got >= rawCount {
		t.Fatalf("image bytes not excluded: payload count %d >= base64 length %d", got, rawCount)
	}

	// The estimate for a 200x200 image is ceil(40000/750) = 54.
	if est := estimateImageTokens(b64); est != 54 {
		t.Fatalf("estimateImageTokens(200x200) = %d, want 54", est)
	}
}

// estimateImageTokens applies the ceil(w*h/750) formula, the cap, and the
// undecodable fallback.
func TestEstimateImageTokens(t *testing.T) {
	if est := estimateImageTokens(pngBase64(t, 200, 200)); est != 54 {
		t.Errorf("200x200 = %d, want 54", est)
	}
	if est := estimateImageTokens(pngBase64(t, 1, 1)); est != 1 {
		t.Errorf("1x1 = %d, want 1 (floor)", est)
	}
	if est := estimateImageTokens(pngBase64(t, 2000, 2000)); est != maxImageTokens {
		t.Errorf("2000x2000 = %d, want cap %d", est, maxImageTokens)
	}
	undecodable := base64.StdEncoding.EncodeToString([]byte("not an image"))
	if est := estimateImageTokens(undecodable); est != fallbackImageTokens {
		t.Errorf("undecodable = %d, want fallback %d", est, fallbackImageTokens)
	}
	if est := estimateImageTokens(""); est != 0 {
		t.Errorf("empty = %d, want 0", est)
	}
}

// History images are counted too, and the input payload is not mutated.
func TestCountPayload_HistoryImagesAndNoMutation(t *testing.T) {
	b64 := pngBase64(t, 150, 150)
	p := &kiroproto.Payload{
		ConversationState: kiroproto.ConversationState{
			CurrentMessage: kiroproto.CurrentMessage{
				UserInputMessage: kiroproto.UserInputMessage{Content: "follow up"},
			},
			History: []kiroproto.HistoryEntry{
				{
					UserInputMessage: &kiroproto.HistoryUserInputMessage{
						Content: "earlier",
						Images: []kiroproto.Image{
							{Format: "png", Source: kiroproto.ImageSource{Bytes: b64}},
						},
					},
				},
			},
		},
	}

	got, err := CountPayload(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rawCount := len(b64)
	if got >= rawCount {
		t.Fatalf("history image bytes not excluded: count %d >= base64 length %d", got, rawCount)
	}

	// Original payload must still hold the image bytes.
	if p.ConversationState.History[0].UserInputMessage.Images[0].Source.Bytes != b64 {
		t.Fatal("CountPayload mutated the input payload's history image bytes")
	}
}

func TestCountPayload_Nil(t *testing.T) {
	n, err := CountPayload(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 for nil payload, got %d", n)
	}
}

// Guard: a base64 string that happens to contain JSON-ish text must not leak
// into the counted document (regression intent of the whole change).
func TestCountPayload_NoBytesLeak(t *testing.T) {
	marker := strings.Repeat("ZZZZ", 500)
	p := payloadWithImage(marker)
	stripped, collected := stripImageBytes(p)
	data, err := marshalPayload(stripped)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(data, []byte(marker)) {
		t.Fatal("image bytes leaked into the counted payload")
	}
	if len(collected) != 1 || collected[0] != marker {
		t.Fatalf("expected the original bytes collected, got %v", collected)
	}
}
