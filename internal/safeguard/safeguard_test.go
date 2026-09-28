package safeguard

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/d-kuro/kirocc/internal/respconv"
)

func noul(v float64) answer { return answer{Type: "noul", Noul: &v} }

// choiceAnswer builds a verdict Choice answer, optionally with the generalizable
// noul (pass a negative gen to omit it).
func choiceAnswer(choice string, confidence, gen float64) map[string]answer {
	m := map[string]answer{
		questionVerdict: {Type: "choice", Choice: choice, Confidence: &confidence},
	}
	if gen >= 0 {
		m[questionGeneralizes] = noul(gen)
	}
	return m
}

func TestResolveChoice(t *testing.T) {
	tests := []struct {
		name       string
		choice     string
		confidence float64
		wantSkip   bool
		wantOut    string // checked only when !wantSkip
	}{
		{"confident not_flagged clears", verdictNotFlagged, 0.95, false, respconv.SafeguardNotFlagged},
		{"confident flagged blocks", verdictFlagged, 0.9, false, respconv.SafeguardFlagged},
		{"explicit uncertain defers", verdictUncertain, 0.99, true, ""},
		{"low-confidence not_flagged defers", verdictNotFlagged, 0.4, true, ""},
		{"low-confidence flagged defers", verdictFlagged, 0.5, true, ""},
		{"unknown option defers", "something_else", 0.99, true, ""},
	}
	c := testClient()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := c.resolve(choiceAnswer(tt.choice, tt.confidence, 0.5))
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if tt.wantSkip {
				if !got.Skip {
					t.Fatalf("want Skip, got outcome %q", got.Outcome)
				}
				return
			}
			if got.Skip {
				t.Fatalf("want outcome %q, got Skip", tt.wantOut)
			}
			if got.Outcome != tt.wantOut {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tt.wantOut)
			}
			if got.Outcome == respconv.SafeguardFlagged && got.Explanation == "" {
				t.Error("a flagged verdict must carry an explanation")
			}
		})
	}
}

// A Choice answer with no confidence field defers rather than acting: absence
// is treated as "not confident".
func TestResolveMissingConfidenceDefers(t *testing.T) {
	got, _, err := testClient().resolve(map[string]answer{
		questionVerdict: {Type: "choice", Choice: verdictNotFlagged},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !got.Skip {
		t.Fatalf("want Skip when confidence is absent, got outcome %q", got.Outcome)
	}
}

// A missing verdict answer is an error (nothing to resolve), which the caller
// turns into a defer.
func TestResolveMissingVerdictErrors(t *testing.T) {
	if _, _, err := testClient().resolve(map[string]answer{}); err == nil {
		t.Fatal("want error for a missing verdict answer")
	}
}

// The generalizable noul above the cap makes the verdict shape-cacheable.
func TestResolveGeneralizable(t *testing.T) {
	// gen 0.9 >= GeneralizableCap 0.6 -> generalizable true.
	if _, gen, err := testClient().resolve(choiceAnswer(verdictNotFlagged, 0.95, 0.9)); err != nil || !gen {
		t.Fatalf("gen = %v, err %v; want generalizable", gen, err)
	}
	// gen 0.1 below the cap -> false.
	if _, gen, err := testClient().resolve(choiceAnswer(verdictNotFlagged, 0.95, 0.1)); err != nil || gen {
		t.Fatalf("gen = %v, err %v; want not generalizable", gen, err)
	}
}

// With no key, New still returns a usable client whose default provider is the
// keyless, free OpenCode Zen endpoint -- safeguards are on by default. A key
// prepends the paid TypeSafe provider as primary.
func TestNewDefaultProviderChain(t *testing.T) {
	keyless := New("")
	if keyless == nil {
		t.Fatal("New(\"\") must return a keyless client, not nil")
	}
	if len(keyless.providers) != 1 || keyless.providers[0].name != "opencode" {
		t.Fatalf("keyless chain = %+v, want [opencode]", keyless.providers)
	}
	if keyless.providers[0].apiKey != "" {
		t.Error("the default opencode provider must be keyless")
	}

	keyed := New("ts-key")
	if len(keyed.providers) != 2 || keyed.providers[0].name != "opencode" || keyed.providers[1].name != "typesafe" {
		t.Fatalf("keyed chain = %+v, want [opencode, typesafe]", keyed.providers)
	}
	if keyed.providers[1].apiKey != "ts-key" {
		t.Error("the typesafe provider must carry the key")
	}
	if keyed.providers[0].apiKey != "" {
		t.Error("the leading opencode provider must stay keyless (free tier)")
	}
}

func testClient() *Client { return New("k") }

// clearAnswersJSON is a Jev response body that decides to a clear: a confident
// not_flagged verdict. generalizable controls the generalizable answer, which
// selects shape-vs-literal caching.
func clearAnswersJSON() string { return clearAnswers(false) }

func clearAnswers(generalizable bool) string {
	g := "0.0"
	if generalizable {
		g = "1.0"
	}
	return `{"model":"jev-latest","answers":{` +
		`"verdict":{"type":"choice","choice":"not_flagged","confidence":0.95,` +
		`"probabilities":{"not_flagged":0.95,"flagged":0.03,"uncertain":0.02}},` +
		`"generalizable":{"type":"noul","noul":` + g + `}}}`
}

// clientFor builds a client with a single test provider and the verdict cache
// disabled, so transport tests see one request per judge call with no failover
// to a real endpoint.
func clientFor(t *testing.T, url string) *Client {
	t.Helper()
	return New("k", WithProviders(NewProvider("test", url, "jev-latest", "k")),
		WithHTTPClient(&http.Client{Timeout: 5 * time.Second}), WithoutCache())
}

// cachingClientFor builds a single-provider client with the verdict cache
// enabled, for the cache tests below.
func cachingClientFor(t *testing.T, url string) *Client {
	t.Helper()
	return New("k", WithProviders(NewProvider("test", url, "jev-latest", "k")),
		WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
}

// A 5xx is transient: judge retries once and the second try's 200 wins.
func TestJudgeRetriesOn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, clearAnswersJSON())
	}))
	defer srv.Close()

	got, err := clientFor(t, srv.URL).judge(context.Background(), nil, ToolUse{ID: "t1", Name: "Bash", Input: `{"command":"x"}`})
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if got.Skip || got.Outcome != respconv.SafeguardNotFlagged {
		t.Fatalf("status = %+v, want not_flagged", got)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("calls = %d, want 2 (one retry)", n)
	}
}

