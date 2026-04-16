package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/config"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/logging"
	"github.com/d-kuro/kirocc/internal/server"
	"github.com/d-kuro/kirocc/internal/tokencount"
	"github.com/d-kuro/kirocc/internal/tracing"
)

func main() {
	cfg := config.Config{}
	flag.IntVar(&cfg.Port, "port", 3456, "listen port")
	flag.StringVar(&cfg.Host, "host", "127.0.0.1", "bind host")
	flag.StringVar(&cfg.DBPath, "db", config.DefaultDBPath(), "kiro-cli SQLite DB path")
	flag.StringVar(&cfg.APIKey, "api-key", "", "optional API key for authentication")
	flag.BoolVar(&cfg.Debug, "debug", false, "enable debug logging with OTel JSON Lines output")
	flag.BoolVar(&cfg.OTel, "otel", false, "enable OpenTelemetry tracing (OTLP HTTP exporter)")
	flag.IntVar(&cfg.OTelBodyLimit, "otel-body-limit", config.DefaultOTelBodyLimit, "max bytes of request body to capture in OTel spans (0 = unlimited)")
	flag.StringVar(&cfg.LogFile.Path, "log-file", "", "write logs to file with rotation (for agent debugging)")
	flag.IntVar(&cfg.LogFile.MaxSize, "log-max-size", logging.DefaultLogMaxSize, "max log file size in MB before rotation")
	flag.IntVar(&cfg.LogFile.MaxBackups, "log-max-backups", logging.DefaultLogMaxBackups, "max number of old log files to retain")
	flag.IntVar(&cfg.LogFile.MaxAge, "log-max-age", logging.DefaultLogMaxAge, "max days to retain old log files")
	flag.BoolVar(&cfg.LogFile.Compress, "log-compress", false, "compress rotated log files with gzip")
	flag.BoolVar(&cfg.LogFile.Console, "log-console", false, "also write logs to console when -log-file is set")
	flag.Parse()

	if err := config.ApplyEnvOverrides(&cfg); err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	logHandler, logCloser := logging.NewHandler(cfg.Debug, cfg.LogFile)
	slog.SetDefault(slog.New(logHandler))

	if cfg.LogFile.Path != "" {
		slog.Info("file logging enabled", "path", cfg.LogFile.Path)
	}

	var otelShutdown func(context.Context) error
	if cfg.OTel {
		shutdown, err := tracing.Init(context.Background())
		if err != nil {
			slog.Error("otel init error", "err", err)
			os.Exit(1)
		}
		otelShutdown = shutdown
		slog.Info("OpenTelemetry tracing enabled", "body_limit", cfg.OTelBodyLimit)
	}

	authMgr := auth.NewAuthManager(cfg.DBPath)
	clientOpts := []kiroclient.HTTPClientOption{
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
	}
	if cfg.OTel {
		clientOpts = append(clientOpts, kiroclient.WithOTel(cfg.OTelBodyLimit))
	}
	kiroClient := kiroclient.NewHTTPClient(clientOpts...)

	var serverOpts []server.ServerOption
	if cfg.OTel {
		serverOpts = append(serverOpts, server.WithOTel(cfg.OTelBodyLimit))
	}
	srv := server.New(authMgr, cfg.APIKey, kiroClient, serverOpts...)

	// Eagerly initialize tiktoken so the first API request doesn't block on BPE data fetch.
	go tokencount.Preload()

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
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
		if otelShutdown != nil {
			if err := otelShutdown(ctx); err != nil {
				slog.Error("otel shutdown error", "err", err)
			}
		}
		if err := logCloser.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "log close error: %v\n", err)
		}
		close(done)
	}()

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
	<-done
}
