package kiroclient

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/logging"
	"github.com/d-kuro/kirocc/internal/tracing"
	"github.com/google/uuid"
)

const (
	amzTarget      = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
	maxRetries     = 3
	baseRetryDelay = 1 * time.Second

	userAgentValue    = "aws-sdk-rust/1.3.12 ua/2.1 api/codewhispererstreaming/0.1.13922 os/macos lang/rust/1.92.0 md/appVersion-1.26.2 app/AmazonQ-For-CLI"
	amzUserAgentValue = "aws-sdk-rust/1.3.12 ua/2.1 api/codewhispererstreaming/0.1.13922 os/macos lang/rust/1.92.0 m/F app/AmazonQ-For-CLI"
)

// Client is the interface for calling the Kiro API.
type Client interface {
	GenerateAssistantResponse(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*Response, error)
}

// Response wraps the HTTP response from the Kiro API.
type Response struct {
	StatusCode   int
	Body         io.ReadCloser
	Header       http.Header
	PromptTokens int // pre-counted from serialized payload via tiktoken
}

// TokenRefresher is called when a 403 is received to get a fresh token.
type TokenRefresher func(ctx context.Context) (newToken string, err error)

// HTTPClient is the production implementation of Client.
type HTTPClient struct {
	httpClient     *http.Client
	baseURL        string // override for tests; empty = use region-based URL
	otel           bool
	otelBodyLimit  int
	tokenRefresher TokenRefresher
	countTokens    func([]byte) (int, error) // nil = skip token counting
}

// HTTPClientOption configures an HTTPClient.
type HTTPClientOption func(*HTTPClient)

// WithBaseURL sets a custom base URL (for testing).
func WithBaseURL(url string) HTTPClientOption {
	return func(c *HTTPClient) { c.baseURL = url }
}

// WithTokenRefresher sets the token refresh callback for 403 retry.
func WithTokenRefresher(fn TokenRefresher) HTTPClientOption {
	return func(c *HTTPClient) { c.tokenRefresher = fn }
}

// WithTokenCounter sets a function to count prompt tokens from the serialized payload.
func WithTokenCounter(fn func([]byte) (int, error)) HTTPClientOption {
	return func(c *HTTPClient) { c.countTokens = fn }
}

// WithOTel enables OpenTelemetry tracing on outgoing requests.
func WithOTel(bodyLimit int) HTTPClientOption {
	return func(c *HTTPClient) {
		c.otel = true
		c.otelBodyLimit = bodyLimit
	}
}

// NewHTTPClient creates a new HTTPClient.
func NewHTTPClient(opts ...HTTPClientOption) *HTTPClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 10
	transport.IdleConnTimeout = 90 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second

	c := &HTTPClient{}
	for _, opt := range opts {
		opt(c)
	}

	var rt http.RoundTripper = transport
	if c.otel {
		rt = tracing.WrapTransport(transport, c.otelBodyLimit)
	}
	c.httpClient = &http.Client{Transport: rt}
	return c
}

func (c *HTTPClient) recordError(ctx context.Context, err error) {
	if c.otel {
		tracing.RecordError(ctx, err)
	}
}

func (c *HTTPClient) endpointURL(region string) string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return fmt.Sprintf("https://q.%s.amazonaws.com/generateAssistantResponse", region)
}

