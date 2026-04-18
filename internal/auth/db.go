package auth

import (
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"
)

// Credentials holds authentication credentials read from Kiro CLI's SQLite database.
type Credentials struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
	Region       string // API region
	SSORegion    string // OIDC region (may differ from API region)
	ClientID     string
	ClientSecret string
	ProfileARN   string // from state table, key "api.codewhisperer.profile"
	AuthType     string // "social" or "idc" — determined by which token key was found
}

// ErrNoCredentials is returned when no token key is found in the database.
var ErrNoCredentials = errors.New("no kiro credentials found")

// OpenDB opens the Kiro CLI SQLite database at path in read-only mode.
func OpenDB(path string) (*sql.DB, error) {
	// Opaque keeps the path literal (no percent-encoding of / or special chars).
	dsn := (&url.URL{Scheme: "file", Opaque: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite db: %w", err)
	}
	return db, nil
}

// ReadCredentials reads authentication credentials from the Kiro CLI SQLite database.
func ReadCredentials(db *sql.DB) (*Credentials, error) {
	creds := &Credentials{}

	// Read token from auth_kv table.
	// Priority: kirocli:social → kirocli:odic/oidc → codewhisperer:odic/oidc
	tokenJSON, authType, err := findFirstAuthKV(db, []string{
		"kirocli:social:token",
		"kirocli:odic:token",
		"kirocli:oidc:token",
		"codewhisperer:odic:token",
		"codewhisperer:oidc:token",
	})
	if err != nil {
		return nil, err
	}
	creds.AuthType = authType

	// Token JSON may use camelCase (accessToken) or snake_case (access_token).
	var tokenData struct {
		AccessToken   string `json:"accessToken"`
		RefreshToken  string `json:"refreshToken"`
		ExpiresAt     any    `json:"expiresAt"`
		AccessTokenS  string `json:"access_token"`
		RefreshTokenS string `json:"refresh_token"`
		ExpiresAtS    any    `json:"expires_at"`
		Region        string `json:"region"`
	}
	if err := json.Unmarshal([]byte(tokenJSON), &tokenData); err != nil {
		return nil, fmt.Errorf("parse token JSON: %w", err)
	}
	creds.AccessToken = coalesce(tokenData.AccessToken, tokenData.AccessTokenS)
	creds.RefreshToken = coalesce(tokenData.RefreshToken, tokenData.RefreshTokenS)
	creds.ExpiresAt = parseExpiresAt(tokenData.ExpiresAt, tokenData.ExpiresAtS)

	// Read device registration.
	// Try both hyphen and underscore variants, and both odic/oidc spellings.
	regJSON, _, err := findFirstAuthKV(db, []string{
		"kirocli:social:device-registration",
		"kirocli:odic:device-registration",
		"kirocli:oidc:device-registration",
		"codewhisperer:odic:device-registration",
		"codewhisperer:oidc:device-registration",
		"kirocli:social:device_registration",
		"kirocli:odic:device_registration",
		"kirocli:oidc:device_registration",
		"codewhisperer:odic:device_registration",
		"codewhisperer:oidc:device_registration",
	})
	if err != nil && !errors.Is(err, ErrNoCredentials) {
		return nil, err
	}
	if regJSON != "" {
		var regData struct {
			ClientID      string `json:"clientId"`
			ClientSecret  string `json:"clientSecret"`
			ClientIDS     string `json:"client_id"`
			ClientSecretS string `json:"client_secret"`
		}
		if err := json.Unmarshal([]byte(regJSON), &regData); err != nil {
			return nil, fmt.Errorf("parse device registration JSON: %w", err)
		}
		creds.ClientID = coalesce(regData.ClientID, regData.ClientIDS)
		creds.ClientSecret = coalesce(regData.ClientSecret, regData.ClientSecretS)
	}

	// Region: prefer token's region, then state table.
	stateRegion, stateErr := readState(db, "auth.idc.region")
	if stateErr != nil && !errors.Is(stateErr, sql.ErrNoRows) {
		return nil, stateErr
	}
	if tokenData.Region != "" {
		creds.Region = tokenData.Region
		creds.SSORegion = tokenData.Region
	} else {
		creds.Region = stateRegion
		creds.SSORegion = stateRegion
	}

	// Read profile ARN from state table.
	// May be a JSON object {"arn":"...","profile_name":"..."} or a plain string.
	profileRaw, profileErr := readState(db, "api.codewhisperer.profile")
	if profileErr != nil && !errors.Is(profileErr, sql.ErrNoRows) {
		return nil, profileErr
	}
	creds.ProfileARN = extractProfileARN(profileRaw)

	return creds, nil
}

// findFirstAuthKV fetches all matching keys in a single query, then returns
// the value of the highest-priority key (earliest in the keys slice).
func findFirstAuthKV(db *sql.DB, keys []string) (string, string, error) {
	if len(keys) == 0 {
		return "", "", ErrNoCredentials
	}

	placeholders := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, k := range keys {
		placeholders[i] = "?"
		args[i] = k
	}
	query := `SELECT key, value FROM auth_kv WHERE key IN (` + strings.Join(placeholders, ",") + `)`

	rows, err := db.Query(query, args...)
	if err != nil {
		return "", "", fmt.Errorf("query auth_kv: %w", err)
	}
	defer func() { _ = rows.Close() }()

	found := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return "", "", fmt.Errorf("scan auth_kv: %w", err)
		}
		found[k] = v
	}
	if err := rows.Err(); err != nil {
		return "", "", fmt.Errorf("iterate auth_kv: %w", err)
	}

	// Return the first match in priority order.
	for _, key := range keys {
		if value, ok := found[key]; ok {
			authType := "idc"
			if strings.Contains(key, ":social:") {
				authType = "social"
			}
			return value, authType, nil
		}
	}
	return "", "", ErrNoCredentials
}

// readState reads a single value from the state table by key.
// Values may be stored as JSON strings (with quotes), so we attempt to unquote them.
func readState(db *sql.DB, key string) (string, error) {
	var value string
	err := db.QueryRow(`SELECT value FROM state WHERE key = ?`, key).Scan(&value)
	if err != nil {
		return "", err
	}
	// Try to JSON-unmarshal as a string to strip quotes.
	var unquoted string
	if err := json.Unmarshal([]byte(value), &unquoted); err == nil {
		return unquoted, nil
	}
	return value, nil
}
