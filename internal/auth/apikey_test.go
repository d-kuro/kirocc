package auth

import (
	"context"
	"testing"
)

func TestWithAPIKey_ShortCircuitsDBAndRefresh(t *testing.T) {
	// A path that cannot be opened proves the DB is never touched: if GetToken
	// consulted it, this would fail rather than return the key.
	m := NewAuthManager("/nonexistent/kirocc-test/data.sqlite3", WithAPIKey("ksk_test", ""))

	if !m.UsesAPIKey() {
		t.Fatal("UsesAPIKey() = false, want true")
	}

	creds, err := m.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if creds.AccessToken != "ksk_test" {
		t.Errorf("AccessToken = %q, want %q", creds.AccessToken, "ksk_test")
	}
	if creds.AuthType != AuthTypeAPIKey {
		t.Errorf("AuthType = %q, want %q", creds.AuthType, AuthTypeAPIKey)
	}
	if creds.Region != defaultAPIKeyRegion {
		t.Errorf("Region = %q, want %q", creds.Region, defaultAPIKeyRegion)
	}
	// An API key has no expiry to honour and no profile ARN to send; both must
	// stay zero so the refresh and profileArn paths remain unreachable.
	if creds.ExpiresAt != 0 {
		t.Errorf("ExpiresAt = %d, want 0", creds.ExpiresAt)
	}
	if creds.RefreshToken != "" {
		t.Errorf("RefreshToken = %q, want empty", creds.RefreshToken)
	}
	if creds.ProfileARN != "" {
		t.Errorf("ProfileARN = %q, want empty", creds.ProfileARN)
	}
}

// Repeated calls must keep working even though nothing is cached: an API key
// never expires from kirocc's point of view, so the token-validity buffer that
// governs database credentials must not apply to it.
func TestWithAPIKey_StableAcrossCalls(t *testing.T) {
	m := NewAuthManager("/nonexistent/kirocc-test/data.sqlite3", WithAPIKey("ksk_test", ""))
	for i := range 3 {
		creds, err := m.GetToken(context.Background())
		if err != nil {
			t.Fatalf("GetToken call %d: %v", i, err)
		}
		if creds.AccessToken != "ksk_test" {
			t.Fatalf("call %d: AccessToken = %q", i, creds.AccessToken)
		}
	}
}

func TestWithAPIKey_RegionOverride(t *testing.T) {
	m := NewAuthManager("", WithAPIKey("ksk_test", "eu-west-1"))
	creds, err := m.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if creds.Region != "eu-west-1" {
		t.Errorf("Region = %q, want %q", creds.Region, "eu-west-1")
	}
}

// An empty key must be inert, so that exporting KIRO_API_KEY= (or leaving the
// flag unset) leaves the database path in effect rather than half-enabling a
// credential-less API-key mode.
func TestWithAPIKey_EmptyKeyIsIgnored(t *testing.T) {
	m := NewAuthManager("/nonexistent/kirocc-test/data.sqlite3", WithAPIKey("", "us-east-1"))
	if m.UsesAPIKey() {
		t.Error("UsesAPIKey() = true for an empty key, want false")
	}
	if _, err := m.GetToken(context.Background()); err == nil {
		t.Error("GetToken succeeded with no key and an unreadable DB, want error")
	}
}
