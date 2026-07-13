---
name: kiro-capture
description: "Capture and analyze kiro-cli HTTPS traffic using mitmdump. Sets up mitmdump proxy, provides the kiro-cli launch command, then parses the capture log to generate individual API call files and a detailed analysis report. Use via /kiro-capture."
disable-model-invocation: true
---

# kiro-capture

Capture kiro-cli HTTPS traffic with mitmdump and generate analysis reports.

## Phase Detection

On invocation, find the latest session under `/tmp/kiro-capture/`:

```bash
LATEST=$(ls -dt /tmp/kiro-capture/*/ 2>/dev/null | head -1)
if [ -n "$LATEST" ] && [ -f "${LATEST}state.json" ]; then
  cat "${LATEST}state.json"
fi
```

Decision logic:

- `state.json` exists with `"status": "capturing"`:
  - Check PID with `kill -0 <pid>`
  - PID alive → ask user: "Capture in progress. Stop and analyze?" If yes → Phase 2
  - PID dead → tell user "mitmdump already stopped. Analyzing log." → Phase 2
- `state.json` exists with `"status": "completed"` or `"status": "analyzing"` → ask user: "Previous capture is done. Start a new capture?"
- No `state.json` or empty directory → Phase 1

## Phase 1: Setup

### Step 1 — Check prerequisites

```bash
which mitmdump
```

If not found:

```bash
brew install mitmproxy
```

Check CA certificate:

```bash
ls ~/.mitmproxy/mitmproxy-ca-cert.pem
```

If missing, run mitmdump once to generate it:

```bash
mitmdump --set flow_detail=0 &
MITM_PID=$!
sleep 2
kill $MITM_PID 2>/dev/null
wait $MITM_PID 2>/dev/null
```

### Step 2 — Check port availability

```bash
lsof -i :8080 -t 2>/dev/null
```

If port 8080 is in use, try 8081, 8082, ... until a free port is found.

### Step 3 — Create session directory

```bash
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
SESSION_DIR="/tmp/kiro-capture/${TIMESTAMP}"
mkdir -p "$SESSION_DIR"
```

### Step 4 — Start mitmdump

```bash
mitmdump -p $PORT --set flow_detail=3 >"${SESSION_DIR}/raw.log" 2>&1 &
MITM_PID=$!
sleep 1
kill -0 $MITM_PID 2>/dev/null && echo "OK" || echo "FAILED"
```

### Step 5 — Save state.json

Write to `${SESSION_DIR}/state.json`:

```json
{
  "status": "capturing",
  "pid": <MITM_PID>,
  "port": <PORT>,
  "session_dir": "<SESSION_DIR>",
  "started_at": "<ISO8601>"
}
```

### Step 6 — Output kiro-cli launch command

Tell the user:

"mitmdump is running on port {PORT}. Run the following command in a **separate terminal** to start kiro-cli through the proxy:

```bash
HTTPS_PROXY=http://127.0.0.1:{PORT} \
HTTP_PROXY=http://127.0.0.1:{PORT} \
SSL_CERT_FILE=~/.mitmproxy/mitmproxy-ca-cert.pem \
kiro-cli chat
```

When you're done using kiro-cli, come back here and run `/kiro-capture` again to stop the capture and generate the analysis report.

Session directory: `{SESSION_DIR}`"

Phase 1 ends here. Wait for the user to re-invoke.

## Phase 2: Analysis

### Step 7 — Stop mitmdump

Read PID from `state.json` and stop that specific process:

```bash
kill <PID_FROM_STATE_JSON>
while kill -0 <PID_FROM_STATE_JSON> 2>/dev/null; do sleep 0.2; done
```

Skip if PID is already dead.

### Step 8 — Update state.json

Set `status` to `"analyzing"`.

### Step 9 — Parse capture log

Read `${SESSION_DIR}/raw.log` and extract request/response pairs.

mitmdump `flow_detail=3` output format:

```
<IP>: <METHOD> <URL>
    <header>: <value>
    ...

<request body>

<< <STATUS> <SIZE>
    <header>: <value>
    ...

<response body>
```

For each request/response pair, extract:

1. **Sequence number** — assign 01, 02, ... in order of appearance
2. **API name** — from `x-amz-target` header. Known APIs:
   - `AmazonCodeWhispererStreamingService.GenerateAssistantResponse` (EventStream response)
   - `AmazonCodeWhispererService.ListAvailableModels` (JSON)
   - `ToolkitTelemetry.ClientTelemetryMetrics` (JSON)
   - `AmazonCodeWhispererService.GetProfile` (JSON)
   - `AmazonCodeWhispererService.GetUsageLimits` (JSON, **403 is expected/normal**; may be absent in a session)
   - `AmazonCodeWhispererService.SendTelemetryEvent` (JSON)
   - `AWSCognitoIdentityService.GetCredentialsForIdentity` (JSON; cognito-identity host — returns temporary AWS creds used to SigV4-sign the telemetry calls)
   - If no `x-amz-target`, infer from URL path/host:
     - `oidc.*.amazonaws.com/token` → `SSOOIDC.CreateToken` (JSON; `grantType: refresh_token` rotates the Bearer token mid-session)
     - `client-telemetry.*.amazonaws.com/metrics` → `ToolkitTelemetry.ClientTelemetryMetrics`
