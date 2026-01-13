package auth

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	tu "github.com/d-kuro/kirocc/internal/testutil"
	_ "modernc.org/sqlite"
)

// newTCP4TestServer creates an httptest.Server bound to tcp4 to avoid IPv6 bind failures in sandboxed environments.
func newTCP4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return tu.NewTCP4TestServer(t, handler)
}

// setupTestDBWithCreds creates an in-memory DB pre-populated with credentials.
func setupTestDBWithCreds(t *testing.T, accessToken, refreshToken string, expiresAt int64, region string) *sql.DB {
	t.Helper()
	db := setupTestDB(t)

	tokenJSON, _ := json.Marshal(map[string]any{
		"accessToken":  accessToken,
		"refreshToken": refreshToken,
		"expiresAt":    expiresAt,
	})
	_, err := db.Exec(`INSERT INTO auth_kv (key, value) VALUES (?, ?)`,
		"kirocli:odic:token", string(tokenJSON),
	)
	if err != nil {
		t.Fatalf("insert token: %v", err)
	}

	regJSON, _ := json.Marshal(map[string]string{
		"clientId":     "test-client-id",
		"clientSecret": "test-client-secret",
	})
	_, err = db.Exec(`INSERT INTO auth_kv (key, value) VALUES (?, ?)`,
		"kirocli:odic:device_registration", string(regJSON),
	)
	if err != nil {
		t.Fatalf("insert device reg: %v", err)
	}

	if region != "" {
		_, err = db.Exec(`INSERT INTO state (key, value) VALUES (?, ?)`,
			"auth.idc.region", region,
		)
		if err != nil {
			t.Fatalf("insert region: %v", err)
		}
	}

	return db
}

func TestRefreshToken_Success(t *testing.T) {
	newExpiry := time.Now().Add(8 * time.Hour).Unix()
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/token" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var body map[string]string
		if err := json.UnmarshalRead(r.Body, &body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body["grantType"] != "refresh_token" {
			http.Error(w, "bad grant type", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"accessToken":  "new-access-token",
			"refreshToken": "new-refresh-token",
			"expiresIn":    28800,
		})
		_, _ = w.Write([]byte("\n"))
		_ = newExpiry
	}))
	defer srv.Close()

	creds := &Credentials{
		RefreshToken: "old-refresh-token",
		ClientID:     "cid",
		ClientSecret: "csec",
		SSORegion:    "us-east-1",
	}

	result, err := (&AuthManager{httpClient: newDefaultHTTPClient()}).refreshOIDCToken(context.Background(), creds, srv.URL+"/token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.AccessToken != "new-access-token" {
		t.Errorf("AccessToken = %q, want %q", result.AccessToken, "new-access-token")
	}
	if result.RefreshToken != "new-refresh-token" {
		t.Errorf("RefreshToken = %q, want %q", result.RefreshToken, "new-refresh-token")
	}
	if result.ExpiresAt <= time.Now().Unix() {
		t.Errorf("ExpiresAt should be in the future, got %d", result.ExpiresAt)
	}
}

func TestRefreshToken_Failure(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	creds := &Credentials{
		RefreshToken: "bad-token",
		ClientID:     "cid",
		ClientSecret: "csec",
		SSORegion:    "us-east-1",
	}

	_, err := (&AuthManager{httpClient: newDefaultHTTPClient()}).refreshOIDCToken(context.Background(), creds, srv.URL+"/token")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAuthManager_CacheValid(t *testing.T) {
	futureExpiry := time.Now().Add(10 * time.Minute).Unix()
	db := setupTestDBWithCreds(t, "cached-token", "ref", futureExpiry, "us-east-1")
	defer func() { _ = db.Close() }()

	mgr := newAuthManagerWithDB(db)

	// Prime the cache
	creds1, err := mgr.GetToken(context.Background())
	if err != nil {
		t.Fatalf("first GetToken: %v", err)
	}

	// Second call should return cached value without hitting DB again
	creds2, err := mgr.GetToken(context.Background())
	if err != nil {
		t.Fatalf("second GetToken: %v", err)
	}

	if creds1.AccessToken != creds2.AccessToken {
		t.Error("expected same credentials (cache hit), got different values")
	}
	if creds2.AccessToken != "cached-token" {
		t.Errorf("AccessToken = %q, want %q", creds2.AccessToken, "cached-token")
	}
}

func TestAuthManager_CacheExpired_DBRefresh(t *testing.T) {
	// Token expired, but DB has a fresh one (simulating kiro-cli updating the DB)
	expiredExpiry := time.Now().Add(-1 * time.Minute).Unix()
	db := setupTestDBWithCreds(t, "stale-token", "ref", expiredExpiry, "us-east-1")
	defer func() { _ = db.Close() }()

	mgr := newAuthManagerWithDB(db)

	// Prime cache with expired token
	mgr.cached = &Credentials{
		AccessToken: "stale-token",
		ExpiresAt:   expiredExpiry,
	}

	// Update DB with fresh token
	freshExpiry := time.Now().Add(8 * time.Hour).Unix()
	freshJSON, _ := json.Marshal(map[string]any{
		"accessToken":  "fresh-from-db",
		"refreshToken": "ref2",
		"expiresAt":    freshExpiry,
	})
	_, err := db.Exec(`UPDATE auth_kv SET value = ? WHERE key = ?`, string(freshJSON), "kirocli:odic:token")
	if err != nil {
		t.Fatalf("update db: %v", err)
	}

	creds, err := mgr.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if creds.AccessToken != "fresh-from-db" {
		t.Errorf("AccessToken = %q, want %q", creds.AccessToken, "fresh-from-db")
	}
}

func TestAuthManager_DBAlsoExpired_OIDCRefresh(t *testing.T) {
	expiredExpiry := time.Now().Add(-1 * time.Minute).Unix()
	db := setupTestDBWithCreds(t, "expired-token", "old-refresh", expiredExpiry, "us-east-1")
	defer func() { _ = db.Close() }()

	callCount := 0
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"accessToken":  "oidc-refreshed-token",
			"refreshToken": "new-refresh",
			"expiresIn":    28800,
		})
		_, _ = w.Write([]byte("\n"))
	}))
	defer srv.Close()

	mgr := newAuthManagerWithDB(db)
	mgr.oidcEndpointFn = func(ssoRegion string) string {
		return srv.URL + "/token"
	}

	creds, err := mgr.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if creds.AccessToken != "oidc-refreshed-token" {
		t.Errorf("AccessToken = %q, want %q", creds.AccessToken, "oidc-refreshed-token")
	}
	if callCount != 1 {
		t.Errorf("OIDC call count = %d, want 1", callCount)
	}
}

