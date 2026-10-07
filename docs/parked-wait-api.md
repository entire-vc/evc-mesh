# Atomic parked WAIT release v1

Two task-scoped writes use the same workspace access and `update_task` permission
as `POST /api/v1/tasks/:task_id/move`. They keep the project and assignee intact.
The authenticated request context supplies the actor; JSON cannot override it.
No route accepts a complete labels replacement or an arbitrary target status.

## Discovery and registration

`POST /api/v1/tasks/:task_id/parked-waits` persists an explicit registration.
The caller must retain its persisted feed/session receipt: task, project, owner,
feed timestamps and the owner-authored WAIT comment. Only comments created within
that closed feed window qualify. A receipt UUID is audit provenance, not a secret
or proof of authorization. The server validates the exact current task snapshot,
comment author, latest owner WAIT and its condition; it does not infer a WAIT from
edges or scan legacy history to fabricate a receipt. Cold restart discovery belongs
to the consumer's durable receipt registry; never delete its Backlog records merely
because a WAIT arrived after the feed closed.

`feed_source` is required and accepts only `confirmed_feed`. `project_id`,
`owner_id` and `owner_type` must come from the receipt captured at that feed,
never from a later task GET. Missing source, `_REFEED`, `legacy` and `current_task`
are rejected with 400. Legacy lane/tid/feeds records lacking historical project
remain discovery-only: retain their tombstones and obtain a new confirmed feed
before registration. Do not backfill the source marker or identity from current
state. A captured identity that no longer matches the task returns 409 even if
the caller refreshes `expected_version`.

The authenticated fleet consumer is responsible for the authenticity and durable
storage of its confirmed feed receipt. The source marker is an explicit protocol
assertion, not a signed proof; this API cannot reconstruct missing historical
identity or certify a fabricated receipt. The server checks the asserted captured
identity, owner WAIT window and current snapshot under the task lock.

Persist the registration UUID and complete request before calling. Register only
after checkout release and the WAIT comment have committed; GET the task again for
the resulting version. Registration does not mutate the task, remove labels, clear
`start_after`, or change its lease. Identical requests return their stored result.
An old WAIT cannot be registered again against a newer task version: a changed
snapshot requires a fresh, proven owner WAIT, not an automatic snapshot refresh.

Example `registration.json` (replace all UUIDs/timestamps with the stored receipt
and current task data):

```json
{
  "registration_id": "11111111-1111-4111-8111-111111111111",
  "project_id": "22222222-2222-4222-8222-222222222222",
  "owner_id": "33333333-3333-4333-8333-333333333333",
  "owner_type": "agent",
  "expected_version": 42,
  "wait_comment_id": "44444444-4444-4444-8444-444444444444",
  "feed_receipt_id": "55555555-5555-4555-8555-555555555555",
  "feed_source": "confirmed_feed",
  "feed_received_at": "2026-10-07T04:00:00Z",
  "feed_closed_at": "2026-10-07T04:10:00Z",
  "reason": "pipeline",
  "condition": { "project_path": "entire-vc/evc-mesh", "pipeline_id": 9176 },
  "remove_labels": ["park:date"],
  "expected_start_after": null,
  "clear_start_after": false,
  "expected_lease": { "mode": "absent", "generation": 7 }
}
```

Field rules:

| Field | Contract |
| --- | --- |
| `registration_id` | Caller-persisted UUID, stable for retries; public park generation. |
| `project_id`, `owner_id`, `owner_type` | Historical identity captured in the confirmed feed receipt, required to match the task's current project and assignee. Never hydrate from a later GET. Human/supervised owners remain gated. |
| `expected_version` | Positive task version after WAIT/checkout writers commit. |
| `wait_comment_id` | Exact latest owner-authored first-line `⏳ WAIT …` comment ID. |
| `feed_receipt_id`, timestamps | Persisted caller receipt; WAIT creation must be inside the closed window. Future windows are rejected (one minute clock allowance). |
| `feed_source` | Required literal `confirmed_feed`; unknown/missing/legacy sources are rejected. No legacy registration fallback. |
| `reason`, `condition` | Exactly one of the conditions below; independent or unknown reasons are rejected. |
| `condition.required_jobs` | Optional for pipelines only: 1–20 unique, nonempty exact job names. The registered list is immutable; release requests do not supply names or readiness. Omit to retain aggregate terminal semantics. An explicit empty list is rejected. |
| `remove_labels` | At most eight unique existing labels from the reason-specific allowlist. Empty array, null or omitted all subtract nothing; remaining park/gate labels still hold. |
| `expected_start_after` | Exact current timestamp or null; must match even when it is not cleared. |
| `clear_start_after` | Only if WAIT comment metadata contains `{"parked_wait":{"start_after":"<same timestamp>"}}`. Caller assertion alone is insufficient. |
| `expected_lease` | `absent` requires all lease fields absent and exact generation. Expired-but-present leases are not absent. `owned` requires exact generation, holder and session UUID; authenticated actor must be the current live holder. No checkout token is transmitted. |

