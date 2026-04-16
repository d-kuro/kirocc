package kiroclient

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
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

	userAgentValue    = "aws-sdk-rust/1.3.14 ua/2.1 api/codewhispererstreaming/0.1.14474 os/macos lang/rust/1.92.0 md/appVersion-2.0.0 app/AmazonQ-For-CLI"
	amzUserAgentValue = "aws-sdk-rust/1.3.14 ua/2.1 api/codewhispererstreaming/0.1.14474 os/macos lang/rust/1.92.0 m/F app/AmazonQ-For-CLI"
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

// ErrBodyReadIdle is returned when the Kiro response body has not produced
// any data within the configured idle timeout. This guards against silent
// hangs where the server sends eventstream headers but never delivers frames.
var ErrBodyReadIdle = errors.New("kiroclient: body read idle timeout")

const defaultBodyReadIdleTimeout = 180 * time.Second

// HTTPClient is the production implementation of Client.
type HTTPClient struct {
	httpClient     *http.Client
	baseURL        string // override for tests; empty = use region-based URL
	otel           bool
	otelBodyLimit  int
	tokenRefresher TokenRefresher
	countTokens    func([]byte) (int, error) // nil = skip token counting
	bodyReadIdle   time.Duration             // idle timeout for response body reads; 0 = use default
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

// WithBodyReadIdleTimeout sets the idle read deadline applied to the
// response body of a successful 200 eventstream response. If no byte is
// read within the given duration, the body Read returns ErrBodyReadIdle.
//
// NOTE: The idle reader calls Close() to unblock a pending Read. This is
// guaranteed to work for net/http.Response.Body but is NOT a general
// guarantee for arbitrary io.ReadCloser implementations.
func WithBodyReadIdleTimeout(d time.Duration) HTTPClientOption {
	return func(c *HTTPClient) { c.bodyReadIdle = d }
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

func (c *HTTPClient) bodyReadIdleTimeout() time.Duration {
	if c.bodyReadIdle > 0 {
		return c.bodyReadIdle
	}
	return defaultBodyReadIdleTimeout
}

// idleReader wraps an io.ReadCloser and enforces an idle timeout per Read call.
// If no data is read within the configured duration, Read returns ErrBodyReadIdle.
//
// The timeout recovery works by calling Close on the underlying reader, which
// is guaranteed to unblock a pending Read for net/http.Response.Body. This is
// NOT a general guarantee for arbitrary io.ReadCloser implementations.
type idleReader struct {
	rc   io.ReadCloser
	idle time.Duration
}

func (r *idleReader) Read(p []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1) // buffered: sender never blocks even if we time out
	go func() {
		n, err := r.rc.Read(p)
		ch <- result{n, err}
	}()
	t := time.NewTimer(r.idle)
	defer t.Stop()
	select {
	case res := <-ch:
		return res.n, res.err
	case <-t.C:
		_ = r.rc.Close() // forces the pending Read to unblock (net/http guarantee)
		return 0, ErrBodyReadIdle
	}
}

func (r *idleReader) Close() error { return r.rc.Close() }

func (c *HTTPClient) recordError(ctx context.Context, err error) {
	if c.otel {
		tracing.RecordError(ctx, err)
	}
}

func (c *HTTPClient) endpointURL(region string) string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return fmt.Sprintf("https://q.%s.amazonaws.com/", region)
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
	traceID, short := logging.TraceIDs(ctx)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+currentToken)
		req.Header.Set("Content-Type", "application/x-amz-json-1.0")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("X-Amz-Target", amzTarget)
		req.Header.Set("User-Agent", userAgentValue)
		req.Header.Set("x-amz-user-agent", amzUserAgentValue)
		req.Header.Set("x-amzn-codewhisperer-optout", "false")
		req.Header.Set("amz-sdk-invocation-id", invocationID)
		req.Header.Set("amz-sdk-request", fmt.Sprintf("attempt=%d; max=%d", attempt+1, maxRetries+1))

		slog.DebugContext(ctx, "kiro request headers",
			"trace_id", traceID,
			"session_id", logging.SessionIDFromContext(ctx),
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
				"trace_id", traceID,
				"session_id", logging.SessionIDFromContext(ctx),
				"status", resp.StatusCode,
				"headers", logging.SafeHeaders{H: resp.Header},
			)
			// Kiro sometimes returns 200 with Content-Type application/json
			// (AWS exception envelope such as ThrottlingException or
			// InternalServerException) instead of the expected
			// application/vnd.amazon.eventstream. Detect and surface that
			// explicitly — otherwise the eventstream parser reads a
			// non-framed body and eventually errors with a confusing
			// "reading prelude" message, masking the real upstream error.
			if ct := resp.Header.Get("Content-Type"); !isEventStreamContentType(ct) {
				errBody := readLimitedBody(resp.Body, upstreamBodyLimit)
				exType := resolveAWSException(errBody, resp.Header)
				// Retry transient AWS exceptions (throttling / internal / 5xx-equivalent)
				// even though the HTTP status is 200.
				if attempt < maxRetries && isRetryableAWSException(exType) {
					delay := backoffDelay(attempt)
					slog.WarnContext(ctx, "kiro: 200 with non-eventstream exception, retrying",
						"trace_id", short, "content_type", ct, "exception", exType,
						"attempt", attempt+1, "max", maxRetries+1,
						"delay", delay, "body", errBody)
					if waitErr := retryWait(ctx, delay); waitErr != nil {
						return nil, waitErr
					}
					continue
				}
				ue := &UpstreamError{
					Status:      resp.StatusCode,
					ContentType: ct,
					Exception:   exType,
					Body:        errBody,
				}
				c.recordError(ctx, ue)
				return nil, ue
			}
			body := io.ReadCloser(&idleReader{rc: resp.Body, idle: c.bodyReadIdleTimeout()})
			return &Response{
				StatusCode:   resp.StatusCode,
				Body:         body,
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
			ue := &UpstreamError{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type")}
			c.recordError(ctx, ue)
			return nil, ue

		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			errBody := readLimitedBody(resp.Body, upstreamBodyLimit)
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
			ue := &UpstreamError{
				Status:      resp.StatusCode,
				ContentType: resp.Header.Get("Content-Type"),
				Exception:   resolveAWSException(errBody, resp.Header),
				Body:        errBody,
			}
			c.recordError(ctx, ue)
			return nil, ue

		default:
			errBody := readLimitedBody(resp.Body, upstreamBodyLimit)
			ue := &UpstreamError{
				Status:      resp.StatusCode,
				ContentType: resp.Header.Get("Content-Type"),
				Exception:   resolveAWSException(errBody, resp.Header),
				Body:        errBody,
			}
			c.recordError(ctx, ue)
			return nil, ue
		}
	}

	err = fmt.Errorf("kiro api: max retries exceeded")
	c.recordError(ctx, err)
	return nil, err
}

