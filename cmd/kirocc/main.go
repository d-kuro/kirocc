package main

import (
	"context"
	"errors"
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
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg, err := parseFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := config.ApplyEnvOverrides(&cfg); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logHandler, logCloser := logging.NewHandler(cfg.Debug, cfg.LogFile)
	slog.SetDefault(slog.New(logHandler))
	if cfg.LogFile.Path != "" {
		slog.Info("file logging enabled", "path", cfg.LogFile.Path)
	}

	var otelShutdown func(context.Context) error
	if cfg.OTel {
		shutdown, err := tracing.Init(ctx)
		if err != nil {
			return fmt.Errorf("otel init: %w", err)
		}
		otelShutdown = shutdown
		slog.Info("OpenTelemetry tracing enabled", "body_limit", cfg.OTelBodyLimit)
	}

	authMgr := auth.NewAuthManager(cfg.DBPath)
	kiroClient := buildKiroClient(authMgr, cfg)
	srv := buildServer(authMgr, kiroClient, cfg)

	// Eagerly initialize tiktoken so the first API request doesn't block on BPE data fetch.
	go tokencount.Preload()

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	if cfg.APIKey == "" && !isLoopback(cfg.Host) {
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

	done := awaitShutdown(httpSrv, otelShutdown, logCloser)

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server: %w", err)
	}
	<-done
	return nil
}

func parseFlags(args []string) (config.Config, error) {
	fs := flag.NewFlagSet("kirocc", flag.ContinueOnError)
	var cfg config.Config
	fs.IntVar(&cfg.Port, "port", 3456, "listen port")
	fs.StringVar(&cfg.Host, "host", "127.0.0.1", "bind host")
	fs.StringVar(&cfg.DBPath, "db", config.DefaultDBPath(), "kiro-cli SQLite DB path")
	fs.StringVar(&cfg.APIKey, "api-key", "", "optional API key for authentication")
	fs.BoolVar(&cfg.Debug, "debug", false, "enable debug logging with OTel JSON Lines output")
	fs.BoolVar(&cfg.OTel, "otel", false, "enable OpenTelemetry tracing (OTLP HTTP exporter)")
	fs.IntVar(&cfg.OTelBodyLimit, "otel-body-limit", config.DefaultOTelBodyLimit, "max bytes of request body to capture in OTel spans (0 = unlimited)")
	fs.StringVar(&cfg.LogFile.Path, "log-file", "", "write logs to file with rotation (for agent debugging)")
	fs.IntVar(&cfg.LogFile.MaxSize, "log-max-size", logging.DefaultLogMaxSize, "max log file size in MB before rotation")
	fs.IntVar(&cfg.LogFile.MaxBackups, "log-max-backups", logging.DefaultLogMaxBackups, "max number of old log files to retain")
	fs.IntVar(&cfg.LogFile.MaxAge, "log-max-age", logging.DefaultLogMaxAge, "max days to retain old log files")
	fs.BoolVar(&cfg.LogFile.Compress, "log-compress", false, "compress rotated log files with gzip")
	fs.BoolVar(&cfg.LogFile.Console, "log-console", false, "also write logs to console when -log-file is set")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func buildKiroClient(authMgr *auth.AuthManager, cfg config.Config) kiroclient.Client {
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
	return kiroclient.NewHTTPClient(clientOpts...)
}

func buildServer(authMgr *auth.AuthManager, client kiroclient.Client, cfg config.Config) *server.Server {
	var opts []server.ServerOption
	if cfg.OTel {
		opts = append(opts, server.WithOTel(cfg.OTelBodyLimit))
	}
	if cfg.Debug {
		opts = append(opts, server.WithCapture(true))
	}
	return server.New(authMgr, cfg.APIKey, client, opts...)
}

func isLoopback(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// awaitShutdown registers a SIGINT/SIGTERM handler that gracefully stops the
// HTTP server, flushes OTel spans, and closes the log file. Returns a channel
// that closes when shutdown is complete.
func awaitShutdown(httpSrv *http.Server, otelShutdown func(context.Context) error, logCloser interface{ Close() error }) <-chan struct{} {
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
	return done
}
