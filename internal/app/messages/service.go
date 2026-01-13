package messages

import (
	"context"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/kiroproto"
)

// TokenGetter loads valid upstream credentials for a request.
type TokenGetter interface {
	GetToken(ctx context.Context) (*auth.Credentials, error)
}

// Service owns message execution and token counting flows.
type Service struct {
	auth     TokenGetter
	client   kiroclient.Client
	envState *kiroproto.EnvState
}

// New constructs a message service.
func New(authMgr TokenGetter, client kiroclient.Client, envState *kiroproto.EnvState) *Service {
	return &Service{
		auth:     authMgr,
		client:   client,
		envState: envState,
	}
}