3. **Request URL**
4. **Request headers** — all headers, but redact:
   - `authorization` header value → `[REDACTED]` (covers both `Bearer <token>` and `AWS4-HMAC-SHA256 ... Signature=...`)
   - `x-amz-security-token` header value → `[REDACTED]`
5. **Request body** — extract JSON as-is, but redact secret **values** in these bodies:
   - `CreateToken` request: `refreshToken`, `clientSecret` → `[REDACTED]`
6. **Response status** — from `<< STATUS SIZE` line
7. **Response headers**
8. **Response body**:
   - JSON → extract as-is, but redact secret **values**:
     - `GetCredentialsForIdentity` response: `AccessKeyId`, `SecretKey`, `SessionToken` → `[REDACTED]`
     - `CreateToken` response: `accessToken`, `refreshToken` → `[REDACTED]`
   - EventStream (`application/vnd.amazon.eventstream`) → apply EventStream parsing below

**Redaction rule:** secret values may only ever appear in `raw.log` (the mitmdump input). Every generated `.md` must contain `[REDACTED]` in their place. After generating files, verify no secret fragment (AWS `ASIA...` access key IDs, `Bearer aoa...`/`aor...` tokens, `AWS4-HMAC ... Signature=<hex>`, Cognito `IQoJ...` session tokens) leaks into any `.md` — including `report.md` prose.

### EventStream response parsing

`GenerateAssistantResponse` responses use EventStream binary format. In mitmdump logs, binary and text are interleaved.

Parsing steps:

1. Identify event boundaries by `:event-type` markers
2. Extract JSON objects (`{...}`) from each event segment
3. Classify events:
   - `initial-response` — stream preamble (`{"conversationId":""}`)
   - `reasoningContentEvent` — native reasoning/thinking stream: `text` deltas followed by a final `signature` field. Concatenate the `text` deltas; note the `signature` exists but show it as `[present]` (do not dump the full value)
   - `assistantResponseEvent` — assistant response text (delta `content` field)
   - `toolUseEvent` — tool call (`name`, `toolUseId`, `input` fields)
   - `metadataEvent` — turn end (`stopReason`, e.g. `END_TURN`)
   - `contextUsageEvent` — context usage (`contextUsagePercentage` field)
   - `meteringEvent` — credit consumption (`{"unit":"credit","usage":...}`)
4. Merge events sharing the same `toolUseId` to reconstruct complete tool calls
5. Concatenate `assistantResponseEvent` `content` fields to reconstruct full response text; concatenate `reasoningContentEvent` `text` fields to reconstruct the reasoning trace

> Note: `reasoningContentEvent` and `meteringEvent` are emitted by kiro-cli 2.10.0. Reasoning is now **native** (a `reasoningContentEvent` stream), not a `thinking` tool in the tools array.

### Step 10 — Generate individual API call files

For each request/response pair, create `${SESSION_DIR}/NN_APIName.md`.

Format (follows existing `_docs/capture/01_ClientTelemetryMetrics.md`):

```markdown
# NN. ServiceName.APIName

## Request

\`\`\`
METHOD URL
\`\`\`

### Request Headers

\`\`\`
header: value
authorization: [REDACTED]
x-amz-security-token: [REDACTED]
...
\`\`\`

### Request Body

\`\`\`json
{JSON body}
\`\`\`

## Response

\`\`\`
STATUS SIZE
\`\`\`

### Response Headers

\`\`\`
header: value
...
\`\`\`

### Response Body

\`\`\`json
{JSON body}
\`\`\`
```

For EventStream responses, use this Response Body format instead:

```markdown
### Response Body

Content-Type: `application/vnd.amazon.eventstream`

**Events:**

| #   | Event Type             | Summary                          |
| --- | ---------------------- | -------------------------------- |
| 1   | initial-response       | conversationId ""                |
| 2   | reasoningContentEvent  | {text delta / signature present} |
| 3   | assistantResponseEvent | {first 80 chars}                 |
| 4   | toolUseEvent           | {tool name}: {first 80 chars}    |
| 5   | metadataEvent          | stopReason {value}               |
| 6   | contextUsageEvent      | {percentage}%                    |
| 7   | meteringEvent          | {usage} credits                  |

**Full reasoning text:** (if any reasoningContentEvent)

\`\`\`
{concatenated reasoning text}
\`\`\`

**Full assistant response:**

\`\`\`
{concatenated full text}
\`\`\`

**Tool calls:** (if any toolUseEvent)

\`\`\`json
[{reconstructed tool call objects}]
\`\`\`

**Metering:** {unit}, usage {value}
```

