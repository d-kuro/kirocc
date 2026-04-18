package messages

import (
	"context"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/kiroclient"
)

// TokenGetter loads valid upstream credentials for a request.
type TokenGetter interface {
	GetToken(ctx context.Context) (*auth.Credentials, error)
}

// Service owns message execution and token counting flows.
type Service struct {
	auth           TokenGetter
	client         kiroclient.Client
	captureEnabled bool
}

// Option configures a Service.
type Option func(*Service)

// WithCapture enables recording of full upstream request/response bodies on
// failure for debugging. Defaults to disabled; callers should enable it only
// when debug logging is on.
func WithCapture(enabled bool) Option {
	return func(s *Service) { s.captureEnabled = enabled }
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
