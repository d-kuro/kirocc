// Package kirocatalog fetches Kiro's model catalog via ListAvailableModels so
// kirocc can resolve models that shipped after its own release.
//
// The catalog lives on a different host from the streaming API: models come from
// the control plane at management.<region>.kiro.dev, while completions go to
// runtime.<region>.kiro.dev.
package kirocatalog

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	amzTarget = "AmazonCodeWhispererService.ListAvailableModels"
	// origin identifies the calling surface. The field is required and enum
	// validated; KIRO_CLI is the value kiro-cli itself sends.
	origin = "KIRO_CLI"
	// errBodyLimit bounds how much of an error response is read into a message.
	errBodyLimit = 2048
	// respBodyLimit bounds the catalog response; the real one is ~10 KB.
	respBodyLimit = 1 << 20
)

// Model is one model advertised by the catalog, reduced to the fields kirocc
// uses for resolution.
type Model struct {
	ID              string
	MaxInputTokens  int
	MaxOutputTokens int
	// EffortEnum lists accepted output_config.effort values, low → high. Empty
	// when the model does not advertise an effort schema.
	EffortEnum []string
}

// Request carries the credential and routing inputs for a catalog fetch.
type Request struct {
	Token string
	// Region selects the control-plane host. Only a few regions serve one, and a
	// token is rejected outside the region that issued it.
	Region string
	// ProfileARN is required by the API; a request without it fails validation.
	// API-key credentials have no profile ARN and therefore cannot fetch.
	ProfileARN string
}

// Client fetches model catalogs.
type Client struct {
	httpClient *http.Client
	baseURL    string // override for tests; empty = region-based URL
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client used for catalog requests.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithBaseURL sets a custom endpoint (for testing).
func WithBaseURL(url string) Option {
	return func(c *Client) { c.baseURL = url }
}

// New creates a Client.
func New(opts ...Option) *Client {
	c := &Client{httpClient: &http.Client{Timeout: 15 * time.Second}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Client) endpointURL(region string) string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return fmt.Sprintf("https://management.%s.kiro.dev/", region)
}

// listResponse mirrors the ListAvailableModels response. The effort enum is
// nested inside a JSON Schema document describing additionalModelRequestFields:
//
//	additionalModelRequestFieldsSchema.properties.output_config.properties.effort.enum
type listResponse struct {
	Models []struct {
		ModelID     string `json:"modelId"`
		TokenLimits struct {
			MaxInputTokens  int `json:"maxInputTokens"`
			MaxOutputTokens int `json:"maxOutputTokens"`
		} `json:"tokenLimits"`
		Schema struct {
			Properties struct {
				OutputConfig struct {
					Properties struct {
						Effort struct {
							Enum []string `json:"enum"`
						} `json:"effort"`
					} `json:"properties"`
				} `json:"output_config"`
			} `json:"properties"`
		} `json:"additionalModelRequestFieldsSchema"`
	} `json:"models"`
}

// List fetches the model catalog. It returns an error rather than a partial
// result so callers can keep their built-in tables on any failure.
func (c *Client) List(ctx context.Context, r Request) ([]Model, error) {
	if r.Token == "" {
		return nil, fmt.Errorf("kirocatalog: missing token")
	}
	if r.Region == "" {
		return nil, fmt.Errorf("kirocatalog: missing region")
	}
	if r.ProfileARN == "" {
		return nil, fmt.Errorf("kirocatalog: missing profile ARN")
	}

	body, err := json.Marshal(map[string]string{
		"origin":     origin,
		"profileArn": r.ProfileARN,
	})
	if err != nil {
		return nil, fmt.Errorf("kirocatalog: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpointURL(r.Region), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("kirocatalog: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Amz-Target", amzTarget)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kirocatalog: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		return nil, fmt.Errorf("kirocatalog: status %d: %s", resp.StatusCode, errBody)
	}

	var parsed listResponse
	if err := json.UnmarshalRead(io.LimitReader(resp.Body, respBodyLimit), &parsed); err != nil {
		return nil, fmt.Errorf("kirocatalog: decode response: %w", err)
	}
	if len(parsed.Models) == 0 {
		return nil, fmt.Errorf("kirocatalog: response contained no models")
	}

	out := make([]Model, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		if m.ModelID == "" {
			continue
		}
		out = append(out, Model{
			ID:              m.ModelID,
			MaxInputTokens:  m.TokenLimits.MaxInputTokens,
			MaxOutputTokens: m.TokenLimits.MaxOutputTokens,
			EffortEnum:      m.Schema.Properties.OutputConfig.Properties.Effort.Enum,
		})
	}
	return out, nil
}
