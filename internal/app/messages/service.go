package messages

import (
	"context"
	"time"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/websearch"
)

// TokenGetter loads valid upstream credentials for a request.
type TokenGetter interface {
	GetToken(ctx context.Context) (*auth.Credentials, error)
}

// Service owns message execution and token counting flows.
type Service struct {
	auth              TokenGetter
	client            kiroclient.Client
	captureEnabled    bool
	keepAliveInterval time.Duration
	// webSearch runs the searches for an emulated web_search_20250305 tool.
	// Nil disables the emulation, in which case a request carrying that tool is
	// refused rather than answered without the search.
	webSearch           websearch.Provider
	webSearchMaxResults int
}

// Option configures a Service.
type Option func(*Service)

// WithCapture enables recording of full upstream request/response bodies on
// failure for debugging. Defaults to disabled; callers should enable it only
// when debug logging is on.
func WithCapture(enabled bool) Option {
	return func(s *Service) { s.captureEnabled = enabled }
}

// WithWebSearch enables in-proxy emulation of the web_search_20250305 server
// tool through the given provider. A nil provider leaves it disabled.
func WithWebSearch(provider websearch.Provider, maxResults int) Option {
	return func(s *Service) {
		s.webSearch = provider
		s.webSearchMaxResults = maxResults
	}
}

// WithKeepAliveInterval sets the idle interval for SSE keep-alive comments.
// A zero duration disables the heartbeat.
func WithKeepAliveInterval(interval time.Duration) Option {
	return func(s *Service) { s.keepAliveInterval = interval }
}

// New constructs a message service.
func New(authMgr TokenGetter, client kiroclient.Client, opts ...Option) *Service {
	s := &Service{
		auth:   authMgr,
		client: client,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}
