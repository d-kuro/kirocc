package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const tokenValidityBuffer = 5 * time.Minute

// Option configures an AuthManager.
type Option func(*AuthManager)

// WithHTTPClient sets a custom HTTP client for token refresh requests.
func WithHTTPClient(c *http.Client) Option {
	return func(m *AuthManager) { m.httpClient = c }
}

// WithAPIKey configures a Kiro API key ("ksk_…", normally from KIRO_API_KEY) as
// the credential source. Such a key is long-lived and presented directly to the
// API, so the SQLite database is never opened and no refresh ever happens —
// which is what lets kirocc run with no Kiro CLI login, in CI or a container.
// An empty key is ignored, leaving the database path in effect.
func WithAPIKey(key, region string) Option {
	return func(m *AuthManager) {
		if key == "" {
			return
		}
		if region == "" {
			region = defaultAPIKeyRegion
		}
		m.apiKey = key
		m.apiKeyRegion = region
	}
}

// defaultAPIKeyRegion is the region to target for API-key auth when none is
// configured. An API key carries no region of its own, unlike a credential read
// from the database.
const defaultAPIKeyRegion = "us-east-1"

// AuthManager manages Kiro credentials with caching and automatic refresh.
type AuthManager struct {
	dbPath           string
	db               *sql.DB // non-nil only in tests via newAuthManagerWithDB
	apiKey           string  // when set, short-circuits both the DB and refresh
	apiKeyRegion     string
	httpClient       *http.Client
	mu               sync.Mutex
	cached           *Credentials
	refreshGroup     singleflight.Group
	oidcEndpointFn   func(ssoRegion string) string
	socialEndpointFn func(region string) string
}

// UsesAPIKey reports whether credentials come from a Kiro API key rather than
// from the Kiro CLI database.
func (m *AuthManager) UsesAPIKey() bool { return m.apiKey != "" }

func newDefaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// NewAuthManager creates an AuthManager that reads credentials from the given SQLite DB path.
func NewAuthManager(dbPath string, opts ...Option) *AuthManager {
	m := &AuthManager{
		dbPath:           dbPath,
		httpClient:       newDefaultHTTPClient(),
		oidcEndpointFn:   defaultOIDCEndpoint,
		socialEndpointFn: defaultSocialEndpoint,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// newAuthManagerWithDB creates an AuthManager backed by an existing *sql.DB (for testing).
func newAuthManagerWithDB(db *sql.DB) *AuthManager {
	return &AuthManager{
		db:               db,
		httpClient:       newDefaultHTTPClient(),
		oidcEndpointFn:   defaultOIDCEndpoint,
		socialEndpointFn: defaultSocialEndpoint,
	}
}

func defaultOIDCEndpoint(ssoRegion string) string {
	return fmt.Sprintf("https://oidc.%s.amazonaws.com/token", ssoRegion)
}

func defaultSocialEndpoint(region string) string {
	if region == "" {
		region = "us-east-1"
	}
	return fmt.Sprintf("https://prod.%s.auth.desktop.kiro.dev/refreshToken", region)
}

// GetToken returns valid credentials, refreshing if necessary.
// It is safe for concurrent use. Concurrent refresh requests are deduplicated via singleflight.
func (m *AuthManager) GetToken(ctx context.Context) (*Credentials, error) {
	// An API key is static: nothing to read, nothing to expire, nothing to
	// refresh. Checked before the cache so the DB and refresh paths below are
	// unreachable in this mode — a revoked key therefore surfaces as a 401 from
	// the API rather than as a refresh failure here.
	if m.apiKey != "" {
		return &Credentials{
			AccessToken: m.apiKey,
			Region:      m.apiKeyRegion,
			AuthType:    AuthTypeAPIKey,
		}, nil
	}

	m.mu.Lock()
	// Return cached credentials if still valid (copy to prevent external mutation).
	if m.cached != nil && isTokenValid(m.cached.ExpiresAt) {
		c := *m.cached
		m.mu.Unlock()
		return &c, nil
	}
	m.mu.Unlock()

	// Use singleflight to deduplicate concurrent refresh attempts.
	// Detach the caller's deadline so that one short-lived request
	// cannot cancel the shared refresh for all waiters.
	// Apply a bounded timeout to prevent indefinite goroutine retention.
	v, err, _ := m.refreshGroup.Do("refresh", func() (any, error) {
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 35*time.Second)
		defer cancel()
		return m.refreshCredentials(refreshCtx)
	})
	if err != nil {
		return nil, err
	}
	// Copy to prevent callers from mutating the shared cached credentials.
	creds := *v.(*Credentials)
	return &creds, nil
}

// InvalidateCache clears the cached credentials, forcing the next GetToken call
// to re-read from DB and potentially refresh. Used when a 403 indicates the
// cached token is rejected by the upstream API.
func (m *AuthManager) InvalidateCache() {
	m.mu.Lock()
	m.cached = nil
	m.mu.Unlock()
}

// refreshCredentials re-reads from DB and refreshes if needed. Called under singleflight.
func (m *AuthManager) refreshCredentials(ctx context.Context) (*Credentials, error) {
	// Re-check cache under lock — another goroutine may have refreshed while we waited.
	m.mu.Lock()
	if m.cached != nil && isTokenValid(m.cached.ExpiresAt) {
		c := *m.cached
		m.mu.Unlock()
		return &c, nil
	}
	m.mu.Unlock()

	creds, err := m.readFromDB()
	if err != nil {
		return nil, err
	}

	if isTokenValid(creds.ExpiresAt) {
		slog.Info("credentials loaded", "auth_type", creds.AuthType, "region", creds.Region)
		m.mu.Lock()
		m.cached = creds
		m.mu.Unlock()
		return creds, nil
	}

	// DB token also expired — refresh (no lock held during HTTP call).
	slog.Info("credentials expired, refreshing", "auth_type", creds.AuthType, "region", creds.Region)
	var refreshed *Credentials
	if creds.AuthType == "social" {
		endpoint := m.socialEndpointFn(creds.Region)
		refreshed, err = m.refreshSocialToken(ctx, creds, endpoint)
	} else {
		// IDC/OIDC: require device registration (ClientID + ClientSecret).
		if creds.ClientID == "" || creds.ClientSecret == "" {
			return nil, fmt.Errorf("token refresh: idc credentials missing device registration (clientId/clientSecret)")
		}
		if creds.SSORegion == "" {
			return nil, fmt.Errorf("token refresh: idc credentials missing region (check kiro-cli configuration)")
		}
		endpoint := m.oidcEndpointFn(creds.SSORegion)
		refreshed, err = m.refreshOIDCToken(ctx, creds, endpoint)
	}
	if err != nil {
		slog.Error("token refresh failed", "auth_type", creds.AuthType, "err", err)
		return nil, fmt.Errorf("token refresh: %w", err)
	}

	slog.Info("token refreshed", "auth_type", creds.AuthType)

	// Carry over fields not returned by the refresh endpoint.
	refreshed.Region = creds.Region
	refreshed.SSORegion = creds.SSORegion
	refreshed.ClientID = creds.ClientID
	refreshed.ClientSecret = creds.ClientSecret
	refreshed.AuthType = creds.AuthType
	if refreshed.ProfileARN == "" {
		refreshed.ProfileARN = creds.ProfileARN
	}

	m.mu.Lock()
	m.cached = refreshed
	m.mu.Unlock()
	return refreshed, nil
}

// readFromDB opens (or reuses) the SQLite DB and reads credentials.
func (m *AuthManager) readFromDB() (*Credentials, error) {
	if m.db != nil {
		return ReadCredentials(m.db)
	}
	db, err := OpenDB(m.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = db.Close() }()
	return ReadCredentials(db)
}

// isTokenValid reports whether the token expires more than tokenValidityBuffer from now.
func isTokenValid(expiresAt int64) bool {
	return time.Unix(expiresAt, 0).After(time.Now().Add(tokenValidityBuffer))
}

// tokenResponse holds the common fields from a token refresh response.
type tokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"`
	ProfileArn   string `json:"profileArn"` // social only
}

