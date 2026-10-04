package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/testutil"
)

type errorFrameClient struct{ frames []byte }

func (c *errorFrameClient) GenerateAssistantResponse(context.Context, string, *kiroproto.Payload, string) (*kiroclient.Response, error) {
	return &kiroclient.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(c.frames)),
		Header:     http.Header{},
	}, nil
}

func TestE2E_ErrorFrame(t *testing.T) {
	var headers []byte
	for _, h := range [][2]string{{":message-type", "error"}, {":error-code", "InternalFailure"}, {":error-message", "backend unavailable"}} {
		headers = append(headers, byte(len(h[0])))
		headers = append(headers, h[0]...)
		headers = append(headers, 7)
		headers = binary.BigEndian.AppendUint16(headers, uint16(len(h[1])))
		headers = append(headers, h[1]...)
	}
	errorFrame := testutil.AssembleFrame(headers, nil)
	for _, tt := range []struct {
		name       string
		stream     bool
		textBefore bool
	}{
		{name: "non-streaming"},
		{name: "streaming before text", stream: true},
		{name: "streaming after text", stream: true, textBefore: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var frames []byte
			if tt.textBefore {
				frames = testutil.BuildFrame(kiroproto.EventAssistantResponse, []byte(`{"content":"partial answer"}`))
			}
			frames = append(frames, errorFrame...)
			srv := newE2EServerWithClient(t, &errorFrameClient{frames: frames})
			defer srv.Close()
			resp := postMessages(t, srv.URL, fmt.Sprintf(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":%t}`, tt.stream))
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if tt.textBefore {
				if resp.StatusCode != http.StatusOK || concatTextDeltas(t, string(body)) != "partial answer" {
					t.Fatalf("status = %d, body = %s; want the preceding text preserved", resp.StatusCode, body)
				}
				if !strings.Contains(string(body), "event: error\n") || strings.Contains(string(body), "event: message_stop\n") {
					t.Fatalf("body = %s, want an error event instead of a successful completion", body)
				}
				return
			}
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502; body = %s", resp.StatusCode, body)
			}
			var got struct {
				Type  string `json:"type"`
				Error struct {
					Type string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if got.Type != "error" || got.Error.Type != "api_error" {
				t.Fatalf("body = %s, want an api_error response", body)
			}
		})
	}
}
