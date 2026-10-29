# Mesh ↔ Runner chat protocol (v1, as implemented)

This page documents the contract the API serves **today** under `/api/v1/runner/chat/*`,
read from the handlers in `internal/handler/chat_*` and the repositories in
`internal/repository/postgres/chat_*`. It does not describe plans. The design source is the
Mesh Doc `agent-chat-runner-protocol`; where it differs from the code, the code wins and the
difference is listed in [Known limits](#known-limits-not-implemented).

Related pages: [Extensions API](api.md), [API version](api-version.md). The runner side
(sessions, worktrees, task waits) lives in the
[evc-mesh-runner](https://github.com/entire-vc/evc-mesh-runner) README; this page covers only
the chat-turn channel between Mesh and a runner.

## 1. Identity and headers

| Item | Rule |
|---|---|
| Credential | The API key of the agent the runner serves (`X-Agent-Key: agk_…`; `Authorization: Bearer` is accepted only for an OAuth `mot_` token, which the runner routes refuse as a connector). Claim, lease and events refuse a chat-session token (`cst_…`), a user token and an OAuth connector (`401`/`403`). Registration, commands/ack and history only require an agent id in the credential: they do not run the connector check, so do not rely on them to reject one. |
| `X-Chat-Protocol: 1` | Required on claim, active, start, session-token, lease and events (the routes that share the runner identity check); runners registration, commands/ack and history do not enforce it today, send it anyway. Missing/other → `400 chat_protocol_unsupported`, `context.supported=[1]`. Responses carry `X-Chat-Protocol` and `X-Chat-Protocol-Supported`. |
| `X-Chat-Runner-Id: <uuid>` | Required on claim, active, start, session-token, lease and events. With the agent key it is the holder identity every later call is fenced against. |
| `X-Runner-Token` | Per-runner bearer from `POST /runner/chat/runners`; optional on commands/ack. With it, the call is scoped to that runner's orders and a `runner_id` query parameter must match it; naming `runner_id` without the token → `403 chat_runner_forbidden`. Without it the caller acts as the agent as a whole and sees agent-wide orders. |
| Agent scope | `agents=` on claim may only contain the key's own agent; any other id → `403 chat_agent_not_served`. A runner serving several agents polls once per agent. |

## 2. Turn lifecycle

States: `queued → claimed → streaming → done | failed | cancelled | expired`. `POST /turns/:id/start` moves `claimed → streaming`; the runner may touch the model only after its `200`. The agent *message* is marked `streaming` separately.

- **Claim** moves `queued → claimed`, mints a `claim_token`, bumps `attempt`, sets `lease_until`. The user message stays `queued` until the accepted **start**, which sets it to `done` (delivered); a turn that never starts leaves it `queued`/`failed`/`cancelled` with the turn.
- A `session` event does not change the turn state; it only records the session for recovery.
- A terminal event (`final` or any `error`) ends it. A turn already
  terminal answers `409 chat_turn_terminal`.
- Lease: 60 s (`ChatLeaseSeconds`), at most 3 attempts (`ChatMaxAttempts`). A lease that lapses
  returns the turn to the queue; the old holder then gets `409 chat_stale_claim` /
  `chat_lease_expired`. Only the runner holding the current `claim_token` may write.

## 3. Endpoint table

| Endpoint | Auth | Success | Errors (code → HTTP) |
|---|---|---|---|
| `GET /runner/chat/claim?agents=<uuid>[&timeout=1..120][&max=1..5]` | agent key + runner id | `200 {protocol, turns[], commands:[], server_time}`; `204` on timeout or server shutdown. Defaults: timeout 30, max 1; out-of-range values are clamped, not rejected (timeout to 1..120, max to at most 5). `Cache-Control: no-store`. | `chat_protocol_unsupported` 400; `agents is required` / non-UUID / `>50 agents` / non-integer `timeout`, non-integer or non-positive `max` 400; `chat_agent_not_served` 403; `X-Chat-Runner-Id must be a UUID` 400; 401 non-agent credential; 403 OAuth connector. |
| `GET /runner/chat/turns/active` | agent key + `X-Chat-Protocol: 1` + runner id | `200 {protocol, turns[]}`: the `claimed`/`streaming` turns this runner id holds for the key's agent, each `{turn_id, thread_id, agent_id, state, attempt, claim_token, lease_until, execution_epoch}`; `turns` is `[]` when none. Used after a restart to resume (`lease`/`start`/`events` with the returned `claim_token`) or abandon. | `chat_protocol_unsupported` 400; `X-Chat-Runner-Id must be a UUID` 400; 401 non-agent credential; 403 OAuth connector. |
| `POST /runner/chat/turns/:id/lease` `{claim_token}` | agent key + runner id | `200 {lease_until, state, cancel_requested, execution_epoch}` | `chat_turn_not_found` 404 (also for a foreign or malformed id: no existence oracle); `chat_not_claim_holder` 403; `chat_stale_claim` 409; `chat_lease_expired` 409; `chat_turn_terminal` 409; missing `claim_token` 400. |
| `POST /runner/chat/turns/:id/events` `{claim_token, events[]}` | agent key + runner id | `200 {applied_seq, state, cancel_requested, lease_until, execution_epoch, ignored[]}` | `chat_batch_too_large` 413 (>256 KiB); `chat_rate_limited` 429 + `Retry-After`; `chat_seq_gap` 409; `chat_not_started` 409; `chat_not_claim_holder` 403; `chat_stale_claim` 409; `chat_turn_terminal` 409; `chat_turn_not_found` 404; empty `events` / bad JSON / unknown `stop_reason` 400. |
| `POST /runner/chat/runners` `{name}` | agent key | `201 {runner_id, agent_id, name, created_at, token}`: the token is shown only here | `chat_runner_forbidden` 403 for a non-agent caller. |
| `DELETE /runner/chat/runners/:runner_id` | agent key | `204` | `chat_runner_forbidden` 403; 400 bad id. |
| `GET /runner/chat/commands?agents=<uuid,…>[&runner_id=<uuid>]` | agent key; `X-Runner-Token` optional (see §1) | `200 {commands:[{command_id, kind, agent_id, runner_id, thread_id, session_id, harness, created_at}]}` oldest first | `chat_runner_forbidden` 403; 400 bad `agents`/`runner_id`. |
| `POST /runner/chat/commands/:command_id/ack` `{result: done\|not_found\|failed, detail}` | agent key; `X-Runner-Token` optional | `200 {command_id, state}` | `chat_runner_forbidden` 403 (also a foreign command); 400 bad id/body. |
| `GET /runner/chat/threads/:thread_id/history?limit=<N>[&before=<message_id>]` | agent key | `200 {thread_id, messages[], has_more}` (default limit 20; values above 100 are clamped to 100, not rejected) | 400 for a malformed `thread_id`, `limit` (non-integer or <1) or `before`; `chat_history_forbidden` 403 unless the thread holds a claimed/streaming turn of this agent with a live lease. |

## 4. Events (`POST …/events`)

A batch holds up to 200 events and 256 KiB. Every event carries a gapless `seq` per turn;
a retry of an already applied `seq` is acknowledged, not re-applied (`applied_seq` says where
the server is). A hole → `409 chat_seq_gap` with `context.expected_seq`.

| `type` | Fields | Effect |
|---|---|---|
| `session` | `session_id`, `harness`, `profile` | Stored as the thread's runtime (`session_id`, `harness`, `profile`) for recovery; the turn state is unchanged. |
| `text_delta` | `text` | Appended to the agent message (cap 256 KiB; beyond it the message is marked `truncated`). |
| `tool_status` | `tool_call_id`, `label` (≤120 runes), `status: started\|finished\|failed` | Tool line pushed to the owner. Other status → ignored `invalid_tool_status`. |
| `usage` | `tokens_in`, `tokens_out`, `cost_usd`, `model` | Recorded on the turn. |
| `final` | `text` (full reply), `stop_reason: end_turn\|cancelled\|max_tokens`, optional `usage` | Turn → `done` (or `cancelled`). Unknown `stop_reason` → 400. |
| `error` | `code`, `message`, `retryable` | Always terminal: turn → `failed`, the message gets `error_code` (empty `code` → `runner_error`). `retryable` is accepted but the server does not act on it; re-queueing happens only by lease expiry. |

`ignored[].reason` values: `after_terminal`, `invalid_tool_status`, `unknown_type`.

## 5. Stop and cancel

- A user's **Stop** (`POST /projects/:proj_id/chat/threads/:thread_id/stop`) on a queued turn
  cancels it at once; on a claimed/streaming turn it sets `cancel_requested`.
- The runner learns it from `cancel_requested=true` in the lease and events answers. It must
  stop the harness and send `final` with `stop_reason: cancelled`. There is no push channel:
  the signal arrives with the next lease renewal or batch (≤ 60 s).
- Deleting a thread cancels its unfinished turns; the runner's next call gets
  `409 chat_turn_terminal`.

## 6. Expiry and retry

- **Offline expiry.** A queued turn nobody claims within the workspace's offline TTL
  (default 24 h, `ChatDefaultOfflineTTL`) becomes `expired` and is not delivered later. A sweeper
  runs about once a minute.
- **Lease expiry.** Handled as in §2; at most 3 attempts, then the turn fails.
- **Retry rules for the runner.** `chat_stale_claim`, `chat_lease_expired`, `chat_not_claim_holder`,
  `chat_turn_terminal`, `chat_turn_not_found` → drop the turn locally, do not retry. `chat_seq_gap`
  → resend from `expected_seq`. `chat_rate_limited` → wait `Retry-After`. 5xx/network → retry
  the same batch with the same `seq`s.

## 7. Orders (commands) and recovery

- Kinds written today: `stop_session` and `delete_transcript`, both queued when a thread is deleted
  by a user or by history retention (`agent_chat.history_retention_days`). Orders are durable and
  delivered **at least once**: a command is returned until acked or until its `expires_at` passes (30 days after a thread delete); an expired command is no longer delivered. `done` and `not_found` close it;
  `failed` keeps it, and after 10 failed acks (`ChatCommandMaxAttempts`) it is dead.
- The claim answer's `commands` field is always `[]`. Fetch orders through `/commands`.
- Recovery: a runner that lost a native session reads `/threads/:id/history` (private chat: only
  while it holds a live turn on that thread) and rebuilds context from it. The current turn's own
  user message is omitted because the claim carries it.
- Transcript purge: a deleted thread's data is removed after 30 days (`ChatPurgeAfter`).

## 8. Session token and scopes

The runner does not hand its agent key to the harness. A host controller mints a per-thread
**chat-session token** through the internal API (`POST /internal/chat-sessions/tokens`,
`…/:session_id/renew`, `…/:session_id/revoke`; direct network only, controller credential).

- Prefix `cst_`; TTL 5 minutes, renewable; a superseded token overlaps 10 s.
- Effective rights are **agent ∩ user**, with the workspace role `member`.
- Confined to the chat's single project: any route naming a resource of another project or
  workspace, or whose project cannot be determined, answers `403 chat_scope_denied`
  (fail-closed). `POST /tasks/:task_id/move-to-project` is always denied.
- Invalid/expired/revoked → `401`. The token is bound to `execution_epoch`; deleting the thread
  bumps the epoch, so a session permission minted before the delete stops working.
- A `cst_` token is refused on every `/runner/chat/*` route.

## 9. Data, network and storage disclosure

| Question | Answer |
|---|---|
| What leaves Mesh toward the runner? | The user's message text, attachment metadata plus short-lived presigned download URLs, the requester's id and display name, and (on `history`) earlier messages of that thread. |
| What the runner sends back | Reply text, tool labels, usage and cost, session id/harness/profile. |
| Where it is stored | Mesh PostgreSQL (`ext_agent_chat_*` tables); attachments in the instance's object storage. The runner keeps its native session transcript on the operator's host. |
| Retention | Per workspace `agent_chat.history_retention_days` (unset or 0 = keep). User delete is soft, purge after 30 days; the runner is told to delete the transcript through `delete_transcript`. |
| Network | The runner connects outbound only, to this Mesh instance (long-poll claim up to 120 s). Mesh never calls into the runner for chat. The harness may reach whatever its own profile allows; that is the operator's configuration, not Mesh's. |
| Secrets | The agent key and the `X-Runner-Token` stay with the runner process; the harness gets only the short-lived `cst_` token. |

## 10. Known limits (not implemented)

These are open work; do not rely on or promise them.

- **E3 an internal ticket, E4 an internal ticket, E5 an internal ticket** carry the remaining runner-side and delivery work. Until
  they are accepted, treat everything below as unavailable.
- **No live streaming to the browser guaranteed by this contract.** The runner posts `text_delta`
  batches and Mesh stores them; real-time token streaming and its latency are not promised.
- **No capacity reporting.** Mesh does not know how many turns a runner can take; a claim returns
  what is queued up to `max`.
- **No write-workspace for chat.** A chat session has the agent∩user rights over the Mesh API
  only; checked-out workspaces for writing code are not part of the chat contract.
- **Cancel is polled**, not pushed (§5). The `cancel_turn` kind named in the design is not produced.
- `commands` in the claim answer is always empty (§7).
- Publishing to a catalog and acceptance of the whole chat are not claimed by this page.

## 11. Verification

Each row of §3 maps to a test: `internal/handler/chat_claim_handler_test.go`,
`chat_runner_events_handler_test.go`, `chat_runner_handler_test.go` and their `_db_test.go`
counterparts. Offline expiry (§6) and recovery of a queued turn by a returning runner are
exercised end to end against the published app by `web/e2e-chat/s07.spec.ts` (job
`agent-chat-e2e`). Last checked against `origin/main` at the commit this page was merged on.
