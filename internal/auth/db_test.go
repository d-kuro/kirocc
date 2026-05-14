package auth

import (
	"database/sql"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS auth_kv (key TEXT PRIMARY KEY, value TEXT)`)
	if err != nil {
		t.Fatalf("failed to create auth_kv table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS state (key TEXT PRIMARY KEY, value TEXT)`)
	if err != nil {
		t.Fatalf("failed to create state table: %v", err)
	}
	return db
}

func TestReadCredentials(t *testing.T) {
	tests := []struct {
		name    string
		authKV  map[string]string // key → value for auth_kv table
		state   map[string]string // key → value for state table
		wantErr bool
		check   func(t *testing.T, c *Credentials)
	}{
		{
			name: "kiro OIDC token",
			authKV: map[string]string{
				"kirocli:odic:token": `{"accessToken":"acc123","refreshToken":"ref456","expiresAt":9999999999}`,
			},
			check: func(t *testing.T, c *Credentials) {
				if c.AccessToken != "acc123" {
					t.Errorf("AccessToken = %q, want %q", c.AccessToken, "acc123")
				}
				if c.RefreshToken != "ref456" {
					t.Errorf("RefreshToken = %q, want %q", c.RefreshToken, "ref456")
				}
				if c.ExpiresAt != 9999999999 {
					t.Errorf("ExpiresAt = %d, want %d", c.ExpiresAt, 9999999999)
				}
			},
		},
		{
			name: "fallback to codewhisperer token",
			authKV: map[string]string{
				"codewhisperer:odic:token": `{"accessToken":"cw_acc","refreshToken":"cw_ref","expiresAt":1111111111}`,
			},
			check: func(t *testing.T, c *Credentials) {
				if c.AccessToken != "cw_acc" {
					t.Errorf("AccessToken = %q, want %q", c.AccessToken, "cw_acc")
				}
			},
		},
		{
			name: "device registration (underscore key)",
			authKV: map[string]string{
				"kirocli:odic:token":               `{"accessToken":"a","refreshToken":"r","expiresAt":1}`,
				"kirocli:odic:device_registration": `{"clientId":"cid123","clientSecret":"csec456"}`,
			},
			check: func(t *testing.T, c *Credentials) {
				if c.ClientID != "cid123" {
					t.Errorf("ClientID = %q, want %q", c.ClientID, "cid123")
				}
				if c.ClientSecret != "csec456" {
					t.Errorf("ClientSecret = %q, want %q", c.ClientSecret, "csec456")
				}
			},
		},
		{
			name: "device registration (hyphen key)",
			authKV: map[string]string{
				"kirocli:odic:token":               `{"accessToken":"a","refreshToken":"r","expiresAt":1}`,
				"kirocli:odic:device-registration": `{"clientId":"hyp_cid","clientSecret":"hyp_csec"}`,
			},
			check: func(t *testing.T, c *Credentials) {
				if c.ClientID != "hyp_cid" {
					t.Errorf("ClientID = %q, want %q", c.ClientID, "hyp_cid")
				}
				if c.ClientSecret != "hyp_csec" {
					t.Errorf("ClientSecret = %q, want %q", c.ClientSecret, "hyp_csec")
				}
			},
		},
		{
			name: "region from state table",
			authKV: map[string]string{
				"kirocli:odic:token": `{"accessToken":"a","refreshToken":"r","expiresAt":1}`,
			},
			state: map[string]string{
				"auth.idc.region": "us-east-1",
			},
			check: func(t *testing.T, c *Credentials) {
				if c.Region != "us-east-1" {
					t.Errorf("Region = %q, want %q", c.Region, "us-east-1")
				}
				if c.SSORegion != "us-east-1" {
					t.Errorf("SSORegion = %q, want %q", c.SSORegion, "us-east-1")
				}
			},
		},
		{
			name: "region JSON-quoted in state",
			authKV: map[string]string{
				"kirocli:odic:token": `{"accessToken":"a","refreshToken":"r","expiresAt":1}`,
			},
			state: map[string]string{
				"auth.idc.region": `"us-east-1"`,
			},
			check: func(t *testing.T, c *Credentials) {
				if c.Region != "us-east-1" {
					t.Errorf("Region = %q, want %q", c.Region, "us-east-1")
				}
			},
		},
		{
			name: "token region takes precedence over state",
			authKV: map[string]string{
				"kirocli:odic:token": `{"accessToken":"a","refreshToken":"r","expiresAt":1,"region":"eu-west-1"}`,
			},
			state: map[string]string{
				"auth.idc.region": "us-east-1",
			},
			check: func(t *testing.T, c *Credentials) {
				if c.Region != "eu-west-1" {
					t.Errorf("Region = %q, want %q (token region should take precedence)", c.Region, "eu-west-1")
				}
				if c.SSORegion != "eu-west-1" {
					t.Errorf("SSORegion = %q, want %q", c.SSORegion, "eu-west-1")
				}
			},
		},
		{
			name: "profile ARN plain string",
			authKV: map[string]string{
				"kirocli:odic:token": `{"accessToken":"a","refreshToken":"r","expiresAt":1}`,
			},
			state: map[string]string{
				"api.codewhisperer.profile": "arn:aws:codewhisperer:us-east-1:123456789:profile/test",
			},
			check: func(t *testing.T, c *Credentials) {
				if c.ProfileARN != "arn:aws:codewhisperer:us-east-1:123456789:profile/test" {
					t.Errorf("ProfileARN = %q", c.ProfileARN)
				}
			},
		},
		{
			name: "profile ARN JSON-quoted",
			authKV: map[string]string{
				"kirocli:odic:token": `{"accessToken":"a","refreshToken":"r","expiresAt":1}`,
			},
			state: map[string]string{
				"api.codewhisperer.profile": `"arn:aws:codewhisperer:us-east-1:123456789:profile/test"`,
			},
			check: func(t *testing.T, c *Credentials) {
				if c.ProfileARN != "arn:aws:codewhisperer:us-east-1:123456789:profile/test" {
					t.Errorf("ProfileARN = %q, want unquoted ARN", c.ProfileARN)
				}
			},
		},
		{
			name: "profile ARN as JSON object",
			authKV: map[string]string{
				"kirocli:odic:token": `{"accessToken":"a","refreshToken":"r","expiresAt":1}`,
			},
			state: map[string]string{
				"api.codewhisperer.profile": `{"arn":"arn:aws:codewhisperer:us-east-1:123:profile/obj","profile_name":"test"}`,
			},
			check: func(t *testing.T, c *Credentials) {
				if c.ProfileARN != "arn:aws:codewhisperer:us-east-1:123:profile/obj" {
					t.Errorf("ProfileARN = %q, want ARN from JSON object", c.ProfileARN)
				}
			},
		},
		{
			name:    "no token returns ErrNoCredentials",
			authKV:  nil,
			wantErr: true,
			check: func(t *testing.T, c *Credentials) {
				// not called
			},
		},
		{
			name: "snake_case token fields",
			authKV: map[string]string{
				"kirocli:odic:token": `{"access_token":"snake_acc","refresh_token":"snake_ref","expires_at":8888888888}`,
			},
			check: func(t *testing.T, c *Credentials) {
				if c.AccessToken != "snake_acc" {
					t.Errorf("AccessToken = %q, want %q", c.AccessToken, "snake_acc")
				}
				if c.RefreshToken != "snake_ref" {
					t.Errorf("RefreshToken = %q, want %q", c.RefreshToken, "snake_ref")
				}
				if c.ExpiresAt != 8888888888 {
					t.Errorf("ExpiresAt = %d, want %d", c.ExpiresAt, 8888888888)
				}
			},
		},
		{
			name: "social token sets AuthType",
			authKV: map[string]string{
				"kirocli:social:token": `{"accessToken":"social_acc","refreshToken":"social_ref","expiresAt":7777777777}`,
			},
			check: func(t *testing.T, c *Credentials) {
				if c.AuthType != "social" {
					t.Errorf("AuthType = %q, want %q", c.AuthType, "social")
				}
				if c.AccessToken != "social_acc" {
					t.Errorf("AccessToken = %q, want %q", c.AccessToken, "social_acc")
				}
			},
		},
		{
			name: "social token with snake_case profile_arn populates region",
			authKV: map[string]string{
				"kirocli:social:token": `{"access_token":"a","refresh_token":"r","provider":"google","profile_arn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/X"}`,
			},
			check: func(t *testing.T, c *Credentials) {
				if c.AuthType != "social" {
					t.Errorf("AuthType = %q, want %q", c.AuthType, "social")
				}
				if c.Region != "us-east-1" {
					t.Errorf("Region = %q, want %q", c.Region, "us-east-1")
				}
				// SSORegion must remain empty when no explicit region is stored —
				// only ARN-derived values are in play, which should not satisfy
				// the OIDC refresh path's region check.
				if c.SSORegion != "" {
					t.Errorf("SSORegion = %q, want empty (ARN-derived region must not populate SSORegion)", c.SSORegion)
				}
				if c.ProfileARN != "arn:aws:codewhisperer:us-east-1:123456789012:profile/X" {
					t.Errorf("ProfileARN = %q", c.ProfileARN)
				}
			},
		},
		{
			name: "social token with camelCase profileArn",
			authKV: map[string]string{
				"kirocli:social:token": `{"accessToken":"a","refreshToken":"r","profileArn":"arn:aws:codewhisperer:eu-west-1:123:profile/Y"}`,
			},
			check: func(t *testing.T, c *Credentials) {
				if c.Region != "eu-west-1" {
					t.Errorf("Region = %q, want %q", c.Region, "eu-west-1")
				}
				if c.ProfileARN != "arn:aws:codewhisperer:eu-west-1:123:profile/Y" {
					t.Errorf("ProfileARN = %q", c.ProfileARN)
				}
			},
		},
		{
			name: "social token without region or ARN falls back to us-east-1",
			authKV: map[string]string{
				"kirocli:social:token": `{"accessToken":"a","refreshToken":"r"}`,
			},
			check: func(t *testing.T, c *Credentials) {
				if c.Region != "us-east-1" {
					t.Errorf("Region = %q, want %q", c.Region, "us-east-1")
				}
				if c.ProfileARN != "" {
					t.Errorf("ProfileARN = %q, want empty", c.ProfileARN)
				}
			},
		},
		{
			name: "token JSON profile_arn beats state ARN",
			authKV: map[string]string{
				"kirocli:social:token": `{"access_token":"a","refresh_token":"r","profile_arn":"arn:aws:codewhisperer:ap-northeast-1:111:profile/token"}`,
			},
			state: map[string]string{
				"api.codewhisperer.profile": "arn:aws:codewhisperer:eu-west-1:222:profile/state",
			},
			check: func(t *testing.T, c *Credentials) {
				if c.ProfileARN != "arn:aws:codewhisperer:ap-northeast-1:111:profile/token" {
					t.Errorf("ProfileARN = %q, want token JSON ARN", c.ProfileARN)
				}
				if c.Region != "ap-northeast-1" {
					t.Errorf("Region = %q, want %q", c.Region, "ap-northeast-1")
				}
			},
		},
		{
			name: "token JSON region beats ARN region",
			authKV: map[string]string{
				"kirocli:social:token": `{"accessToken":"a","refreshToken":"r","region":"eu-west-1","profile_arn":"arn:aws:codewhisperer:us-east-1:123:profile/X"}`,
			},
			check: func(t *testing.T, c *Credentials) {
				if c.Region != "eu-west-1" {
					t.Errorf("Region = %q, want %q (token region must win over ARN region)", c.Region, "eu-west-1")
				}
			},
		},
		{
			name: "IDC token without any region leaves SSORegion empty",
			authKV: map[string]string{
				"kirocli:odic:token": `{"accessToken":"a","refreshToken":"r","expiresAt":1}`,
			},
			// No state.auth.idc.region and no profile ARN.
			check: func(t *testing.T, c *Credentials) {
				if c.SSORegion != "" {
					t.Errorf("SSORegion = %q, want empty so refreshCredentials fails fast for misconfigured IDC", c.SSORegion)
				}
				if c.Region != "us-east-1" {
					t.Errorf("Region = %q, want %q (API region falls back)", c.Region, "us-east-1")
				}
			},
		},
		{
			name: "IDC token with state profile ARN does not derive SSORegion from ARN",
			authKV: map[string]string{
				"kirocli:odic:token": `{"accessToken":"a","refreshToken":"r","expiresAt":1}`,
			},
			state: map[string]string{
				"api.codewhisperer.profile": "arn:aws:codewhisperer:eu-west-1:123:profile/idc",
			},
			check: func(t *testing.T, c *Credentials) {
				if c.SSORegion != "" {
					t.Errorf("SSORegion = %q, want empty (ARN region must not leak into SSORegion)", c.SSORegion)
				}
				if c.Region != "eu-west-1" {
					t.Errorf("Region = %q, want %q (API region may use ARN)", c.Region, "eu-west-1")
				}
			},
		},
		{
			name: "IDC profile ARN region wins over SSO region for API endpoint",
			authKV: map[string]string{
				"kirocli:odic:token": `{"accessToken":"a","refreshToken":"r","expiresAt":1}`,
			},
			state: map[string]string{
				"auth.idc.region":           "us-east-1", // OIDC SSO region
				"api.codewhisperer.profile": "arn:aws:codewhisperer:eu-west-1:123:profile/idc",
			},
			check: func(t *testing.T, c *Credentials) {
				if c.Region != "eu-west-1" {
					t.Errorf("Region = %q, want %q (API region must come from profile ARN, not SSO region)", c.Region, "eu-west-1")
				}
				if c.SSORegion != "us-east-1" {
					t.Errorf("SSORegion = %q, want %q (SSO region from auth.idc.region)", c.SSORegion, "us-east-1")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupTestDB(t)
			defer func() { _ = db.Close() }()

			for k, v := range tt.authKV {
				if _, err := db.Exec(`INSERT INTO auth_kv (key, value) VALUES (?, ?)`, k, v); err != nil {
					t.Fatalf("insert auth_kv: %v", err)
				}
			}
			for k, v := range tt.state {
				if _, err := db.Exec(`INSERT INTO state (key, value) VALUES (?, ?)`, k, v); err != nil {
					t.Fatalf("insert state: %v", err)
				}
			}

			creds, err := ReadCredentials(db)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.check(t, creds)
		})
	}
}

func TestParseExpiresAt(t *testing.T) {
	tests := []struct {
		name string
		vals []any
		want int64
	}{
		{"float64", []any{float64(1234567890)}, 1234567890},
		{"int string", []any{"1234567890"}, 1234567890},
		{"float string", []any{"1234567890.5"}, 1234567890},
		{"ISO 8601", []any{"2025-01-01T00:00:00Z"}, 1735689600},
		{"ISO 8601 nano", []any{"2025-01-01T00:00:00.000Z"}, 1735689600},
		{"empty string", []any{""}, 0},
		{"nil", []any{nil}, 0},
		{"zero float", []any{float64(0)}, 0},
		{"fallback to second", []any{nil, float64(999)}, 999},
		{"first wins", []any{float64(111), float64(222)}, 111},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseExpiresAt(tt.vals...)
			if got != tt.want {
				t.Errorf("parseExpiresAt(%v) = %d, want %d", tt.vals, got, tt.want)
			}
		})
	}
}

