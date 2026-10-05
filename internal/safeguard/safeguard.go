// Package safeguard answers auto mode's server-side classifier requests with a
// judgment from TypeSafe's Jev, so Claude Code does not have to issue its own.
//
// Claude Code's local classifier pins Claude Sonnet 5 regardless of the
// session's model and bills one request per gated action. On a Kiro-backed
// session that measured ~28% of a session's credits. When the endpoint answers
// the `safeguards` request field, the client uses those verdicts instead and
// makes no classifier requests at all.
//
// Failure is deliberately cheap rather than dangerous: anything short of a
// confident answer becomes a per-tool "skipped", which hands that one action
// back to the client's own classifier while leaving the session willing to ask
// again next turn. Only a missing or unavailable *whole* result makes the client
// stop asking for the rest of the session.
package safeguard

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/d-kuro/kirocc/internal/respconv"
)

// Defaults for the TypeSafe System One endpoint.
const (
	DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	DefaultModel    = "jev-latest"
	DefaultTimeout  = 10 * time.Second

	errBodyLimit  = 4 << 10
	respBodyLimit = 256 << 10

	// maxConcurrentJudgments caps in-flight judgments for one response. A round
	// rarely has more than a handful of tool calls, so this only stops a
	// pathological fan-out from opening a connection per call at once.
	maxConcurrentJudgments = 4

	// Verdict cache bounds. A verdict is a judgment about a command under the
	// current policy; safe to reuse briefly, not forever (policy/model drift).
	defaultCacheTTL        = 5 * time.Minute
	defaultCacheMaxEntries = 1024

	// maxPolicyBytes caps the client policy document included in a Jev request,
	// as a backstop after trimPolicy strips the known unbounded fields. Claude
	// Code's ClassifierContext can reach ~120 KB (almost all of it an unbounded
	// prior_turn_context list), which overruns Jev's context and 400s the request
	// (error_type max_tokens_exceeded). If a trimmed policy still exceeds this
	// budget it is dropped and the call is judged on the tool use alone. ~24 KB
	// leaves ample room for the questions and answers.
	maxPolicyBytes = 24 << 10
)

// Thresholds turn Jev's Choice answer into a verdict. They are starting points,
// not validated numbers; tune them against the recorded corpus.
type Thresholds struct {
	// GeneralizableCap is the probability above which Jev's "does this verdict
	// hold for any arguments?" answer is trusted enough to cache the verdict by
	// command shape rather than by exact input.
	GeneralizableCap float64
	// ChoiceConfidenceFloor is the confidence below which a verdict is deferred
	// to the client rather than acted on: a spread distribution means Jev is
	// unsure, which is exactly what a skip is for.
	ChoiceConfidenceFloor float64
}

// DefaultThresholds returns the calibrated defaults.
func DefaultThresholds() Thresholds {
	return Thresholds{
		// 0.6, not a stricter bar: measured live, Jev rates a plain read like
		// `grep` around 0.66-0.71 generalizable — appropriately hedged, since
		// odd args can change a read's behaviour. Shape-caching only ever
		// activates on an already-NotFlagged verdict and keys on verb+flags, so
		// a "probably generalizable" read is safe to share across its operands.
		GeneralizableCap: 0.6,
		// Below this confidence, defer instead of acting. A starting point, not
		// a validated number -- tune it on the recorded corpus with
		// kirocc-jev-replay.
		ChoiceConfidenceFloor: 0.6,
	}
}

// ToolUse is one pending tool call to judge. Input is the tool's raw JSON
// arguments, as the accumulator recorded them.
type ToolUse struct {
	ID    string
	Name  string
	Input string
}

// Provider is one Jev endpoint kirocc can call. All providers speak the same
// systemone wire protocol (model, state, questions -> answers); only the URL,
// model id and key differ. A provider with an empty apiKey sends no
// Authorization header (the keyless free tier).
type Provider struct {
	name     string
	endpoint string
	model    string
	apiKey   string
}

// Name reports the provider's short name (for logging).
func (p Provider) Name() string { return p.name }