func TestAuthManager_ConcurrentAccess(t *testing.T) {
	expiredExpiry := time.Now().Add(-1 * time.Minute).Unix()
	db := setupTestDBWithCreds(t, "expired-token", "old-refresh", expiredExpiry, "us-east-1")
	defer func() { _ = db.Close() }()

	callCount := 0
	var callMu sync.Mutex
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callMu.Lock()
		callCount++
		callMu.Unlock()
		// Simulate slow OIDC endpoint
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"accessToken":  "concurrent-token",
			"refreshToken": "new-refresh",
			"expiresIn":    28800,
		})
		_, _ = w.Write([]byte("\n"))
	}))
	defer srv.Close()

	mgr := newAuthManagerWithDB(db)
	mgr.oidcEndpointFn = func(ssoRegion string) string {
		return srv.URL + "/token"
	}

	const goroutines = 10
	var wg sync.WaitGroup
	errors := make([]error, goroutines)
	tokens := make([]string, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			creds, err := mgr.GetToken(context.Background())
			errors[idx] = err
			if creds != nil {
				tokens[idx] = creds.AccessToken
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errors {
		if err != nil {
			t.Errorf("goroutine %d error: %v", i, err)
		}
	}
	for i, tok := range tokens {
		if tok != "concurrent-token" {
			t.Errorf("goroutine %d token = %q, want %q", i, tok, "concurrent-token")
		}
	}
	if callCount != 1 {
		t.Errorf("OIDC call count = %d, want 1 (mutex should prevent duplicate calls)", callCount)
	}
}

func TestRefreshSocialToken_Success(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.UnmarshalRead(r.Body, &body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body["refreshToken"] != "social-ref" {
			http.Error(w, "wrong refresh token", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"accessToken":  "social-new-acc",
			"refreshToken": "social-new-ref",
			"expiresIn":    3600,
			"profileArn":   "arn:aws:codewhisperer:us-east-1:123:profile/social",
		})
		_, _ = w.Write([]byte("\n"))
	}))
	defer srv.Close()

	creds := &Credentials{
		RefreshToken: "social-ref",
		Region:       "us-east-1",
		AuthType:     "social",
	}

	result, err := (&AuthManager{httpClient: newDefaultHTTPClient()}).refreshSocialToken(context.Background(), creds, srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.AccessToken != "social-new-acc" {
		t.Errorf("AccessToken = %q, want %q", result.AccessToken, "social-new-acc")
	}
	if result.RefreshToken != "social-new-ref" {
		t.Errorf("RefreshToken = %q, want %q", result.RefreshToken, "social-new-ref")
	}
	if result.ProfileARN != "arn:aws:codewhisperer:us-east-1:123:profile/social" {
		t.Errorf("ProfileARN = %q, want social ARN", result.ProfileARN)
	}
}