// GenerateAssistantResponse sends a request to the Kiro API with retry logic.
func (c *HTTPClient) GenerateAssistantResponse(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*Response, error) {
	endpoint := c.endpointURL(region)

	if c.otel {
		var span trace.Span
		ctx, span = tracing.Tracer().Start(ctx, "kiro.GenerateAssistantResponse")
		defer span.End()
		span.SetAttributes(
			attribute.String("kiro.region", region),
			attribute.String("kiro.endpoint", endpoint),
		)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	var promptTokens int
	if c.countTokens != nil {
		n, err := c.countTokens(body)
		if err != nil {
			slog.Debug("tokencount: failed to count prompt tokens", "err", err)
		} else {
			promptTokens = n
		}
	}

	currentToken := token
	invocationID := uuid.New().String()
	short := logging.ShortTraceID(logging.TraceIDFromContext(ctx))

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+currentToken)
		req.Header.Set("Content-Type", "application/x-amz-json-1.0")
		req.Header.Set("Accept", "application/vnd.amazon.eventstream")
		req.Header.Set("X-Amz-Target", amzTarget)
		req.Header.Set("User-Agent", userAgentValue)
		req.Header.Set("x-amz-user-agent", amzUserAgentValue)
		req.Header.Set("x-amzn-codewhisperer-optout", "false")
		req.Header.Set("amz-sdk-invocation-id", invocationID)
		req.Header.Set("amz-sdk-request", fmt.Sprintf("attempt=%d; max=%d", attempt+1, maxRetries+1))

		slog.DebugContext(ctx, "kiro request headers",
			"trace_id", short,
			"headers", logging.SafeHeaders{H: req.Header},
		)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if attempt < maxRetries {
				delay := backoffDelay(attempt)
				slog.WarnContext(ctx, "kiro: request error, retrying",
					"trace_id", short, "attempt", attempt+1, "max", maxRetries+1,
					"delay", delay, "err", err)
				if waitErr := retryWait(ctx, delay); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			c.recordError(ctx, err)
			return nil, fmt.Errorf("do request: %w", err)
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			slog.DebugContext(ctx, "kiro response headers",
				"trace_id", short,
				"status", resp.StatusCode,
				"headers", logging.SafeHeaders{H: resp.Header},
			)
			return &Response{
				StatusCode:   resp.StatusCode,
				Body:         resp.Body,
				Header:       resp.Header,
				PromptTokens: promptTokens,
			}, nil

		case resp.StatusCode == http.StatusForbidden:
			_ = resp.Body.Close()
			if attempt < maxRetries && c.tokenRefresher != nil {
				newToken, err := c.tokenRefresher(ctx)
				if err != nil {
					slog.WarnContext(ctx, "kiro: token refresh failed",
						"trace_id", short, "err", err)
				} else {
					currentToken = newToken
					slog.InfoContext(ctx, "kiro: 403 received, token refreshed",
						"trace_id", short, "attempt", attempt+1, "max", maxRetries+1)
					continue
				}
			}
			err := fmt.Errorf("kiro api returned 403 Forbidden")
			c.recordError(ctx, err)
			return nil, err

		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			errBody := readErrorBody(resp.Body)
			if attempt < maxRetries {
				delay := backoffDelay(attempt)
				slog.WarnContext(ctx, "kiro: upstream error, retrying",
					"trace_id", short, "status", resp.StatusCode,
					"attempt", attempt+1, "max", maxRetries+1,
					"delay", delay, "body", errBody)
				if waitErr := retryWait(ctx, delay); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			err := fmt.Errorf("kiro api returned %d: %s", resp.StatusCode, errBody)
			c.recordError(ctx, err)
			return nil, err

		default:
			errBody := readErrorBody(resp.Body)
			err := fmt.Errorf("kiro api returned %d: %s", resp.StatusCode, errBody)
			c.recordError(ctx, err)
			return nil, err
		}
	}

	err = fmt.Errorf("kiro api: max retries exceeded")
	c.recordError(ctx, err)
	return nil, err
}

// backoffDelay returns exponential backoff delay with ±25% jitter.
func backoffDelay(attempt int) time.Duration {
	base := baseRetryDelay << attempt
	jitter := time.Duration(rand.Int64N(int64(base)/2)) - base/4
	return base + jitter
}

// readErrorBody reads up to 1024 bytes from body and closes it.
func readErrorBody(body io.ReadCloser) string {
	b, _ := io.ReadAll(io.LimitReader(body, 1024))
	_ = body.Close()
	return string(b)
}

// retryWait waits for the given delay, respecting ctx cancellation.
func retryWait(ctx context.Context, delay time.Duration) error {
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