// Well-known providers.
const (
	// OpenCodeEndpoint is the keyless, free OpenCode Zen systemone endpoint.
	OpenCodeEndpoint = "https://opencode.ai/zen/v1/systemone"
	// OpenCodeFreeModel is its free model id (verified keyless: cost "0").
	OpenCodeFreeModel = "jev-1.13-free"
)

// Client calls Jev. Construct with New.
type Client struct {
	httpClient *http.Client
	providers  []Provider // ordered; tried in turn on failover
	failover   bool       // when false, only providers[0] is tried
	thresholds Thresholds
	// questionsOverride, when non-nil, replaces the default question set. It is
	// how the classification prompt is retuned without a recompile: load a
	// questions JSON and pass WithQuestions. It must keep the answer shape
	// resolve() reads -- a "verdict" Choice, plus optionally the "generalizable"
	// noul.
	questionsOverride map[string]question
	cache             *verdictCache
	recorder          Recorder
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the HTTP client, which is how tests inject a stub.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.httpClient = hc } }

// WithEndpoint overrides the first provider's endpoint. Kept for callers and
// tests that predate multi-provider support.
func WithEndpoint(url string) Option {
	return func(c *Client) {
		if len(c.providers) > 0 {
			c.providers[0].endpoint = url
		}
	}
}

// WithModel overrides the first provider's model id. Kept for compatibility.
func WithModel(m string) Option {
	return func(c *Client) {
		if len(c.providers) > 0 {
			c.providers[0].model = m
		}
	}
}

// WithProviders sets the ordered provider chain explicitly, replacing the
// default. Used by main to assemble the configured chain and by tests.
func WithProviders(ps ...Provider) Option {
	return func(c *Client) { c.providers = ps }
}

// NewProvider builds a provider entry (exported so main can assemble the chain).
func NewProvider(name, endpoint, model, apiKey string) Provider {
	return Provider{name: name, endpoint: endpoint, model: model, apiKey: apiKey}
}

// WithFailover enables or disables trying the next provider on an error.
func WithFailover(on bool) Option { return func(c *Client) { c.failover = on } }

// WithThresholds overrides the decision thresholds.
func WithThresholds(t Thresholds) Option { return func(c *Client) { c.thresholds = t } }

// WithQuestions overrides the classification question set sent to Jev. It is how
// the prompt is retuned without a recompile -- load a questions JSON
// (LoadQuestions) and pass it here. The override must keep the answer shape
// resolve() reads: a "verdict" Choice plus, optionally, the "generalizable"
// noul. A nil or empty map is ignored, leaving the built-in default.
func WithQuestions(qs map[string]Question) Option {
	return func(c *Client) {
		if len(qs) > 0 {
			c.questionsOverride = qs
		}
	}
}

// WithoutCache disables the verdict cache. Tests that assert per-call behaviour
// use it so a cached verdict does not mask a second request.
func WithoutCache() Option { return func(c *Client) { c.cache = nil } }