func TestRefreshSocialToken_Failure(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	creds := &Credentials{RefreshToken: "bad"}
	_, err := (&AuthManager{httpClient: newDefaultHTTPClient()}).refreshSocialToken(context.Background(), creds, srv.URL)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAuthManager_SocialRefreshPath(t *testing.T) {
	expiredExpiry := time.Now().Add(-1 * time.Minute).Unix()
	db := setupTestDB(t)
	defer func() { _ = db.Close() }()

	// Insert social token (no device registration → social path)
	tokenJSON, _ := json.Marshal(map[string]any{
		"accessToken":  "expired-social",
		"refreshToken": "social-ref",
		"expiresAt":    expiredExpiry,
	})
	_, err := db.Exec(`INSERT INTO auth_kv (key, value) VALUES (?, ?)`,
		"kirocli:social:token", string(tokenJSON),
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"accessToken":  "social-refreshed",
			"refreshToken": "social-new-ref",
			"expiresIn":    3600,
		})
		_, _ = w.Write([]byte("\n"))
	}))
	defer srv.Close()

	mgr := newAuthManagerWithDB(db)
	mgr.socialEndpointFn = func(region string) string {
		return srv.URL
	}

	creds, err := mgr.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if creds.AccessToken != "social-refreshed" {
		t.Errorf("AccessToken = %q, want %q", creds.AccessToken, "social-refreshed")
	}
	if creds.AuthType != "social" {
		t.Errorf("AuthType = %q, want %q", creds.AuthType, "social")
	}
}

func TestRefreshOIDCToken_EmptyAccessToken(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"accessToken":  "",
			"refreshToken": "ref",
			"expiresIn":    3600,
		})
		_, _ = w.Write([]byte("\n"))
	}))
	defer srv.Close()

	creds := &Credentials{RefreshToken: "ref", ClientID: "c", ClientSecret: "s"}
	_, err := (&AuthManager{httpClient: newDefaultHTTPClient()}).refreshOIDCToken(context.Background(), creds, srv.URL)
	if err == nil {
		t.Fatal("expected error for empty accessToken")
	}
}

func TestRefreshOIDCToken_ZeroExpiresIn(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"accessToken":  "tok",
			"refreshToken": "ref",
			"expiresIn":    0,
		})
		_, _ = w.Write([]byte("\n"))
	}))
	defer srv.Close()

	creds := &Credentials{RefreshToken: "ref", ClientID: "c", ClientSecret: "s"}
	_, err := (&AuthManager{httpClient: newDefaultHTTPClient()}).refreshOIDCToken(context.Background(), creds, srv.URL)
	if err == nil {
		t.Fatal("expected error for zero expiresIn")
	}
}

func TestRefreshSocialToken_EmptyAccessToken(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"accessToken":  "",
			"refreshToken": "ref",
			"expiresIn":    3600,
		})
		_, _ = w.Write([]byte("\n"))
	}))
	defer srv.Close()

	creds := &Credentials{RefreshToken: "ref"}
	_, err := (&AuthManager{httpClient: newDefaultHTTPClient()}).refreshSocialToken(context.Background(), creds, srv.URL)
	if err == nil {
		t.Fatal("expected error for empty accessToken")
	}
}

func TestRefreshOIDCToken_RespectsContext(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow endpoint.
		time.Sleep(5 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"accessToken":  "tok",
			"refreshToken": "ref",
			"expiresIn":    3600,
		})
		_, _ = w.Write([]byte("\n"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	creds := &Credentials{RefreshToken: "ref", ClientID: "c", ClientSecret: "s"}
	_, err := (&AuthManager{httpClient: newDefaultHTTPClient()}).refreshOIDCToken(ctx, creds, srv.URL)
	if err == nil {
		t.Fatal("expected error from context timeout")
	}
}

func TestRefreshSocialToken_RespectsContext(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"accessToken":  "tok",
			"refreshToken": "ref",
			"expiresIn":    3600,
		})
		_, _ = w.Write([]byte("\n"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	creds := &Credentials{RefreshToken: "ref"}
	_, err := (&AuthManager{httpClient: newDefaultHTTPClient()}).refreshSocialToken(ctx, creds, srv.URL)
	if err == nil {
		t.Fatal("expected error from context timeout")
	}
}

func TestDefaultEndpoints(t *testing.T) {
	oidc := defaultOIDCEndpoint("us-west-2")
	if oidc != "https://oidc.us-west-2.amazonaws.com/token" {
		t.Errorf("defaultOIDCEndpoint = %q", oidc)
	}

	social := defaultSocialEndpoint("eu-west-1")
	if social != "https://prod.eu-west-1.auth.desktop.kiro.dev/refreshToken" {
		t.Errorf("defaultSocialEndpoint = %q", social)
	}

	socialDefault := defaultSocialEndpoint("")
	if socialDefault != "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken" {
		t.Errorf("defaultSocialEndpoint('') = %q, want us-east-1 fallback", socialDefault)
	}
}

func TestNewAuthManager(t *testing.T) {
	mgr := NewAuthManager("/tmp/nonexistent-test.sqlite3")
	if mgr == nil {
		t.Fatal("NewAuthManager returned nil")
	}
	// GetToken should fail gracefully when DB doesn't exist
	_, err := mgr.GetToken(context.Background())
	if err == nil {
		t.Error("expected error for nonexistent DB, got nil")
	}
}
