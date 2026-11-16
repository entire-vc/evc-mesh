# Idempotency-Key

A client that sends a write and loses the answer (timeout, dropped connection,
restart) cannot tell whether the write happened. Sending it again with the same
`Idempotency-Key` header is safe: on the routes below Mesh writes at most once
and answers the repeat with the write that happened.

## Routes

| Route | Receipt route | What a repeat would otherwise duplicate |
|-------|---------------|------------------------------------------|
| `POST /api/v1/tasks/{task_id}/comments` | `comment.create` | the comment |
| `POST /api/v1/tasks/{task_id}/artifacts` | `artifact.create` | the artifact |
| `POST /api/v1/tasks/{task_id}/finish` | `task.finish` | the published result (outcome and closing comment) |
| `POST /api/v1/agents/me/sessions/report` | `session.report` | the reported tokens and cost, added to the session a second time |
| `POST /api/v1/tasks/{task_id}/move` | — (answer store only, see below) | the move and its side effects |

Without the header every route behaves as it does without idempotency: each
request runs and writes.

## Two layers

**The receipt** (the four routes with a receipt route). The key is recorded
next to the written row, in the same database transaction as the write: a
row in `idempotency_receipts` naming the actor, the receipt route, the task,
the key, the request fingerprint, the written row's id and a snapshot of the
written object. The write and its receipt commit together or not at all.

**The answer store** (all five routes). The middleware claims the key before
the handler runs and stores the answer after it. It catches a repeat that
arrives while the first request is still running, and replays the exact first
answer for 24 hours.

A repeat is answered from the answer store when it has the answer, else from
the receipt, else it runs. So the receipt decides in every case the answer
store does not cover.

## Guarantees

On `comments`, `artifacts`, `finish` and `sessions/report`, with the same key
from the same caller on the same task:

1. **A write that happened is never repeated**, for as long as its receipt
   exists (receipts do not expire), whatever happened to the answer store:
   - the answer was not stored (the process died or the store failed between
     the commit and storing the answer);
   - the answer was a `5xx` after the write had committed (a `5xx` releases
     the answer store's claim at once);
   - the 24 hours of the answer store are over;
   - a claim was taken over after 5 minutes while the first request was still
     running.
2. **The repeat answers with that write**: the same id, the success status of
   the route and header `Idempotent-Replayed: true`, before the checkout fence,
   a gate or the task's current state is checked. The write happened; the
   repeat reconciles to it.
3. **Concurrent repeats write once.** The receipt is unique per actor, receipt
   route, task and key; a second transaction with the same key waits for the
   first and rolls back if it committed. Its request answers with the first
   write. A refusal caused by the first write (the task's version moved, the
   lock is gone) is checked against the receipt first, so it also answers with
   the first write.
4. **Another body under a used key is refused**: `409`,
   `error_code: idempotency_key_reused`, nothing written, also after the answer
   store forgot the key.
5. **A write that did not happen is not reported as done.** A refusal
   (`4xx`) writes nothing and records no receipt. A retry with the same key
   runs again against the current state, so a `finish` refused by a gate
   succeeds once the gate is met. With a checkout fence (`X-Mesh-Checkout-Token`
   and the rest, see `docs/extensions/agents-me-contract.md`, "Fencing a stale
   worker"), a write that did not happen is not performed under a lost lock:
   the retry is refused again with `409 checkout_precondition_failed`.

The task in the receipt is the resolved task id. A repeat through the task's
short id and one through its full id are the same write.

## Reconciling by reading

A caller that lost the answer can find out whether its write happened without
sending it again:

```
GET /api/v1/me/idempotency-receipts?route=<receipt route>&key=<key>&task_id=<task uuid>
```

- `200`: the write happened. The body has `resource_id` (the comment, artifact,
  turn outcome or session id), `created_at` and `response`, the written object
  as the write answered it.
- `404`, `error_code: idempotency_receipt_not_found`: this key wrote nothing on
  this route and task. Sending the write again with the same key writes it once.

`task_id` is the full task UUID. Leave it out for a usage report sent without
`task_id`. Only the caller's own receipts are visible.

## The key and its scope

- 1–255 visible ASCII characters, no spaces (a UUID is a good choice). Anything
  else is refused with `400`.
- Generate one key per logical write and reuse it for every retry of that write.
- The scope is the caller (user, agent or extension actor), the route and the
  task. **The same key from another caller is a separate write**, and so is the
  same key on another task. If the work moves to another agent (a handoff, a new
  checkout by someone else), the new agent's retry with the old key does not
  find the old agent's write: the new agent reconciles by reading the task
  (its comments, artifacts and outcomes), not by repeating the write.

## Answers

| Situation | Answer |
|-----------|--------|
| First request with the key | Runs as usual. |
| Repeat with the same body, write happened | The write: same id, the route's success status, `Idempotent-Replayed: true`. Nothing is written. |
| Repeat with a different body | `409`, `error_code: idempotency_key_reused`. Nothing is written. |
| Repeat while the first one is still running (answer store) | `409`, `error_code: idempotency_request_in_progress`, `Retry-After: 1`. Retry with the same key. |
| Repeat of a refused request | Runs again against the current state. |

"Same body" is compared by a SHA-256 fingerprint of the body. For a multipart
upload the fingerprint is taken over the parsed parts (each part's headers and
content, in order), not the raw bytes. A retry that re-encodes the same parts
with a fresh boundary is the same request; any change to a part's headers or
content is a different one.

## What a replay carries

A replay from the answer store is the first answer byte for byte. A replay from
the receipt is built from the snapshot taken when the row was written:

- comment: the comment as created (with `author_name`), and its `url`;
- artifact: the artifact as created;
- finish: `outcome` and `comment_id`. `task` is left out and `released` is
  `false`: read the task for its current status and lock;
- usage report: `session_id` and the session's totals as of that report, not
  as of now.

## Not covered

- `POST /tasks/{task_id}/move` and the extension routes `move-to-catalog` and
  `repin` have only the answer store: after a lost answer, a 5xx after the
  commit or 24 hours, a repeat runs again.
- A usage report's `tool_breakdown` is added in a second statement after the
  tokens. If that statement fails, the tokens stay counted and a repeat answers
  from the receipt without adding the breakdown.
- A receipt outlives its row. If the comment or artifact is deleted later, a
  repeat still answers with the snapshot and writes nothing.
- An answer-store claim still running after 5 minutes is not stopped; the
  receipt keeps the second request from writing, but keep such slow writes rare.

## Example

```bash
KEY=$(uuidgen)
curl -sS -X POST "$MESH/api/v1/tasks/$TASK/comments" \
  -H "X-Agent-Key: $AGENT_KEY" -H "Content-Type: application/json" \
  -H "Idempotency-Key: $KEY" -d '{"body":"Done, verified"}'
# timeout -> same command again: same comment id, Idempotent-Replayed: true
# or read without writing:
curl -sS "$MESH/api/v1/me/idempotency-receipts?route=comment.create&task_id=$TASK&key=$KEY" \
  -H "X-Agent-Key: $AGENT_KEY"
```
