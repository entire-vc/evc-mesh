---
created: 2026-06-11T21:50+03:00
updated: 2026-06-11T21:50+03:00
author: Garfield-Mesh
status: proposed
project: mesh
type: adr
tags:
  - mesh
  - architecture
  - sse
  - nats
  - transport
---

# ADR-0001 — SSE → Durable Transport: Evaluation (NATS JetStream / Redis Streams)

**Status:** Proposed · awaiting Pavel review  
**Task:** [fc2acff3](https://mesh.entire.host/t/fc2acff3-6af1-462a-8fa4-97bc256daf20)  
**Depends on:** [81f5cec1] SSE cursor+replay (deployed 2026-05-21, prod b862db3)  
**Observation window:** 2026-05-21 → 2026-06-11 (21 days)

---

## Context

Task `81f5cec1` shipped SSE cursor+replay on 2026-05-21. The present ADR asks: does the resulting reliability justify staying with the current two-tier architecture, or do we need a third transport layer (NATS JetStream per-agent consumers, or Redis Streams)?

Before writing conclusions I audited the production stack and code. This changed the framing materially.

---

## Discovered Architecture (prod, as of 2026-06-11)

The Mesh event pipeline has three layers — not two:

```
Producer  ──publish──▶  NATS JetStream           (durable write path)
                         MESH_EVENTS stream
                         dedup window = 2 min
                              │
                       pg-writer consumer          (ACK-explicit, MaxDeliver=10)
                              │
                       PostgreSQL                  (event_bus_messages + agent_events)
                              │
                       Redis pub/sub               (live fan-out, ws:{workspace_slug})
                              │
                       SSE handler                 (Last-Event-ID cursor → DB replay)
                              │
                       Agent (dispatcher/fiddler)
```

Key facts:
- `MESH_EVENTS` JetStream stream: **created 2026-03-13**, continuously running; 13,226 total events published, **0 redeliveries, 0 pending** on `pg-writer` consumer as of today.
- Redis pub/sub delivers events to live SSE connections per workspace channel.
- `agent_events` table stores per-agent events with TTL: critical (`task.assigned`, `task.created`, `task.mentioned`) = **7 days**; others = **24 h**.
- On reconnect with `Last-Event-ID`, SSE handler replays from `agent_events` (up to 500 events per fetch, deduplicating against the Redis buffer window). Expired cursor → **HTTP 410 Gone** → client falls back to `get_my_tasks` full-state recovery.
- NATS JetStream also exposes a `Subscribe(agentID)` method that creates **per-agent durable pull consumers** on `MESH_EVENTS` — not currently wired to SSE, but the infrastructure is already present.

---

## Telemetry — 21-Day Window

| Metric | Value | Source |
|---|---|---|
| NATS `pg-writer` redeliveries | **0** | `/jsz?consumers=1` |
| NATS `pg-writer` ack-pending | **0** | `/jsz?consumers=1` |
| NATS API errors | **0** (of 110 calls) | `/jsz` |
| NATS stream messages (retained) | 12,099 (since 2026-05-13) | `/jsz` |
| Stale-redispatch actual dispatches | **0** (20 log lines = thread-start on restart, not actual stale-task actions) | `~/logs/mesh-dispatcher.log` |
| 410 Gone cursor-expiry events | **unknown** — no persistent counter | Server stdout, no log retention |
| Lost-event incidents | **0** confirmed | Task history, no escalations |

**Gap:** server stdout is not persisted beyond the running process lifetime; systemd journal retains only ~2 days on tw-mesh. A `410_gone_total` Prometheus counter would close this gap.

---

## Option Analysis

### Option A — NATS JetStream per-agent SSE consumers

Replace Redis pub/sub fan-out with per-agent durable pull consumers on `MESH_EVENTS`.

**Pros:**
- Eliminates Redis pub/sub as a SPOF for live delivery.
- ACK-explicit = messages can't be lost at the SSE delivery layer.
- Infrastructure already present: `Subscribe(agentID)` method and NATS running.

**Cons:**
- Per-agent consumers create one JetStream consumer per agent (currently ~15 agents). Manageable, but adds operational surface.
- NATS JetStream is already the backbone; wiring SSE to it would be an optimization of an already-working system.
- Replay windows would switch from PostgreSQL TTL (7d/24h, flexible) to NATS stream retention (file-based, currently ~29-day first_ts window). Divergent retention semantics.
- Moderate implementation effort: refactor SSE handler to pull from JetStream instead of Redis sub.

### Option B — Redis Streams

Add a Redis Streams layer (`XADD` + `XREAD` with consumer groups) for per-agent event delivery.

**Pros:**
- Redis already in stack.
- Consumer groups provide durable delivery semantics.

**Cons:**
- Purely additive over an already-sufficient stack. NATS JetStream + PostgreSQL already cover everything Redis Streams would provide; adding a third durable layer creates "durability layer soup."
- Redis Streams adds no benefit that JetStream doesn't already provide, and requires new code + ops.
- Increases complexity without reducing risk.

### Option C — Status quo (cursor+replay, current architecture)

Keep NATS JetStream as the write-path backbone (`pg-writer`), Redis pub/sub for live SSE fan-out, and cursor+replay from `agent_events` on reconnect.

**Pros:**
- 21 days with 0 redeliveries, 0 lost-event incidents, 0 stale-redispatch actions. Empirically working.
- Architecture is already two-tier at the write path (NATS JetStream → PostgreSQL). Adding a third tier for SSE delivery is not justified by the data.
- No new dependencies; operational surface stays flat.
- Cursor 410 Gone → `get_my_tasks` is a clean, tested graceful-degradation path.

**Cons:**
- Redis pub/sub can drop events if a connection closes mid-publish. This is covered by cursor replay, but only if the agent reconnects within the TTL window (24h/7d). An agent offline >7d after a critical event would need a full `get_my_tasks` recovery — which is the designed fallback and acceptable for the current team size.
- No visibility into 410 Gone frequency (gap noted below).

---

## Decision

**Recommendation: C — Status quo.**

### Rationale

1. **NATS JetStream is already tier-2.** The premise of the task was "evaluate adding NATS JetStream." It's already there. The `MESH_EVENTS` stream + `pg-writer` consumer has run without a single redelivery or lost event since March 2026. There is nothing to "add."

2. **21 days of prod data satisfy the decision criteria.** The task specified: lost-event rate <0.1%/month → C. No lost-event incidents were observed. The stale-redispatch safety net was not triggered. Cursor+replay covers the Redis pub/sub gap.

3. **Option A is a valid future upgrade, not a current necessity.** Wiring SSE directly to per-agent JetStream consumers (instead of Redis pub/sub) would eliminate the last non-durable hop. This makes sense if we scale to hundreds of concurrent agents or if Redis becomes a bottleneck. At 15 agents and 0 incidents, it's premature.

4. **Option B adds no value.** Redis Streams would duplicate NATS JetStream semantics on a layer already covered.

---

## Follow-up Actions (not blocking this close)

| # | Action | Owner | Priority |
|---|---|---|---|
| F1 | Add `sse_cursor_410_total` Prometheus counter in agent_handler.go (labeled by agent_id) | Linus | Low |
| F2 | Add `sse_replay_events_total` counter (events served per replay) | Linus | Low |
| F3 | Evaluate per-agent JetStream consumers for SSE at 50+ agents (revisit ADR-0001-A) | Garfield | Low, future |

F1 and F2 close the telemetry gap identified above and would make the next evaluation data-driven instead of proxy-driven.

---

## Migration Plan

N/A — Option C requires no migration.

---

## References

- `internal/eventbus/eventbus.go` — PublishEvent, Subscribe, NATS+Redis+PG wiring
- `internal/eventbus/nats.go` — MESH_EVENTS stream config
- `internal/eventbus/pgwriter.go` — pg-writer durable consumer
- `internal/handler/agent_handler.go:826` — SSE cursor+replay implementation
- `internal/service/agent_notify_service.go:40` — sseEventTTL (7d/24h)
- `internal/repository/postgres/agent_events_repo.go` — agent_events CRUD + DeleteExpired
- Prod NATS `/jsz`: 13,228 seqs, 0 redeliveries, 0 API errors (verified 2026-06-11)
- Task `81f5cec1` (SSE cursor+replay, deployed 2026-05-21)
