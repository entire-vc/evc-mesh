# SSE Event Durability

How the API delivers agent notifications with at-least-once durability across
reconnects, and how the backing store is kept bounded.

## Goal

An agent that disconnects (crash, redeploy, network blip) and reconnects with a
`Last-Event-ID` must receive every event it missed, in order, exactly once — or
be told unambiguously to perform a full state recovery. The store that makes this
possible must not grow without bound.

## Components

| Component | File | Role |
|-----------|------|------|
| `agent_events` table | `migrations/20260521049_create_agent_events.sql` | Durable per-agent event log |
| `AgentEventsRepo` | `internal/repository/postgres/agent_events_repo.go` | `Create` / `Lookup` / `ListAfter` / `DeleteExpired` |
| `AgentNotifyService` | `internal/service/agent_notify_service.go` | Assigns event ID, persists, publishes |
| SSE handler | `internal/handler/agent_handler.go` (`EventStream`) | Replay + live stream over `text/event-stream` |
| Sweeper | `cmd/api/main.go` | Deletes expired rows every 5 min |

## Schema

```sql
CREATE TABLE agent_events (
    event_id     UUID        PRIMARY KEY,   -- UUID v7, time-ordered
    agent_id     UUID        NOT NULL,
    workspace_id UUID        NOT NULL,
    event_type   TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    emitted_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL       -- absolute TTL, set at insert
);

CREATE INDEX agent_events_agent_eid_idx ON agent_events (agent_id, event_id);  -- replay
CREATE INDEX agent_events_expires_idx   ON agent_events (expires_at);          -- sweeper
```

`event_id` is a **UUID v7** (`uuid.NewV7()`). v7 is time-ordered, so byte/string
comparison yields chronological ordering — that is what makes
`WHERE event_id > $cursor ORDER BY event_id` a correct "everything after this
cursor" query without a separate sequence column. If the system clock is
misconfigured `NewV7` fails and the code falls back to v4 (delivery still works,
replay ordering is weakened — this is logged).

## Event lifecycle

```
emit (task.assigned / .created / .status_changed / .commented / .mentioned)
  └─ AgentNotifyService.dispatch(agentID, event)
       1. event.EventID = uuid.NewV7()
       2. INSERT into agent_events, expires_at = now + TTL(event_type)   ← persist FIRST
       3. PUBLISH agent-notify:<agentID> on Redis                        ← then wake subscribers
       4. (optional) POST callback_url with retry
```

Persist-then-publish ordering is deliberate: the row is in Postgres **before**
any subscriber could be woken, so a client that reconnects and replays can never
miss an event that a live subscriber already saw.

## Retention

Retention is **TTL-based hard expiry**. Every row carries an absolute
`expires_at` stamped at insert time from a per-event-type policy:

| Event type | TTL | Rationale |
|------------|-----|-----------|
| `task.assigned` | 7 days | Agent may be offline for days; assignment must survive |
| `task.created` | 7 days | Same — work item must not be lost |
| `task.mentioned` | 7 days | Mentions are user-addressed, must survive offline windows |
| *(all other types)* | 24 hours | Status/comment churn is only useful while recent |

Source: `criticalSSEEventTypes` + `sseEventTTL()` in
`internal/service/agent_notify_service.go`.

An absolute `expires_at` is used rather than a relative `ttl_hours` column: it is
directly indexable for the sweeper (`WHERE expires_at < NOW()`) and needs no
per-query arithmetic.

### Sweeper

A goroutine in `cmd/api/main.go` runs every **5 minutes**:

```sql
DELETE FROM agent_events WHERE expires_at < NOW()
```

backed by `agent_events_expires_idx`, with a 30s statement timeout and graceful
shutdown. Deleted counts are logged (`[agent-events-sweeper] Deleted N expired
events`).

**The table is therefore self-bounding**: it holds at most ~7 days of agent
notifications. There is no unbounded-growth failure mode; no `VACUUM` cron and no
manual pruning are required.

## Replay (reconnect path)

`GET /agents/me/events/stream`, handler `EventStream`:

1. Read the `Last-Event-ID` header. If absent → live-only stream.
2. **Validate the cursor before sending SSE headers**: `Lookup(eventID)` (which
   filters `expires_at > NOW()`).
   - Cursor unknown or expired → **`410 Gone`** `{"error":"cursor_expired"}`.
     The client must then perform a full state recovery (e.g. re-fetch its task
     list). This is the contract for "you were gone longer than the retention
     window".
3. `Subscribe` to Redis **before** the replay DB query (closes the
   publish-between-query-and-subscribe race).
4. Send SSE headers (`text/event-stream`, `Cache-Control: no-cache`,
   `X-Accel-Buffering: no`).
5. Replay: `ListAfter(agentID, cursor, limit=500)` — only non-expired rows,
   ordered by `event_id`. Track `maxReplayed`.
6. Switch to live: forward Redis messages, **deduplicating** any with
   `event_id <= maxReplayed` (events buffered during the replay window).
7. Keepalive comment (`: ping`) every 30s.

```
client reconnect ──Last-Event-ID: <uuid>──▶ EventStream
  cursor valid?  ──no──▶ 410 Gone (do full recovery)
       │ yes
       ▼
  subscribe Redis ▶ replay rows > cursor ▶ live stream (dedup ≤ maxReplayed)
```

## Operational notes

- **Bounded growth** — guaranteed by `expires_at` + the sweeper; no archival
  step is required.
- **Indexes** — `(agent_id, event_id)` for replay, `(expires_at)` for sweep.
  Both are required; do not drop.
- **Reverse proxy** — `text/event-stream` responses must not be buffered. A
  fronting proxy must stream the API upstream (e.g. disable response buffering /
  set flush interval to immediate). `X-Accel-Buffering: no` is emitted for
  nginx-class proxies.
- **Failure modes** — a persist failure is non-fatal (logged; live delivery via
  Redis still happens, only that one event is lost from replay). A Redis publish
  failure means no live wake, but the row is durable and replays on the next
  reconnect.

## Potential future work

Not currently implemented — the present design is sufficient for the platform's
scale (small number of concurrent SSE connections, sub-3 KB event payloads, a
table self-bounded to ≤7 days). Listed with the condition that would justify
revisiting.

| Item | Why not now | Revisit when |
|------|-------------|--------------|
| Cold archive of expired events | Expired rows have dead cursors (un-replayable) and duplicate source-of-truth data already held in `tasks` / `comments` / `comment_mentions`. | A requirement to retain raw event history for compliance/audit appears. |
| gzip on the SSE stream | Egress is negligible at current scale, and gzip-on-SSE must be flush-aware per event (`gz.Flush()` + `rw.Flush()` after each write) or clients stall; off-the-shelf gzip middleware buffers and breaks streaming. | Concurrent SSE connections grow large, egress cost becomes measurable, or 500-event replay bursts become routine. |
| Streaming-broker backend (e.g. NATS JetStream) | Postgres + Redis comfortably handle the current event rate. | Event throughput grows by an order of magnitude. |
| Cross-region replication / event-sourcing rebuild | No disaster-recovery or rebuild requirement today. | If such a requirement is declared. |