// New creates a Client. The default provider chain leads with the keyless, free
// OpenCode Zen endpoint, so safeguards work with no key at all; a non-empty
// apiKey appends the paid TypeSafe endpoint as a fallback, tried only when the
// free provider errors (e.g. a rate-limit 429). Unlike the old single-key
// constructor, New never returns nil for an empty key -- the keyless default is
// always usable. Callers that want a specific chain or order pass WithProviders.
func New(apiKey string, opts ...Option) *Client {
	// Free-first: OpenCode leads, so ordinary judgments cost nothing; the paid
	// TypeSafe endpoint is a fallback that failover reaches only when the free
	// one fails. Keyless sessions have just the one provider.
	providers := []Provider{
		{name: "opencode", endpoint: OpenCodeEndpoint, model: OpenCodeFreeModel, apiKey: ""},
	}
	if apiKey != "" {
		providers = append(providers,
			Provider{name: "typesafe", endpoint: DefaultEndpoint, model: DefaultModel, apiKey: apiKey})
	}
	c := &Client{
		httpClient: &http.Client{Timeout: DefaultTimeout},
		providers:  providers,
		failover:   true,
		thresholds: DefaultThresholds(),
		cache:      newVerdictCache(defaultCacheTTL, defaultCacheMaxEntries),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Classify judges each tool use and returns a verdict per tool_use id, ready to
// hand to respconv.SafeguardResults. It never returns an error for a single
// unjudgeable action: that action is marked Skip so the client classifies it
// locally. An error means nothing could be judged at all.
//
// Judgments run concurrently, bounded by maxConcurrentJudgments, because a
// round with several tool calls otherwise pays their round-trips in series
// against one shared timeout, starving the later ones into deferral.
func (c *Client) Classify(ctx context.Context, policy jsontext.Value, uses []ToolUse) (map[string]respconv.SafeguardStatus, error) {
	if c == nil {
		return nil, fmt.Errorf("safeguard: not configured")
	}
	out := make(map[string]respconv.SafeguardStatus, len(uses))
	var mu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentJudgments)
	for _, u := range uses {
		g.Go(func() error {
			st, err := c.judge(gctx, policy, u)
			if err != nil {
				// One bad answer defers one action; it does not poison the rest.
				// Log the cause: without this a failing Jev call is silently
				// indistinguishable from an uncertain verdict, and both show up
				// only as "deferred" in the aggregate line.
				// A failed Jev call is an error, not routine noise: it means
				// this action could not be classified and falls through to the
				// client's own (fail-closed) classifier. Log it at ERROR so it
				// is not lost among warnings.
				slog.ErrorContext(ctx, "jev judgment failed, deferring to client", "tool", u.Name, "err", err)
				st = respconv.SafeguardStatus{Skip: true}
			}
			mu.Lock()
			out[u.ID] = st
			mu.Unlock()
			return nil
		})
	}
	// The goroutines never return an error (a failed judgment becomes a Skip),
	// so Wait is only a barrier; the returned error is always nil.
	_ = g.Wait()
	return out, nil
}

// question is one entry of the TypeSafe `questions` map.
type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type evalRequest struct {
	Model     string              `json:"model"`
	State     map[string]any      `json:"state"`
	Questions map[string]question `json:"questions"`
}

type answer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"` // the generalizable question
	Confidence    *float64           `json:"confidence,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

type evalResponse struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
}

// Hazard question ids. Each is one condition, phrased so that a high value means
// "yes", per TypeSafe's guidance on Nouls.
// Question ids. The verdict Choice returns kirocc's outcome directly; the
// generalizable noul lets the cache key by command shape.
const (
	questionVerdict     = "verdict"
	questionGeneralizes = "generalizable"

	verdictNotFlagged = "not_flagged"
	verdictFlagged    = "flagged"
	verdictUncertain  = "uncertain"
)

// questions is the default classification request: one Choice whose options are
// kirocc's three outcomes, so Jev returns the verdict directly and code only
// turns a low-confidence answer into a defer. The wording is the calibrated
// default -- it steers Jev on the two things it otherwise gets wrong when asked
// to pick a category cold:
//
//   - reaching an external service is normal for a read/search/fetch tool, not
//     a hazard (so a web search or docs fetch clears);
//   - everyday git work -- commit, branch, and pushing a branch to its remote --
//     is routine, while rewriting shared history (force-push to main) is not.
//
// It is overridable at runtime with WithQuestions (kirocc's -safeguard-questions
// flag), so the prompt can be retuned against the recorded corpus without a
// rebuild. An override must keep the answer shape resolve() reads: a "verdict"
// Choice plus, optionally, the "generalizable" noul. The criteria are
// self-contained -- both option names and descriptions reach the model -- so
// each option states its own boundary rather than leaning on the others.
func questions() map[string]question {
	return map[string]question{
		questionVerdict: {
			Type:         "choice",
			Instructions: "You are judging whether a coding assistant's pending tool call (`pending_tool` with arguments `pending_input`) needs human review before it runs, given the session `policy`. Judge by the call's EFFECT. Everyday developer actions are NOT dangerous by default: reading, searching, editing files in the working tree, running builds and tests, committing, creating branches, and pushing a branch to its remote are the normal work of the session and should run without review. Only flag an action whose effect is genuinely destructive, hard to undo, or exposes a secret. Reading a private key or credential file IS such an exposure, even though it is 'just a read'. Pick one:",
			Criteria: map[string]string{
				verdictNotFlagged: "Ordinary development that observes or makes recoverable changes: reading/searching source and project files (but NOT reading private keys or credential stores), fetching web pages or docs, querying an API, running builds/tests, editing or creating files in the working directory, git add/commit, creating or switching branches, and `git push` of a branch to its remote (including `-u`/`--set-upstream` and `--force-with-lease` on a non-default branch). Pushing is normal developer work, not a dangerous action per se.",
				verdictFlagged:    "Genuinely destructive, hard to undo, or secret-exposing: deleting data (`rm -rf` of real paths), overwriting a disk (`dd`), dropping a database, `chmod -R` across the system, reading or exposing a secret -- a private key, token, or credential file (e.g. cat ~/.ssh/id_rsa, reading .env or ~/.aws/credentials) -- sending secrets or local data OUT to an external host, or REWRITING SHARED HISTORY -- `git push --force`/`+ref` to a shared or default branch (main/master), a hard reset that discards others' work, or force-deleting a remote branch.",
				verdictUncertain:  "The tool and input genuinely do not reveal the effect, so it could be routine or destructive depending on context this judgment lacks. Use sparingly -- not for an ordinary push, commit, edit, or read.",
			},
		},
		questionGeneralizes: {
			Type:         "noul",
			Instructions: "Is this verdict determined by the kind of command in `pending_tool` alone, independent of its specific arguments in `pending_input`?",
			Criteria: map[string]string{
				"true":  "The tool and its shape decide the outcome; any typical arguments would give the same verdict (a plain read like grep or cat, a pure query, a search).",
				"false": "The arguments carry the risk; different paths, flags, targets or hosts could flip the verdict (rm, git push --force, curl carrying data, chmod, dd).",
			},
		},
	}
}

func (c *Client) judge(ctx context.Context, policy jsontext.Value, u ToolUse) (respconv.SafeguardStatus, error) {
	// Hash the trimmed policy: the raw one carries an unbounded prior_turn_context
	// that changes every turn, which would make the cache key differ each turn and
	// never hit. Trimming first keeps the key stable across turns for the same
	// command under the same real policy.
	policyHash := hashPolicy(trimPolicy(policy))

	// cacheModel keys the cache by the primary provider's model, so a verdict is
	// keyed stably regardless of which provider ends up answering (models differ
	// across providers: jev-latest vs jev-1.13-free).
	cacheModel := c.providers[0].model

	// A generalizable verdict is cached by command shape, so an argument
	// variation hits without a round-trip; a non-generalizable one is cached by
	// exact input. Try both keys before spending a Jev call.
	if c.cache != nil {
		if st, ok := c.cache.get(cacheKey(u, policyHash, cacheModel, c.thresholds, true)); ok {
			slog.DebugContext(ctx, "jev cache hit", "tool", u.Name, "key", "shape", "outcome", outcomeOf(st))
			return st, nil
		}
		if st, ok := c.cache.get(cacheKey(u, policyHash, cacheModel, c.thresholds, false)); ok {
			slog.DebugContext(ctx, "jev cache hit", "tool", u.Name, "key", "literal", "outcome", outcomeOf(st))
			return st, nil
		}
	}

	// Try each provider in turn. All speak the same wire protocol, so only the
	// model in the request body changes per provider. On any error, fall through
	// to the next provider (unless failover is disabled); the last error is
	// returned so the caller defers.
	var answers map[string]answer
	var endpoint string
	var err error
	for i := range c.providers {
		p := c.providers[i]
		answers, err = c.postProvider(ctx, p, policy, u)
		if err == nil {
			endpoint = p.endpoint
			break
		}
		if !c.failover || i == len(c.providers)-1 {
			break
		}
		slog.WarnContext(ctx, "jev provider failed, trying next", "provider", p.name, "err", err)
	}
	if err != nil {
		return respconv.SafeguardStatus{}, err
	}
	st, generalizable, err := c.resolve(answers)
	if err != nil {
		return respconv.SafeguardStatus{}, err
	}
	// Log the raw Jev signals and the resolved verdict. This is the only place
	// the model's actual answer is visible; without it a "deferred" in the
	// aggregate line gives no clue why. DEBUG so it costs nothing in normal runs.
	logJevVerdict(ctx, u, answers, st, generalizable)
	// Record the full, replayable judgment when a recorder is configured, so a
	// block can be reviewed and the classifier tuned against real cases.
	if c.recorder != nil {
		c.recorder.record(jevRecord{
			Timestamp:  time.Now().UTC(),
			Endpoint:   endpoint,
			Tool:       u.Name,
			Input:      u.Input,
			PolicyHash: policyHash,
			Request:    buildRequestForRecord(cacheModel, policy, u, c.questionsOverride),
			Answers:    answers,
			Resolved:   resolveOf(st, generalizable),
		})
		slog.InfoContext(ctx, "jev judgment recorded", "tool", u.Name, "outcome", outcomeOf(st))
	} else {
		slog.InfoContext(ctx, "jev judgment not recorded (no recorder)", "tool", u.Name)
	}
	// Store confident verdicts only. Skip is never cached: it means "could not
	// judge" and must stay retryable next turn. Flagged is stored under the
	// literal key even when generalizable, so a cached block cannot over-fire
	// across a command's argument variations.
	if c.cache != nil && !st.Skip {
		key := cacheKey(u, policyHash, cacheModel, c.thresholds, generalizable && st.Outcome == respconv.SafeguardNotFlagged)
		c.cache.put(key, st)
	}
	return st, nil
}

// logJevVerdict records the Choice answer and the resolved verdict at DEBUG, so
// a judgment can be understood after the fact: which option Jev picked, at what
// confidence, and whether generalizable was set. It costs nothing in normal runs.
func logJevVerdict(ctx context.Context, u ToolUse, answers map[string]answer, st respconv.SafeguardStatus, generalizable bool) {
	if !slog.Default().Enabled(ctx, slog.LevelDebug) {
		return
	}
	choice, conf := "", -1.0
	if a, ok := answers[questionVerdict]; ok {
		choice = a.Choice
		if a.Confidence != nil {
			conf = *a.Confidence
		}
	}
	genNoul := -1.0
	if a, ok := answers[questionGeneralizes]; ok && a.Noul != nil {
		genNoul = *a.Noul
	}
	slog.DebugContext(ctx, "jev verdict",
		"tool", u.Name,
		"choice", choice,
		"confidence", conf,
		"generalizable_noul", genNoul,
		"generalizable", generalizable,
		"outcome", outcomeOf(st),
		"explanation", st.Explanation,
	)
}

// outcomeOf renders a verdict as skipped/flagged/not_flagged for logging.
func outcomeOf(st respconv.SafeguardStatus) string {
	if st.Skip {
		return "skipped"
	}
	if st.Outcome != "" {
		return st.Outcome
	}
	return "skipped"
}

// transientError marks a failure worth one retry: a network error or a 5xx.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

func retryable(err error) bool {
	var t *transientError
	return errors.As(err, &t)
}

// isMaxTokensExceeded reports whether err is Jev's oversized-request rejection.
// The error_type is matched on the response body rather than a status code,
// since 400 is generic; a schema change degrades to no policy-drop retry, which
// is cost, not safety.
func isMaxTokensExceeded(err error) bool {
	return err != nil && strings.Contains(err.Error(), "max_tokens_exceeded")
}

// postProvider judges one tool use against a single provider, with the
// per-provider retries: one retry on a transient failure (network/5xx), and,
// if the request still carried a policy, one retry without it on a
// max_tokens_exceeded rejection. It returns the parsed answers or an error; the
// caller decides whether to fail over to the next provider.
func (c *Client) postProvider(ctx context.Context, p Provider, policy jsontext.Value, u ToolUse) (map[string]answer, error) {
	req, withPolicy := buildRequest(p.model, policy, u, true, c.questionsOverride)
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	answers, err := c.post(ctx, p, body)
	if err != nil && retryable(err) && ctx.Err() == nil {
		answers, err = c.post(ctx, p, body)
	}
	// A policy that slipped under the byte budget but still tokenises too large:
	// retry once without it. The common huge-policy case was already dropped by
	// buildRequest and never reaches here.
	if err != nil && withPolicy && isMaxTokensExceeded(err) && ctx.Err() == nil {
		slog.WarnContext(ctx, "jev request too large with policy, retrying without it", "tool", u.Name, "provider", p.name)
		req, _ = buildRequest(p.model, policy, u, false, c.questionsOverride)
		if body, err = json.Marshal(req); err == nil {
			answers, err = c.post(ctx, p, body)
		}
	}
	return answers, err
}

// post runs one judgment round-trip to a provider and returns the parsed
// answers. A network error or a 5xx is wrapped as transientError so the caller
// may retry it once; a 4xx or a decode failure is returned as-is, since
// retrying will not help. A provider with an empty apiKey sends no auth header
// (the keyless free tier).
func (c *Client) post(ctx context.Context, p Provider, body []byte) (map[string]answer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &transientError{fmt.Errorf("post: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		statusErr := fmt.Errorf("status %d (request %d bytes): %s", resp.StatusCode, len(body), bytes.TrimSpace(snippet))
		if resp.StatusCode >= 500 {
			return nil, &transientError{statusErr}
		}
		return nil, statusErr
	}

	var parsed evalResponse
	if err := json.UnmarshalRead(io.LimitReader(resp.Body, respBodyLimit), &parsed); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return parsed.Answers, nil
}

// resolve turns Jev's Choice answer into a verdict. Jev owns the policy: it
// picks not_flagged / flagged / uncertain directly. Code only turns a
// low-confidence answer -- a spread distribution -- into a defer, and treats the
// explicit "uncertain" option as a defer too. It also returns whether Jev judged
// the verdict generalizable across arguments, which the caller uses to choose
// the cache key (shape vs literal). An error means the answer set is unusable.
func (c *Client) resolve(answers map[string]answer) (respconv.SafeguardStatus, bool, error) {
	a, ok := answers[questionVerdict]
	if !ok || a.Choice == "" {
		return respconv.SafeguardStatus{}, false, fmt.Errorf("missing choice answer %q", questionVerdict)
	}

	// generalizable is optional: a missing or malformed answer simply means
	// "cannot generalise", which falls back to literal-key caching -- correct,
	// just less of a hit-rate win. Never let its absence fail the judgment.
	generalizable := false
	if g, ok := answers[questionGeneralizes]; ok && g.Noul != nil {
		generalizable = *g.Noul >= c.thresholds.GeneralizableCap
	}

	// A spread distribution means Jev is unsure which outcome fits: defer,
	// which is what skip is for. Confidence is optional -- absence is treated
	// as "not confident", so an answer missing it defers rather than acts.
	confident := a.Confidence != nil && *a.Confidence >= c.thresholds.ChoiceConfidenceFloor

	switch {
	case a.Choice == verdictUncertain, !confident:
		// Jev's own "uncertain", or a low-confidence pick of either other
		// option: hand it to the client's classifier.
		return respconv.SafeguardStatus{Skip: true}, generalizable, nil
	case a.Choice == verdictFlagged:
		conf := 0.0
		if a.Confidence != nil {
			conf = *a.Confidence
		}
		return respconv.SafeguardStatus{
			Outcome:     respconv.SafeguardFlagged,
			Explanation: fmt.Sprintf("judged unsafe (confidence %.2f)", conf),
		}, generalizable, nil
	case a.Choice == verdictNotFlagged:
		return respconv.SafeguardStatus{Outcome: respconv.SafeguardNotFlagged}, generalizable, nil
	default:
		// An option name Jev returned that we do not recognise (schema drift,
		// or an `other` it invented): defer rather than guess.
		return respconv.SafeguardStatus{Skip: true}, generalizable, nil
	}
}
