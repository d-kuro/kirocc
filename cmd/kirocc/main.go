package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/config"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/logging"
	"github.com/d-kuro/kirocc/internal/server"
	"github.com/d-kuro/kirocc/internal/tokencount"
)

func main() {
	cfg := config.Config{}
	flag.IntVar(&cfg.Port, "port", 3456, "listen port")
	flag.StringVar(&cfg.Host, "host", "127.0.0.1", "bind host")
	flag.StringVar(&cfg.DBPath, "db", config.DefaultDBPath(), "kiro-cli SQLite DB path")
	flag.StringVar(&cfg.APIKey, "api-key", "", "optional API key for authentication")
	flag.BoolVar(&cfg.Debug, "debug", false, "enable debug logging with OTel JSON Lines output")
	flag.Parse()

	if err := config.ApplyEnvOverrides(&cfg); err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	slog.SetDefault(slog.New(logging.NewHandler(cfg.Debug)))

	authMgr := auth.NewAuthManager(cfg.DBPath)
	kiroClient := kiroclient.NewHTTPClient(
		kiroclient.WithTokenCounter(tokencount.CountBytes),
		kiroclient.WithTokenRefresher(func(ctx context.Context) (string, error) {
			// Invalidate cache so GetToken re-reads from DB and refreshes
			// instead of returning the same rejected token.
			authMgr.InvalidateCache()
			creds, err := authMgr.GetToken(ctx)
			if err != nil {
				return "", err
			}
			return creds.AccessToken, nil
		}),
	)
	srv := server.New(authMgr, cfg.APIKey, kiroClient)

	// Eagerly initialize tiktoken so the first API request doesn't block on BPE data fetch.
	go tokencount.Preload()

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	if cfg.APIKey == "" && cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		slog.Warn("server is binding to a non-loopback address without an API key — all endpoints are unauthenticated",
			"host", cfg.Host)
	}
	slog.Info("kirocc listening", "addr", "http://"+addr)
	slog.Info("set ANTHROPIC_BASE_URL to use with Claude Code", "url", "http://"+addr)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout is intentionally not set: this server streams SSE responses
		// that can last minutes. A fixed WriteTimeout would kill long-running streams.
		// Slowloris is mitigated by ReadHeaderTimeout on the request side.
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	done := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		slog.Info("shutting down", "signal", sig.String())

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(ctx); err != nil {
			slog.Error("shutdown error", "err", err)
		}
		close(done)
	}()

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
	<-done
}