| Reason | Condition and owner WAIT | Removable labels |
| --- | --- | --- |
| `dependency` | `{"task_id":"<full blocker UUID>"}`; first line `⏳ WAIT card:#<blocker8>: …`; named edge must be `blocks`. Every `blocks` edge must be done/cancelled at release. Deleted blockers do not count as done. | `park:dependency`, `park:wait-dependency`, `wake:dependency_edges`, `park:wait-external` |
| `pipeline` | `{"project_path":"group/project","pipeline_id":9176}`; first line `⏳ WAIT pipeline:group/project#9176: …`. Server checks this exact pipeline through workspace-configured GitLab credentials. | `park:date`, `park:pipeline`, `park:wait-ci` |
| `date` | `{"not_before":"<UTC timestamp>","time_semantics":"not_before"}`; first line `⏳ WAIT 20m: …`; timestamp must equal comment creation + 20 minutes. No inferred pipeline/date semantics. | `park:date` |

Explicit dependency condition + matching owner WAIT permits removing an erroneously
classified dependency-only `park:wait-external`. The presence of edges alone does
not reclassify external/manual/human waits. Pipeline IDs and task prefixes must end
at a token boundary; `#9176` does not match `#91760`.

All independent human/manual/freeze/no-promote gates hold. A future unowned
`start_after` holds even if the pipeline or blockers are terminal. The optional
canonical `custom_fields.park_reason` must agree with the registered reason.

### Required jobs in a mixed pipeline

Rollout gate: this additive field is pending deployment until its merge and the
production API version and required-job validation are verified. The contract was
published to Khan, the parked-wake consumer owner, on 2026-10-07 in integration
task `65a9d860-251d-4590-97c1-4cd19319ce3f`, comments
`5772639b-c232-4260-85f6-b18b9a6376b0` (schema) and
`9bb8a77d-31c4-4ca4-975e-1b3f18ca9d0c` (implementation and rollout gate).
The handoff specifies the optional immutable pipeline-only list, exact retry,
legacy behavior when absent and retained WAIT until live support. Consumer
activation must follow
that server verification and its own integration acceptance. A published schema
alone is not proof that the running API supports it.

For a pipeline that remains `manual` because unrelated products have manual jobs,
register the specific jobs the wait needs:

```json
{
  "project_path": "group/deploy",
  "pipeline_id": 9181,
  "required_jobs": ["build:product", "deploy:product", "verify:product"]
}
```

The server reads jobs from the workspace-configured GitLab instance, includes all
retry attempts, verifies the exact pipeline ID on every row, and selects the
largest job ID for each exact name. Readiness requires every selected job to be
`success`; unrelated `manual` or `failed` jobs and the aggregate status do not
affect readiness. Missing, skipped, running, failed, cancelled, pending or newly
retried required jobs prevent release. There is no client-provided `ready` flag.

Every page must have consistent pagination metadata and the complete expected
number of rows. Missing metadata, duplicate/unsorted IDs, provider failures,
truncated responses and changing pagination fail closed. Verification is bounded
to 100 pages of 100 jobs and eight seconds for the entire verification. Every page
is read again to detect status changes anywhere in the snapshot; the head is
checked once more after a multi-page verification to detect retries during that
second traversal.
This verifies a provider snapshot; GitLab and the task transaction do not share a
lock. A retry created after the final provider read is a subsequent event.

The release body, owner/project/version/lease/human/all-blockers fences and exact
retry behavior remain unchanged. The list must be persisted with registration
before sending; changing it requires a fresh owner-authored registration rather
than retrying a different body. Activity records include `required_jobs`; when it
is present, `pipeline_status: "success"` describes the required-job verification,
not an aggregate success claim. A committed release replays its stored receipt
without contacting GitLab, even after a lost response and later provider failure.
Provider verification runs under the release transaction's task and receipt locks.
Workspace and provider configuration are resolved before opening that transaction,
so verification never reserves another database connection while holding its own.
Concurrent retries wait for that receipt and replay it without another provider
read. A verification failure rolls back the transaction and retains the WAIT.
Without `required_jobs`, the existing aggregate terminal behavior remains intact.

## Terminal event and release

`POST /api/v1/tasks/:task_id/parked-waits/release`:

```json
{
  "registration_id": "11111111-1111-4111-8111-111111111111",
  "expected_version": 42,
  "release_id": "66666666-6666-4666-8666-666666666666",
  "trigger": { "kind": "pipeline", "event_id": "gitlab:entire-vc/evc-mesh:9176:terminal" }
}
```

Persist `release_id` and the complete body before sending. `trigger.kind` must equal
the registration reason; `event_id` is a nonempty caller event/receipt ID of at most
256 characters. It supplies provenance, never authorization or proof of completion.
There is no accepted caller `status` or `actor` field. Terminal proof comes from
server-side GitLab lookup (`success`, `failed`, `canceled`, `skipped`), locked blocker
rows, or the database clock. A failed pipeline is a terminal wake event, not success
evidence. GitLab unavailable/missing configuration returns 503; running/manual/
scheduled pipelines return 409. Configured GitLab host is never read from the body.

