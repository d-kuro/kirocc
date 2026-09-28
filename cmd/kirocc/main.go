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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/config"
	"github.com/d-kuro/kirocc/internal/kirocatalog"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/logging"
	"github.com/d-kuro/kirocc/internal/models"
	"github.com/d-kuro/kirocc/internal/safeguard"
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

	authMgr := auth.NewAuthManager(cfg.DBPath, auth.WithAPIKey(cfg.KiroAPIKey, cfg.KiroAPIRegion))
	if authMgr.UsesAPIKey() {
		// Say so up front: with a key there is no database read and no refresh,
		// so the usual "credentials loaded" line never appears and its absence
		// would otherwise look like a failure.
		slog.Info("using Kiro API key", "auth_type", auth.AuthTypeAPIKey, "db_read", false)
	}
	if cfg.KiroAPIRegion != "" {
		slog.Info("Kiro API region pinned", "region", cfg.KiroAPIRegion)
	}
	kiroClient := buildKiroClient(authMgr, cfg)
	srv := buildServer(authMgr, kiroClient, cfg)

	if cfg.ModelDiscovery {
		go discoverModels(ctx, authMgr, cfg.KiroAPIRegion)
	}

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
	fs.StringVar(&cfg.KiroAPIKey, "kiro-api-key", "", "Kiro API key (ksk_...) to use instead of the kiro-cli database credential; also KIRO_API_KEY")
	fs.StringVar(&cfg.KiroAPIRegion, "kiro-api-region", "", "region for Kiro API endpoints (runtime.<region>.kiro.dev); overrides the credential's region; also KIRO_API_REGION")
	fs.BoolVar(&cfg.ModelDiscovery, "model-discovery", true, "fetch Kiro's model catalog at startup so new models resolve without a kirocc update; also KIROCC_MODEL_DISCOVERY")
	fs.BoolVar(&cfg.Safeguard, "safeguard", true, "answer auto mode's safeguards field (on by default, keyless/free); -safeguard=false leaves it unanswered so Claude Code uses its own classifier; also KIROCC_SAFEGUARD")
	fs.StringVar(&cfg.SafeguardAPIKey, "safeguard-api-key", "", "TypeSafe API key; adds the paid TypeSafe endpoint as a fallback behind the free provider; also TYPESAFE_API_KEY")
	fs.StringVar(&cfg.SafeguardKeyFile, "safeguard-key-file", "", "path to read the TypeSafe key from when -safeguard-api-key/TYPESAFE_API_KEY is unset; also KIROCC_SAFEGUARD_KEY_FILE; defaults to ~/.config/kiro/typesafe-key")
	fs.StringVar(&cfg.SafeguardRecordFile, "safeguard-record-file", "", "append a replayable JSON-lines record of each Jev judgment here (for -replay-jev review/tuning); also KIROCC_SAFEGUARD_RECORD_FILE; off when empty")
	fs.StringVar(&cfg.SafeguardEndpoint, "safeguard-endpoint", "", "override the TypeSafe endpoint; also KIROCC_SAFEGUARD_ENDPOINT")
	fs.StringVar(&cfg.SafeguardModel, "safeguard-model", "", "override the TypeSafe model id; also KIROCC_SAFEGUARD_MODEL")
	fs.StringVar(&cfg.SafeguardQuestions, "safeguard-questions", "", "path to a questions JSON that overrides the default classification prompt; also KIROCC_SAFEGUARD_QUESTIONS")
	fs.StringVar(&cfg.SafeguardProviders, "safeguard-providers", "", "ordered, comma-separated Jev providers to try (e.g. typesafe,opencode); also KIROCC_SAFEGUARD_PROVIDERS; default: typesafe (if keyed) then keyless-free opencode")
	fs.BoolVar(&cfg.SafeguardFailover, "safeguard-failover", true, "fall through to the next provider on error; also KIROCC_SAFEGUARD_FAILOVER")
	fs.StringVar(&cfg.OpenCodeAPIKey, "opencode-api-key", "", "optional key for the OpenCode Zen provider; the free model is keyless, so only needed for a keyed tier; also OPENCODE_API_KEY")
	fs.BoolVar(&cfg.Debug, "debug", false, "enable debug logging with OTel JSON Lines output")
	fs.BoolVar(&cfg.OTel, "otel", false, "enable OpenTelemetry tracing (OTLP HTTP exporter)")
	fs.IntVar(&cfg.OTelBodyLimit, "otel-body-limit", config.DefaultOTelBodyLimit, "max bytes of request body to capture in OTel spans (0 = unlimited)")
	fs.Int64Var(&cfg.MaxRequestBody, "max-request-body", config.DefaultMaxRequestBody, "max bytes of a client request body (0 = unlimited); also KIROCC_MAX_REQUEST_BODY")
	fs.DurationVar(&cfg.KeepAliveInterval, "keepalive-interval", config.DefaultKeepAliveInterval, "SSE idle keep-alive interval (0 = disabled)")
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
	if authMgr.UsesAPIKey() {
		clientOpts = append(clientOpts, kiroclient.WithAPIKeyAuth())
	}
	if cfg.KiroAPIRegion != "" {
		clientOpts = append(clientOpts, kiroclient.WithRegion(cfg.KiroAPIRegion))
	}
	if cfg.OTel {
		clientOpts = append(clientOpts, kiroclient.WithOTel(cfg.OTelBodyLimit))
	}
	return kiroclient.NewHTTPClient(clientOpts...)
}

