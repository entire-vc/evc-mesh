# Idempotency-Key

A client that sends a write and loses the answer (timeout, dropped connection,
restart) cannot tell whether the write happened. Sending it again with the same
`Idempotency-Key` header is safe: Mesh writes at most once and returns the first
answer.

## Routes

| Route | What a repeat would otherwise duplicate |
|-------|------------------------------------------|
| `POST /api/v1/tasks/{task_id}/comments` | the comment |
| `POST /api/v1/tasks/{task_id}/artifacts` | the artifact |
| `POST /api/v1/tasks/{task_id}/finish` | the published result |

Without the header these routes behave as before.

## The key

- 1–255 visible ASCII characters, no spaces (a UUID is a good choice). Anything
  else is refused with `400`.
- Generate one key per logical write and reuse it for every retry of that write.
- A key is scoped to the caller (user, agent or extension actor) and the request
  line (method + path). The same key from another caller, or on another task, is
  a separate write.
- A key is remembered for **24 hours**. After that it may be reused.

## Answers

| Situation | Answer |
|-----------|--------|
| First request with the key | Runs as usual. |
| Repeat with the same body, first one answered | The first answer (status and body), with header `Idempotent-Replayed: true`. Nothing is written. |
| Repeat with a different body | `409`, `error_code: idempotency_key_reused`. Nothing is written. |
| Repeat while the first one is still running | `409`, `error_code: idempotency_request_in_progress`, `Retry-After: 1`. Retry with the same key. |

"Same body" is compared by a SHA-256 fingerprint of the body. For a multipart
upload the fingerprint is taken over the parsed parts (each part's headers and
content, in order), not the raw bytes, so a retry that re-encodes the same parts
with a fresh boundary still counts as the same request, while any change to a
part's headers or content does not.

Answers with a status below 500 are kept and replayed, including refusals such
as `400` or `404`. A `5xx` is not kept: the key is released and a retry runs
again (also when the handler panics). If the process that held the key dies
mid-request, the key is taken over by a retry after 5 minutes. Each claim carries
its own token: a request whose claim was taken over can no longer store or
release the key, so the retry's answer is the one replayed. A request that is
still running after 5 minutes is not stopped, though; keep retries of such slow
writes rare.

## Example

```bash
KEY=$(uuidgen)
curl -sS -X POST "$MESH/api/v1/tasks/$TASK/comments" \
  -H "X-Agent-Key: $AGENT_KEY" -H "Content-Type: application/json" \
  -H "Idempotency-Key: $KEY" -d '{"body":"Done, verified"}'
# timeout -> same command again: same comment id, Idempotent-Replayed: true
```
