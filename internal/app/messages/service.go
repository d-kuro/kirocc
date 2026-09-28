package messages

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/config"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/safeguard"
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
	maxRequestBody    int64
	safeguard         *safeguard.Client
	// degraded counts consecutive responses whose safeguard verdicts fell back
	// wholesale to the client's own classifier. It drives a single WARN when a
	// sustained fallback means the field has stopped saving anything.
	degraded atomic.Int64
}

// Option configures a Service.
type Option func(*Service)

// WithCapture enables recording of full upstream request/response bodies on
// failure for debugging. Defaults to disabled; callers should enable it only
// when debug logging is on.
func WithCapture(enabled bool) Option {
	return func(s *Service) { s.captureEnabled = enabled }
}

// WithKeepAliveInterval sets the idle interval for SSE keep-alive comments.
// A zero duration disables the heartbeat.
func WithKeepAliveInterval(interval time.Duration) Option {
	return func(s *Service) { s.keepAliveInterval = interval }
}

// WithMaxRequestBody caps the client request body in bytes. Zero disables the
// cap. Defaults to config.DefaultMaxRequestBody when the option is omitted.
func WithMaxRequestBody(limit int64) Option {
	return func(s *Service) { s.maxRequestBody = limit }
}

// WithSafeguard supplies the classifier that answers auto mode's `safeguards`
// request field. A nil client leaves the field unanswered, which is the default
// and simply means Claude Code keeps using its own billed classifier.
func WithSafeguard(c *safeguard.Client) Option {
	return func(s *Service) { s.safeguard = c }
}

// New constructs a message service.
func New(authMgr TokenGetter, client kiroclient.Client, opts ...Option) *Service {
	s := &Service{
		auth:           authMgr,
		client:         client,
		maxRequestBody: config.DefaultMaxRequestBody,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}
