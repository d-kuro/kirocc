package server

import (
	"net/http"

	messagesapp "github.com/d-kuro/kirocc/internal/app/messages"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/tracing"
)

// ServerOption configures a Server.
type ServerOption func(*Server)

// WithOTel enables OpenTelemetry tracing middleware.
func WithOTel(bodyLimit int) ServerOption {
	return func(s *Server) {
		s.otel = true
		s.otelBodyLimit = bodyLimit
	}
}

// WithCapture enables upstream capture logging in the messages service.
func WithCapture(enabled bool) ServerOption {
	return func(s *Server) { s.captureEnabled = enabled }
}

// Server is the HTTP server for the kirocc proxy.
type Server struct {
	apiKey         string
	otel           bool
	otelBodyLimit  int
	captureEnabled bool
	mux            *http.ServeMux
	messages       *messagesapp.Service
}

// New creates a new Server.
func New(authMgr messagesapp.TokenGetter, apiKey string, client kiroclient.Client, opts ...ServerOption) *Server {
	s := &Server{
		apiKey: apiKey,
		mux:    http.NewServeMux(),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.messages = messagesapp.New(authMgr, client, messagesapp.WithCapture(s.captureEnabled))
	s.registerRoutes()
	return s
}

// Handler returns the http.Handler for the server.
func (s *Server) Handler() http.Handler {
	h := traceMiddleware(corsMiddleware(s.authMiddleware(s.mux)))
	if s.otel {
		h = tracing.Middleware(h, s.otelBodyLimit)
	}
	return h
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	s.mux.HandleFunc("POST /v1/messages/count_tokens", s.messages.HandleCountTokens)
	s.mux.HandleFunc("POST /v1/messages", s.messages.HandleMessages)
}