// A 4xx is not retried: retrying a bad request cannot help, so it errors after
// exactly one call and the caller defers.
func TestJudgeDoesNotRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := clientFor(t, srv.URL).judge(context.Background(), nil, ToolUse{ID: "t1", Name: "Bash", Input: `{"command":"x"}`})
	if err == nil {
		t.Fatal("want error on 401")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("calls = %d, want 1 (no retry)", n)
	}
}

// Classify judges several tool uses concurrently and returns a verdict for each,
// with a failing one degraded to Skip without poisoning the others.
func TestClassifyConcurrentMixed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// The one tool whose input says "fail" gets a 400 (no retry, -> Skip).
		if bytes.Contains(body, []byte("fail")) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, clearAnswersJSON())
	}))
	defer srv.Close()

	uses := []ToolUse{
		{ID: "a", Name: "Bash", Input: `{"command":"ok1"}`},
		{ID: "b", Name: "Bash", Input: `{"command":"fail"}`},
		{ID: "c", Name: "Bash", Input: `{"command":"ok2"}`},
	}
	out, err := clientFor(t, srv.URL).Classify(context.Background(), nil, uses)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("verdicts = %d, want 3", len(out))
	}
	if out["a"].Outcome != respconv.SafeguardNotFlagged || out["c"].Outcome != respconv.SafeguardNotFlagged {
		t.Fatalf("a/c should clear: %+v", out)
	}
	if !out["b"].Skip {
		t.Fatalf("b should Skip on 400, got %+v", out["b"])
	}
}

// A generalizable NotFlagged verdict is cached by command shape, so a different
// argument to the same read hits without a second Jev call.
func TestCacheGeneralizableSharesShape(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, clearAnswers(true))
	}))
	defer srv.Close()
	c := cachingClientFor(t, srv.URL)

	if _, err := c.judge(context.Background(), nil, ToolUse{ID: "1", Name: "Bash", Input: `{"command":"grep foo a.go"}`}); err != nil {
		t.Fatalf("judge 1: %v", err)
	}
	if _, err := c.judge(context.Background(), nil, ToolUse{ID: "2", Name: "Bash", Input: `{"command":"grep foo b.go"}`}); err != nil {
		t.Fatalf("judge 2: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("Jev calls = %d, want 1 (shape cache hit on the second grep)", n)
	}
}

// A non-generalizable verdict is cached by exact input: different args to a
// risky command each cost a Jev call, but an identical repeat hits.
func TestCacheNonGeneralizableIsLiteral(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, clearAnswers(false)) // not generalizable
	}))
	defer srv.Close()
	c := cachingClientFor(t, srv.URL)

	// Two different-arg rm-shaped calls: no shape sharing -> two Jev calls.
	_, _ = c.judge(context.Background(), nil, ToolUse{ID: "1", Name: "Bash", Input: `{"command":"rm a"}`})
	_, _ = c.judge(context.Background(), nil, ToolUse{ID: "2", Name: "Bash", Input: `{"command":"rm b"}`})
	if n := calls.Load(); n != 2 {
		t.Fatalf("Jev calls = %d, want 2 (no cross-arg sharing for non-generalizable)", n)
	}
	// An exact repeat of the first hits the literal cache.
	_, _ = c.judge(context.Background(), nil, ToolUse{ID: "3", Name: "Bash", Input: `{"command":"rm a"}`})
	if n := calls.Load(); n != 2 {
		t.Fatalf("Jev calls = %d, want 2 (identical rm should be a literal hit)", n)
	}
}

