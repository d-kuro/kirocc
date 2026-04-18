package kiroclient

import (
	"encoding/json/v2"
	"fmt"
	"mime"
	"net/http"
	"strings"
)

// UpstreamError is returned when the Kiro API responds with an HTTP error
// (any non-success status) or an unexpected Content-Type on a 200 response.
// Callers can use errors.As to extract structured fields for logging.
type UpstreamError struct {
	Status      int    // HTTP status code
	ContentType string // Content-Type header value
	Exception   string // AWS exception class (normalized, may be "")
	Body        string // response body (up to 8 KiB)
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("kiro api: status=%d content_type=%q exception=%q: %s",
		e.Status, e.ContentType, e.Exception, e.Body)
}

// parseAWSExceptionType extracts the AWS exception type from an error body.
// AWS JSON 1.0 errors encode the exception class as "__type", optionally
// prefixed by a shape name ("com.amazonaws...#ThrottlingException").
// Returns "" if the body cannot be parsed.
func parseAWSExceptionType(body string) string {
	if body == "" {
		return ""
	}
	var m struct {
		Type1 string `json:"__type"`
		Type2 string `json:"type"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return ""
	}
	t := m.Type1
	if t == "" {
		t = m.Type2
	}
	if t == "" {
		t = m.Code
	}
	return normalizeAWSExceptionType(t)
}

// isRetryableAWSException reports whether an AWS exception type is transient
// and worth retrying (modeled after the AWS SDK retry policy).
func isRetryableAWSException(exType string) bool {
	switch exType {
	case "ThrottlingException",
		"TooManyRequestsException",
		"ServiceUnavailableException",
		"InternalServerException",
		"InternalFailureException",
		"InternalServerError":
		return true
	}
	return false
}

// normalizeAWSExceptionType strips namespace prefixes and hostname suffixes
// from an AWS exception type string. AWS uses two formats:
//   - JSON __type: "com.amazon.coral.service#ThrottlingException"
//   - Header X-Amzn-ErrorType: "ThrottlingException:http://example.com"
//
// This function handles both by stripping after '#' and before ':'.
func normalizeAWSExceptionType(raw string) string {
	if raw == "" {
		return ""
	}
	// Strip namespace prefix (e.g. "com.amazon.coral.service#ThrottlingException").
	if i := strings.LastIndexByte(raw, '#'); i >= 0 {
		raw = raw[i+1:]
	}
	// Strip hostname suffix (e.g. "ThrottlingException:http://example.com").
	if colon, _, ok := strings.Cut(raw, ":"); ok {
		raw = colon
	}
	return raw
}

// resolveAWSException determines the AWS exception type from the response,
// checking the body first, then falling back to the X-Amzn-ErrorType header.
func resolveAWSException(body string, header http.Header) string {
	if exType := parseAWSExceptionType(body); exType != "" {
		return exType
	}
	return normalizeAWSExceptionType(header.Get("X-Amzn-ErrorType"))
}

// isEventStreamContentType reports whether ct matches the AWS event stream
// content type (with or without parameters such as "; charset=...").
func isEventStreamContentType(ct string) bool {
	const want = "application/vnd.amazon.eventstream"
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return strings.EqualFold(mt, want)
}
