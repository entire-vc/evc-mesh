# Agent repository access

Which repositories an agent may work in. Mesh stores the list; the runner reads
it on every pass and compares the normalized triples.

## Read

`GET /agents/me` and `GET /agents/{id}` carry `repository_access`:

| Value | Meaning |
|---|---|
| field absent | not configured: the runner keeps its legacy list |
| `{"revision": 2, "repositories": []}` | configured, no repository allowed |
| `{"revision": 1, "repositories": [{"provider": "gitlab", "host": "git.example.org", "project_path": "entire-vc/evc-mesh"}]}` | configured list |

Only these two responses load it. On list endpoints the field is always absent
and says nothing. A failed read fails the request (500), never returns the
"absent" form.

## Write

`PUT /agents/{id}/repository-access` is the only writer.

```json
{"expected_revision": 1, "repositories": [{"provider": "gitlab", "host": "git.example.org", "project_path": "entire-vc/evc-mesh"}]}
```

- `expected_revision` is the revision you read; `0` when the field was absent.
- `200` returns the stored `{revision, repositories}` with `revision` = old + 1.
- `409` when the stored revision differs: read again and retry.
- `400` for a malformed body: anything but exactly one JSON object, a missing or
  null field, an unknown field, or a list the normalization refuses.
- `403` when the caller may not write; `404` for an unknown agent.

There is no way back to "not configured" through the API.

### Normalization

Applied on write; the response and every read show the normalized form.

- `provider`: lowercased, `gitlab` or `github`.
- `host`: lowercased; an `http://` or `https://` prefix and one trailing `/` are
  removed, with the scheme's default port (`:443` for https and a bare host,
  `:80` for http). Any other scheme, user info, path or query is refused. Another
  port (1..65535, no leading zero) stays part of the host.
- `project_path`: trailing `/` and one `.git` suffix removed; at least
  `group/project`; no leading `/`, empty, `.` or `..` segment; case is kept.
- A triple that appears twice after normalization refuses the whole list.
- At most 200 entries.

### Who may write

- the agent's operator (a signed-in person);
- an owner or admin of the agent's workspace;
- another agent of the same operator holding a live runtime_manager grant
  ("Can manage My Runner"), with its own agent key.

Refused: the agent itself (also when it is a runtime_manager), any other agent,
a person who is neither operator nor owner/admin, and OAuth connectors.

`PATCH /agents/{id}`, `PATCH /agents/me`, `PUT /agents/{id}/profile` (the MCP
`update_agent_profile` path), `POST /agents/heartbeat` and
`POST /workspaces/{ws_id}/config/import` ignore a `repository_access` field.

## Audit

Every successful write adds one agent activity event
(`repository_access_changed`) in the same transaction. The editor reads it as:

`GET /agents/{id}/repository-access/history?limit=&cursor=`

```json
{
  "items": [
    {
      "id": "<event uuid>",
      "created_at": "2026-11-11T18:02:11.123456Z",
      "actor": {"type": "user", "id": "<user or agent uuid>", "name": "Display Name"},
      "old": {"revision": 1, "repositories": [{"provider": "gitlab", "host": "git.example.org", "project_path": "entire-vc/evc-mesh"}]},
      "new": {"revision": 2, "repositories": []}
    }
  ],
  "next_cursor": "<opaque string>"
}
```

- Newest first. `limit` 1..100, default 20; `next_cursor` is null on the last
  page, otherwise pass it back as `cursor`. A bad `limit` or `cursor` is `400`.
- `old` is null for the first write (the agent was not configured before).
- `actor.type` is `user` or `agent` (a runtime_manager). `actor.name` is the
  person's display name (username when unset) or the agent's name.
- Readable by whoever may read `GET /agents/{id}`; `404` for an unknown agent.

The same events, raw, are in
`GET /agents/{id}/activity?event_type=repository_access_changed` with
`metadata = {actor_type, actor_id, old_revision, new_revision, old_repositories,
new_repositories}` (`old_revision: 0`, `old_repositories: null` for the first
write). `POST /agents/{id}/activity` refuses this event type, so the entries
cannot be written by hand.

## Storage

`agents.repository_access` (JSONB array) and `agents.repository_access_revision`
(BIGINT), both NULL until the first write; a check keeps them NULL together or
set together with revision >= 1. Migration `20261111300_agent_repository_access`
is additive and its Down drops both columns.