// A changed policy misses the cache: the same call under a different policy can
// warrant a different verdict.
func TestCachePolicyScoped(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, clearAnswers(true))
	}))
	defer srv.Close()
	c := cachingClientFor(t, srv.URL)

	u := ToolUse{ID: "1", Name: "Bash", Input: `{"command":"grep foo a.go"}`}
	_, _ = c.judge(context.Background(), jsontext.Value(`{"v":1,"mode":"a"}`), u)
	_, _ = c.judge(context.Background(), jsontext.Value(`{"v":1,"mode":"b"}`), u)
	if n := calls.Load(); n != 2 {
		t.Fatalf("Jev calls = %d, want 2 (policy change must miss)", n)
	}
}

// A Skip verdict is never cached: it must stay retryable next turn.
func TestCacheNeverStoresSkip(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// An explicit "uncertain" verdict -> Skip.
		_, _ = io.WriteString(w, `{"model":"jev-latest","answers":{`+
			`"verdict":{"type":"choice","choice":"uncertain","confidence":0.9,`+
			`"probabilities":{"not_flagged":0.05,"flagged":0.05,"uncertain":0.9}},`+
			`"generalizable":{"type":"noul","noul":1.0}}}`)
	}))
	defer srv.Close()
	c := cachingClientFor(t, srv.URL)

	u := ToolUse{ID: "1", Name: "Bash", Input: `{"command":"make build"}`}
	st, _ := c.judge(context.Background(), nil, u)
	if !st.Skip {
		t.Fatalf("want Skip, got %+v", st)
	}
	_, _ = c.judge(context.Background(), nil, u)
	if n := calls.Load(); n != 2 {
		t.Fatalf("Jev calls = %d, want 2 (Skip must not be cached)", n)
	}
}

// Failover: the first provider erroring falls through to the second, which
// answers. Verified by pointing provider[0] at a server that 500s and
// provider[1] at one that clears.
func TestFailoverToSecondProvider(t *testing.T) {
	var p0, p1 atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p0.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p1.Add(1)
		_, _ = io.WriteString(w, clearAnswers(false))
	}))
	defer good.Close()

	c := New("k",
		WithProviders(NewProvider("bad", bad.URL, "m", "k"), NewProvider("good", good.URL, "m", "")),
		WithHTTPClient(&http.Client{Timeout: 5 * time.Second}), WithoutCache())
	st, err := c.judge(context.Background(), nil, ToolUse{ID: "t1", Name: "Bash", Input: `{"command":"x"}`})
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if st.Skip || st.Outcome != respconv.SafeguardNotFlagged {
		t.Fatalf("status = %+v, want not_flagged from the second provider", st)
	}
	if p1.Load() == 0 {
		t.Fatal("the second provider was never tried")
	}
}

// Failover disabled: only the first provider is tried; its error is returned
// (and the caller defers) without touching the second.
func TestFailoverDisabledStopsAtFirst(t *testing.T) {
	var p1 atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p1.Add(1)
		_, _ = io.WriteString(w, clearAnswers(false))
	}))
	defer good.Close()

	c := New("k",
		WithProviders(NewProvider("bad", bad.URL, "m", "k"), NewProvider("good", good.URL, "m", "")),
		WithFailover(false),
		WithHTTPClient(&http.Client{Timeout: 5 * time.Second}), WithoutCache())
	_, err := c.judge(context.Background(), nil, ToolUse{ID: "t1", Name: "Bash", Input: `{"command":"x"}`})
	if err == nil {
		t.Fatal("want an error from the only (failing) provider")
	}
	if p1.Load() != 0 {
		t.Fatal("the second provider must not be tried when failover is disabled")
	}
}

// A keyless provider sends no Authorization header.
func TestKeylessProviderSendsNoAuth(t *testing.T) {
	var sawAuth atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}
		_, _ = io.WriteString(w, clearAnswers(false))
	}))
	defer srv.Close()

	c := New("", WithProviders(NewProvider("free", srv.URL, "jev-1.13-free", "")),
		WithHTTPClient(&http.Client{Timeout: 5 * time.Second}), WithoutCache())
	if _, err := c.judge(context.Background(), nil, ToolUse{ID: "t1", Name: "Bash", Input: `{"command":"x"}`}); err != nil {
		t.Fatalf("judge: %v", err)
	}
	if sawAuth.Load() {
		t.Error("a keyless provider must not send an Authorization header")
	}
}