// parseAWSExceptionType extracts the AWS exception type from an error body.
// AWS JSON 1.0 errors encode the exception class as "__type", optionally
// prefixed by a shape name ("com.amazonaws...#ThrottlingException").
// Returns "" if the body cannot be parsed.
func parseAWSExceptionType(body string) string {
	if body == "" {
		return ""
	}
	var m struct {
		Type1 string `json:"__type"`
		Type2 string `json:"type"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return ""
	}
	t := m.Type1
	if t == "" {
		t = m.Type2
	}
	if t == "" {
		t = m.Code
	}
	return normalizeAWSExceptionType(t)
}

// isRetryableAWSException reports whether an AWS exception type is transient
// and worth retrying (modeled after the AWS SDK retry policy).
func isRetryableAWSException(exType string) bool {
	switch exType {
	case "ThrottlingException",
		"TooManyRequestsException",
		"ServiceUnavailableException",
		"InternalServerException",
		"InternalFailureException",
		"InternalServerError":
		return true
	}
	return false
}

// UpstreamError is returned when the Kiro API responds with an HTTP error
// (any non-success status) or an unexpected Content-Type on a 200 response.
// Callers can use errors.As to extract structured fields for logging.
type UpstreamError struct {
	Status      int    // HTTP status code
	ContentType string // Content-Type header value
	Exception   string // AWS exception class (normalized, may be "")
	Body        string // response body (up to 8 KiB)
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("kiro api: status=%d content_type=%q exception=%q: %s",
		e.Status, e.ContentType, e.Exception, e.Body)
}

// normalizeAWSExceptionType strips namespace prefixes and hostname suffixes
// from an AWS exception type string. AWS uses two formats:
//   - JSON __type: "com.amazon.coral.service#ThrottlingException"
//   - Header X-Amzn-ErrorType: "ThrottlingException:http://example.com"
//
// This function handles both by stripping after '#' and before ':'.
func normalizeAWSExceptionType(raw string) string {
	if raw == "" {
		return ""
	}
	// Strip namespace prefix (e.g. "com.amazon.coral.service#ThrottlingException").
	if i := strings.LastIndexByte(raw, '#'); i >= 0 {
		raw = raw[i+1:]
	}
	// Strip hostname suffix (e.g. "ThrottlingException:http://example.com").
	if colon, _, ok := strings.Cut(raw, ":"); ok {
		raw = colon
	}
	return raw
}

// resolveAWSException determines the AWS exception type from the response,
// checking the body first, then falling back to the X-Amzn-ErrorType header.
func resolveAWSException(body string, header http.Header) string {
	if exType := parseAWSExceptionType(body); exType != "" {
		return exType
	}
	return normalizeAWSExceptionType(header.Get("X-Amzn-ErrorType"))
}

// isEventStreamContentType reports whether ct matches the AWS event stream
// content type (with or without parameters such as "; charset=...").
func isEventStreamContentType(ct string) bool {
	const want = "application/vnd.amazon.eventstream"
	if len(ct) < len(want) {
		return false
	}
	head := ct[:len(want)]
	if !strings.EqualFold(head, want) {
		return false
	}
	// Accept either exact match or media-type parameter separator.
	if len(ct) == len(want) {
		return true
	}
	c := ct[len(want)]
	return c == ';' || c == ' ' || c == '\t'
}

// backoffDelay returns exponential backoff delay with ±25% jitter.
func backoffDelay(attempt int) time.Duration {
	base := baseRetryDelay << attempt
	jitter := time.Duration(rand.Int64N(int64(base)/2)) - base/4
	return base + jitter
}

// readLimitedBody reads up to n bytes from body and closes it.
func readLimitedBody(body io.ReadCloser, n int64) string {
	b, _ := io.ReadAll(io.LimitReader(body, n))
	_ = body.Close()
	return string(b)
}

const upstreamBodyLimit = 8192

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
