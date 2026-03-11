package server

import (
	"net/http"
	"os"
	"runtime"

	messagesapp "github.com/d-kuro/kirocc/internal/app/messages"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/kiroproto"
)

// Server is the HTTP server for the kirocc proxy.
type Server struct {
	apiKey   string
	mux      *http.ServeMux
	messages *messagesapp.Service
}

// New creates a new Server.
func New(authMgr messagesapp.TokenGetter, apiKey string, client kiroclient.Client) *Server {
	cwd, _ := os.Getwd()
	osName := runtime.GOOS
	if osName == "darwin" {
		osName = "macos"
	}

	envState := &kiroproto.EnvState{
		OperatingSystem:         osName,
		CurrentWorkingDirectory: cwd,
	}
	s := &Server{
		apiKey:   apiKey,
		mux:      http.NewServeMux(),
		messages: messagesapp.New(authMgr, client, envState),
	}
	s.registerRoutes()
	return s
}

// Handler returns the http.Handler for the server.
func (s *Server) Handler() http.Handler {
	return traceMiddleware(corsMiddleware(s.authMiddleware(s.mux)))
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	s.mux.HandleFunc("POST /v1/messages/count_tokens", s.messages.HandleCountTokens)
	s.mux.HandleFunc("POST /v1/messages", s.messages.HandleMessages)
}
