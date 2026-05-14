package auth

import "testing"

func TestExtractRegionFromARN(t *testing.T) {
	tests := []struct {
		name string
		arn  string
		want string
	}{
		{"empty", "", ""},
		{"valid social ARN", "arn:aws:codewhisperer:us-east-1:123456789012:profile/X", "us-east-1"},
		{"valid eu-west-1", "arn:aws:codewhisperer:eu-west-1:123:profile/y", "eu-west-1"},
		{"too few segments", "arn:aws:codewhisperer", ""},
		{"missing arn prefix", "not:aws:codewhisperer:us-east-1:123:profile/x", ""},
		{"empty region segment", "arn:aws:codewhisperer::123:profile/x", ""},
		{"non-arn string", "not-an-arn", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractRegionFromARN(tt.arn); got != tt.want {
				t.Errorf("extractRegionFromARN(%q) = %q, want %q", tt.arn, got, tt.want)
			}
		})
	}
}

func TestResolveRegion(t *testing.T) {
	const (
		arnUSEast = "arn:aws:codewhisperer:us-east-1:123:profile/x"
		arnEUWest = "arn:aws:codewhisperer:eu-west-1:123:profile/x"
	)
	tests := []struct {
		name        string
		tokenRegion string
		tokenARN    string
		stateRegion string
		stateARN    string
		want        string
	}{
		{"token region wins over everything", "ap-northeast-1", arnUSEast, "us-west-2", arnEUWest, "ap-northeast-1"},
		{"token ARN beats state side when token region empty", "", arnUSEast, "us-west-2", arnEUWest, "us-east-1"},
		{"state ARN beats stateRegion (API region from profile ARN)", "", "", "us-west-2", arnEUWest, "eu-west-1"},
		{"stateRegion used only when no ARN anywhere", "", "", "us-west-2", "", "us-west-2"},
		{"state ARN used when others empty", "", "", "", arnEUWest, "eu-west-1"},
		{"all empty falls back to us-east-1", "", "", "", "", "us-east-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveRegion(tt.tokenRegion, tt.tokenARN, tt.stateRegion, tt.stateARN)
			if got != tt.want {
				t.Errorf("resolveRegion(%q,%q,%q,%q) = %q, want %q",
					tt.tokenRegion, tt.tokenARN, tt.stateRegion, tt.stateARN, got, tt.want)
			}
		})
	}
}
