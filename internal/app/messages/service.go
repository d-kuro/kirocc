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
	auth   TokenGetter
	client kiroclient.Client
}

// New constructs a message service.
func New(authMgr TokenGetter, client kiroclient.Client) *Service {
	return &Service{
		auth:   authMgr,
		client: client,
	}
}