// doTokenRefresh posts a JSON body to the given endpoint and decodes the token response.
func (m *AuthManager) doTokenRefresh(ctx context.Context, endpoint string, body []byte, label string) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		slog.Debug("token refresh: error response body", "label", label, "status", resp.StatusCode, "body", string(errBody))
		return nil, fmt.Errorf("%s token endpoint returned %d", label, resp.StatusCode)
	}

	var result tokenResponse
	if err := json.UnmarshalRead(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if result.AccessToken == "" {
		return nil, fmt.Errorf("%s token response: empty access token", label)
	}
	if result.ExpiresIn <= 0 {
		return nil, fmt.Errorf("%s token response: invalid expiresIn %d", label, result.ExpiresIn)
	}

	return &result, nil
}

// refreshOIDCToken calls the AWS SSO OIDC token endpoint.
func (m *AuthManager) refreshOIDCToken(ctx context.Context, creds *Credentials, endpoint string) (*Credentials, error) {
	body, err := json.Marshal(map[string]string{
		"grantType":    "refresh_token",
		"clientId":     creds.ClientID,
		"clientSecret": creds.ClientSecret,
		"refreshToken": creds.RefreshToken,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	result, err := m.doTokenRefresh(ctx, endpoint, body, "oidc")
	if err != nil {
		return nil, err
	}

	return &Credentials{
		AccessToken:  result.AccessToken,
		RefreshToken: coalesce(result.RefreshToken, creds.RefreshToken),
		ExpiresAt:    time.Now().Unix() + result.ExpiresIn,
	}, nil
}

// refreshSocialToken calls the Kiro Desktop social auth refresh endpoint.
func (m *AuthManager) refreshSocialToken(ctx context.Context, creds *Credentials, endpoint string) (*Credentials, error) {
	body, err := json.Marshal(map[string]string{
		"refreshToken": creds.RefreshToken,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	result, err := m.doTokenRefresh(ctx, endpoint, body, "social")
	if err != nil {
		return nil, err
	}

	return &Credentials{
		AccessToken:  result.AccessToken,
		RefreshToken: coalesce(result.RefreshToken, creds.RefreshToken),
		ExpiresAt:    time.Now().Unix() + result.ExpiresIn,
		ProfileARN:   result.ProfileArn,
	}, nil
}