### Step 11 — Generate analysis report

Create `${SESSION_DIR}/report.md`.

Template:

```markdown
# Capture Analysis Report — {TIMESTAMP}

## Capture Environment

| Item             | Value                                                 |
| ---------------- | ----------------------------------------------------- |
| Date             | {started_at from state.json}                          |
| kiro-cli version | {from user-agent: md/appVersion-X.Y.Z}                |
| AWS SDK          | {from user-agent: aws-sdk-rust/X.Y.Z}                 |
| OS               | {from user-agent}                                     |
| Model            | {modelId from GenerateAssistantResponse request body} |
| Region           | {from request URL hostname}                           |
| Capture tool     | mitmdump (mitmproxy)                                  |
| Raw log          | [raw.log](raw.log)                                    |

## API Call Summary

| #   | API                     | Status   | Size   | File                           |
| --- | ----------------------- | -------- | ------ | ------------------------------ |
| 01  | {ServiceName}.{APIName} | {status} | {size} | [01_APIName.md](01_APIName.md) |
| ... |

## API Category Breakdown

| Category                      | Count | APIs                                                                |
| ----------------------------- | ----- | ------------------------------------------------------------------- |
| CodeWhispererStreamingService | {n}   | GenerateAssistantResponse                                           |
| CodeWhispererService          | {n}   | GetProfile, ListAvailableModels, GetUsageLimits, SendTelemetryEvent |
| ToolkitTelemetry              | {n}   | ClientTelemetryMetrics                                              |
| AWSCognitoIdentityService     | {n}   | GetCredentialsForIdentity                                           |
| SSOOIDC                       | {n}   | CreateToken                                                         |

(Omit categories with count 0.)

## Authentication

1. **Bearer Token** (`authorization: Bearer <token>`): CodeWhisperer / kiro.dev API
2. **AWS Signature V4** (`authorization: AWS4-HMAC-SHA256 ...`): Telemetry API
3. **Cognito + OIDC**: `GetCredentialsForIdentity` yields the temporary AWS creds that SigV4-sign the telemetry calls; `CreateToken` (`grantType: refresh_token`) refreshes the Bearer token mid-session (note if/when the token rotates)

## Conversation Flow

Group requests by conversationId and agentContinuationId. Render as ASCII tree:

\`\`\`

1. kiro-cli startup
   ├── ClientTelemetryMetrics (...)
   ├── GetProfile
   ├── ...

2. User message sent
   ├── GenerateAssistantResponse #1 (...)
   ├── SendTelemetryEvent
   └── ClientTelemetryMetrics (...)

3. Tool result sent
   ├── GenerateAssistantResponse #2 (...)
   ...
   \`\`\`

## Context Usage

| #   | GenerateAssistantResponse | History length    | contextUsagePercentage |
| --- | ------------------------- | ----------------- | ---------------------- |
| 1   | #NN                       | {history entries} | {percentage}%          |
| ... |

## Model Info

- Model ID: {modelId}
- Reasoning/thinking: {active/inactive — active if the response has a `reasoningContentEvent` stream. kiro-cli 2.10.0 uses native reasoning, so there is no `thinking` tool in the tools array}
- Tool count: {length of tools array}

## Tool Usage Patterns

| Tool name                     | Invocation count |
| ----------------------------- | ---------------- |
| {tool name from toolUseEvent} | {count}          |

(If no toolUseEvent occurred, report `(none) | 0` and note the turns were plain text.)

## Notable Findings

Bullet list of anything noteworthy vs. prior captures, e.g.:

- New event types or APIs observed (e.g. `reasoningContentEvent`, `meteringEvent`)
- Bearer token refresh / rotation via OIDC `CreateToken`
- Model-catalog specifics from `ListAvailableModels` (rateMultiplier, maxOutputTokens, context window, thinking.type enum)
- APIs that were expected but absent (e.g. `GetUsageLimits`)
- Metering / credit usage per turn
```

### Step 12 — Update state.json

Set `status` to `"completed"`. Add `completed_at` timestamp.

### Step 13 — Report to user

Tell the user:

"Capture analysis complete.

- Session directory: `{SESSION_DIR}`
- Raw log: `{SESSION_DIR}/raw.log`
- Analysis report: `{SESSION_DIR}/report.md`
- Individual API calls: `{SESSION_DIR}/NN_*.md` ({count} files)

Total API calls: {total_count}

- GenerateAssistantResponse: {count}
- Others: {count}

Context usage (final): {last_percentage}%"