// modelDiscoveryTimeout bounds the startup catalog fetch. Generous, because it
// runs in the background and a slow answer costs nothing.
const modelDiscoveryTimeout = 20 * time.Second

// discoverModels installs Kiro's advertised model catalog as a fallback layer
// behind the built-in mapping table, so a model Kiro launches after this build
// resolves with the right context window and effort enum instead of falling
// through to pass-through defaults. Best-effort: every failure path leaves the
// built-in tables untouched.
//
// It runs in the background because it needs a credential, and blocking startup
// on a network round trip would delay the listener for no benefit — the built-in
// table already covers every model shipped with this build.
func discoverModels(ctx context.Context, authMgr *auth.AuthManager, regionOverride string) {
	ctx, cancel := context.WithTimeout(ctx, modelDiscoveryTimeout)
	defer cancel()

	creds, err := authMgr.GetToken(ctx)
	if err != nil {
		slog.Debug("model discovery skipped: no credentials", "err", err)
		return
	}
	if creds.ProfileARN == "" {
		// API keys carry no profile ARN, and the API rejects a request without
		// one. Nothing to do but keep the built-in table.
		slog.Debug("model discovery skipped: credential has no profile ARN", "auth_type", creds.AuthType)
		return
	}
	region := creds.Region
	if regionOverride != "" {
		region = regionOverride
	}

	catalog, err := kirocatalog.New().List(ctx, kirocatalog.Request{
		Token:      creds.AccessToken,
		Region:     region,
		ProfileARN: creds.ProfileARN,
	})
	if err != nil {
		slog.Warn("model discovery failed, using built-in model table", "region", region, "err", err)
		return
	}

	entries := make([]models.CatalogModel, 0, len(catalog))
	for _, m := range catalog {
		entries = append(entries, models.CatalogModel{
			ID:             m.ID,
			MaxInputTokens: m.MaxInputTokens,
			EffortEnum:     m.EffortEnum,
		})
	}
	added := models.SetCatalog(entries)
	slog.Info("model catalog discovered",
		"region", region, "advertised", len(catalog), "new_models", added)
}

func buildServer(authMgr *auth.AuthManager, client kiroclient.Client, cfg config.Config) *server.Server {
	opts := []server.ServerOption{
		server.WithKeepAliveInterval(cfg.KeepAliveInterval),
		server.WithMaxRequestBody(cfg.MaxRequestBody),
	}
	if cfg.OTel {
		opts = append(opts, server.WithOTel(cfg.OTelBodyLimit))
	}
	if cfg.Debug {
		opts = append(opts, server.WithCapture(true))
	}
	if sg := newSafeguardClient(cfg); sg != nil {
		opts = append(opts, server.WithSafeguard(sg))
	}
	return server.New(authMgr, cfg.APIKey, client, opts...)
}