func TestExtractProfileARN(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", ""},
		{"plain string", "arn:aws:codewhisperer:us-east-1:123:profile/test", "arn:aws:codewhisperer:us-east-1:123:profile/test"},
		{"json object", `{"arn":"arn:aws:codewhisperer:us-east-1:123:profile/obj","profile_name":"p"}`, "arn:aws:codewhisperer:us-east-1:123:profile/obj"},
		{"json object no arn", `{"profile_name":"p"}`, `{"profile_name":"p"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractProfileARN(tt.raw)
			if got != tt.want {
				t.Errorf("extractProfileARN(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestCoalesce(t *testing.T) {
	tests := []struct {
		a, b, want string
	}{
		{"a", "b", "a"},
		{"", "b", "b"},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := coalesce(tt.a, tt.b); got != tt.want {
			t.Errorf("coalesce(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestOpenDB(t *testing.T) {
	f, err := os.CreateTemp("", "kirocc-test-*.db")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	_ = f.Close()
	defer func() { _ = os.Remove(f.Name()) }()

	// Initialize the DB with required tables so read-only open works
	initDB, err := sql.Open("sqlite", f.Name())
	if err != nil {
		t.Fatalf("open for init: %v", err)
	}
	_, err = initDB.Exec(`CREATE TABLE IF NOT EXISTS auth_kv (key TEXT PRIMARY KEY, value TEXT)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_ = initDB.Close()

	db, err := OpenDB(f.Name())
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.Ping(); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestOpenDB_SpecialCharsInPath(t *testing.T) {
	// Paths with special characters (e.g. '?', '#', '%') should not break DSN parsing.
	dir := t.TempDir()
	specialDir := dir + "/path with?special#chars"
	if err := os.MkdirAll(specialDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dbPath := specialDir + "/data.sqlite3"

	// Initialize the DB.
	initDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open for init: %v", err)
	}
	_, err = initDB.Exec(`CREATE TABLE IF NOT EXISTS auth_kv (key TEXT PRIMARY KEY, value TEXT)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_ = initDB.Close()

	// OpenDB should handle the special characters in the path.
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB with special chars failed: %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.Ping(); err != nil {
		t.Errorf("Ping: %v", err)
	}
}
