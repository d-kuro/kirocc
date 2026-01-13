package messages

import (
	"context"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/respconv"
)

// TokenGetter loads valid upstream credentials for a request.
type TokenGetter interface {
	GetToken(ctx context.Context) (*auth.Credentials, error)
}

// Service owns message execution and token counting flows.
type Service struct {
	auth           TokenGetter
	client         kiroclient.Client
	truncStore     *respconv.TruncationStore
	cancel         context.CancelFunc
	envState       *kiroproto.EnvState
	conversationID string
}

// New constructs a message service with its own truncation store lifecycle.
func New(authMgr TokenGetter, client kiroclient.Client, envState *kiroproto.EnvState, conversationID string) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		auth:           authMgr,
		client:         client,
		truncStore:     respconv.NewTruncationStore(ctx),
		cancel:         cancel,
		envState:       envState,
		conversationID: conversationID,
	}
}

// Close stops background work owned by the service.
func (s *Service) Close() {
	s.cancel()
}