The transaction locks the task and registered generation, verifies version, lease,
all task gates, labels and start_after, then locks every blocker. Categories are read
in a fresh statement after the blocker locks: a joined read can otherwise retain a
pre-lock `done` category while a blocker concurrently reopens. Dependency changes
invalidate task.version through a trigger. Concurrent gate/lease/comment/repark
writers also invalidate it. A conflict does not subtract labels or wake the task.

The transaction subtracts only the explicit labels, optionally clears WAIT-owned
start_after/holder lease, moves Backlog → the same project's first Todo status,
inserts one visible `task.moved` Activity and stores the release receipt. Failure of
any write rolls the entire transaction back. Ordinary task PATCH/move remain usable.
Activity records server actor, source `parked-wait-api`, reason, trigger, exact WAIT,
feed receipt, registration/release IDs, removed labels and version transition. Its
usual event/notification is published after commit; audit durability does not depend
on delivery of that notification. Polling consumers must recover from the task and
durable receipt state.

200 response:

```json
{
  "registration_id": "11111111-1111-4111-8111-111111111111",
  "task_id": "77777777-7777-4777-8777-777777777777",
  "project_id": "22222222-2222-4222-8222-222222222222",
  "owner_id": "33333333-3333-4333-8333-333333333333",
  "version": 43,
  "activity_id": "88888888-8888-4888-8888-888888888888",
  "released": true,
  "replayed": false
}
```

An identical retry by the same authenticated actor returns this stored result with
`replayed:true`, including after later task changes or provider outage. It never
creates another Activity or re-executes the transition. A different release ID/body/
actor for an already released generation returns 409. Registration responses have
`released:false` and no activity_id until release. Standard structured API errors:
400 malformed plan; 401/403 auth/permissions; 404 missing task/registration; 409
snapshot/condition conflict; 503 external verification unavailable. On 409, retain
the consumer's record and reconcile current task state; do not fall back to labels
PATCH, retry with a blindly updated version, or force-release another holder.

## Positive and negative curl controls

Use only an isolated same-project, same-owner fixture. Supply `$MESH_API` including
`/api/v1`, `$TASK_ID` and the normal authenticated header from a named credential
carrier; never log its value. The examples assume `$MESH_AUTH_HEADER` is populated
securely. Request files contain no credentials.

```sh
curl --fail-with-body --silent --show-error \
  -H "$MESH_AUTH_HEADER" -H 'Content-Type: application/json' \
  --data-binary @registration.json "$MESH_API/tasks/$TASK_ID/parked-waits" > registered.json
jq -e '.released == false and .registration_id != null' registered.json

# Red: stale expected_version, running pipeline, future date, added gate/lease,
# or any unfinished blocker must return 409 and preserve labels/Backlog/owner.
jq '.expected_version -= 1' release.json > stale-release.json
curl --silent --show-error -o stale-response.json -w '%{http_code}\n' \
  -H "$MESH_AUTH_HEADER" -H 'Content-Type: application/json' \
  --data-binary @stale-release.json "$MESH_API/tasks/$TASK_ID/parked-waits/release"
jq -e '.code == 409' stale-response.json

# Green: all guards satisfied and a server-confirmed terminal event.
curl --fail-with-body --silent --show-error \
  -H "$MESH_AUTH_HEADER" -H 'Content-Type: application/json' \
  --data-binary @release.json "$MESH_API/tasks/$TASK_ID/parked-waits/release" > released.json
jq -e '.released == true and .replayed == false and .activity_id != null' released.json

# Lost-response retry: unchanged body, authenticated actor and release_id.
curl --fail-with-body --silent --show-error \
  -H "$MESH_AUTH_HEADER" -H 'Content-Type: application/json' \
  --data-binary @release.json "$MESH_API/tasks/$TASK_ID/parked-waits/release" > replayed.json
jq -e '.released == true and .replayed == true' replayed.json
jq -s -e '.[0].activity_id == .[1].activity_id and .[0].version == .[1].version' released.json replayed.json
```

Also assert GET task content and its Activity collection: Todo, unchanged project/
owner, unrelated labels retained and exactly one returned activity_id with matching
source/reason/trigger/WAIT provenance. HTTP 200 alone is not a live control.

## Migration and rollback

Additive migration `20261007003_parked_wait_release.sql` creates the registration/
receipt table and dependency snapshot trigger. It uses already-delivered task CAS;
no generic reaper/outbox changes. Local PostgreSQL tests cover rollback/up, races,
terminal controls and failure/retry. Before production migration capture the database
backup and previous binary. Binary rollback preserves additive schema and receipts;
prefer it. Schema down drops registrations/receipts: use only before real writes or
after an explicit verified backup, with the new routes disabled and callers stopped.
Do not downgrade the prerequisite task.version migration while this migration exists.