// newSafeguardClient builds the auto-mode classifier, or returns nil when
// safeguards are disabled (-safeguard=false), in which case kirocc leaves the
// field unanswered and Claude Code falls back to its own classifier. Enabled
// (the default), the provider chain leads with the keyless, free OpenCode Zen
// endpoint, so it works with no key; a TypeSafe key (flag, env, or key file)
// appends the paid endpoint as a fallback reached only when the free tier
// errors, and the chain fails over on error unless -safeguard-failover=false.
func newSafeguardClient(cfg config.Config) *safeguard.Client {
	if !cfg.Safeguard {
		slog.Info("auto mode safeguards disabled (-safeguard=false); client will use its own classifier")
		return nil
	}
	// TypeSafe key: a direct key (flag or TYPESAFE_API_KEY) wins; otherwise read
	// a key file so a background/daemon start (which never sources an interactive
	// shell's rc) still picks it up.
	tsKey := cfg.SafeguardAPIKey
	if tsKey == "" {
		if k, from := readSafeguardKeyFile(cfg.SafeguardKeyFile); k != "" {
			tsKey = k
			slog.Info("auto mode safeguards: loaded TypeSafe key from file", "path", from)
		}
	}

	// Assemble the provider chain from the configured, ordered names. Unknown
	// names are skipped with a warning. Default order: opencode-free first, then
	// typesafe (only if keyed) as a paid fallback reached on a free-tier error.
	names := parseProviderList(cfg.SafeguardProviders)
	var providers []safeguard.Provider
	for _, name := range names {
		switch name {
		case "typesafe":
			if tsKey == "" {
				continue // no key -> nothing to prepend
			}
			endpoint := safeguard.DefaultEndpoint
			model := safeguard.DefaultModel
			if cfg.SafeguardEndpoint != "" {
				endpoint = cfg.SafeguardEndpoint
			}
			if cfg.SafeguardModel != "" {
				model = cfg.SafeguardModel
			}
			providers = append(providers, safeguard.NewProvider("typesafe", endpoint, model, tsKey))
		case "opencode":
			// Keyless by default; an OPENCODE_API_KEY selects a keyed/paid tier.
			providers = append(providers, safeguard.NewProvider("opencode", safeguard.OpenCodeEndpoint, safeguard.OpenCodeFreeModel, cfg.OpenCodeAPIKey))
		default:
			slog.Warn("auto mode safeguards: unknown provider, skipping", "provider", name)
		}
	}
	if len(providers) == 0 {
		// Everything was unknown or keyless-typesafe-only: fall back to the
		// keyless opencode default so safeguards still work.
		providers = []safeguard.Provider{safeguard.NewProvider("opencode", safeguard.OpenCodeEndpoint, safeguard.OpenCodeFreeModel, cfg.OpenCodeAPIKey)}
	}

	opts := []safeguard.Option{
		safeguard.WithProviders(providers...),
		safeguard.WithFailover(cfg.SafeguardFailover),
	}
	if cfg.SafeguardQuestions != "" {
		qs, err := safeguard.LoadQuestions(cfg.SafeguardQuestions)
		if err != nil {
			slog.Warn("auto mode safeguards: could not load questions override, using the default prompt", "path", cfg.SafeguardQuestions, "err", err)
		} else {
			opts = append(opts, safeguard.WithQuestions(qs))
			slog.Info("auto mode safeguards: using questions override", "path", cfg.SafeguardQuestions)
		}
	}
	if cfg.SafeguardRecordFile != "" {
		if rec, err := safeguard.NewFileRecorder(cfg.SafeguardRecordFile); err != nil {
			slog.Warn("auto mode safeguards: could not open record file, not recording", "path", cfg.SafeguardRecordFile, "err", err)
		} else {
			opts = append(opts, safeguard.WithRecorder(rec))
			slog.Info("auto mode safeguards: recording judgments", "path", cfg.SafeguardRecordFile)
		}
	}
	c := safeguard.New(tsKey, opts...)
	chosen := make([]string, len(providers))
	for i, p := range providers {
		chosen[i] = p.Name()
	}
	slog.Info("auto mode safeguards enabled", "providers", strings.Join(chosen, ","), "failover", cfg.SafeguardFailover)
	return c
}

// parseProviderList splits the comma-separated provider list, trimming spaces
// and lowercasing; an empty/blank value yields the default chain.
func parseProviderList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{"opencode", "typesafe"}
	}
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.ToLower(strings.TrimSpace(part)); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"opencode", "typesafe"}
	}
	return out
}

// readSafeguardKeyFile reads the TypeSafe key from cfg.SafeguardKeyFile, or from
// ~/.config/kiro/typesafe-key when unset. A missing file is not an error (the
// bridge simply runs without the classifier); it returns the trimmed key and
// the path it came from.
func readSafeguardKeyFile(path string) (key, from string) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", ""
		}
		path = filepath.Join(home, ".config", "kiro", "typesafe-key")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "" // missing/unreadable: run without the classifier
	}
	return strings.TrimSpace(string(b)), path
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
