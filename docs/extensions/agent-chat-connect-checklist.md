# agent-chat: connecting a foreign instance with its own runner (checklist)

Acceptance script (draft). Each step has a command and an expected result; run it top to bottom
on an instance that is not ours, with a runner you operate. Contract details:
[runner-chat-protocol.md](runner-chat-protocol.md). Runner side: `[chat]` section of the
evc-mesh-runner README.

> The runner's `[chat]` support (E5 an internal ticket) is in the evc-mesh-runner README on main
> on the default branch. Steps 6 onwards need a runner build that contains it; if `once` rejects the
> `[chat]` section, record the runner version as the blocker, not as a doc error.

Variables used below: `MESH` (instance URL), `ADMIN` (admin user token), `WS`, `PROJ`
(workspace / project UUIDs), `AGENT` (UUID of an agent), `AKEY` (that agent's API key).

## 1. Instance is compatible
```bash
curl -s "$MESH/api/v1/extensions/manifest-schema" -H "Authorization: Bearer $ADMIN" | head -c 200
```
Expect: JSON Schema text (`"$schema"` present). Then
`curl -s "$MESH/api/v1/workspaces/$WS/extensions" -H "Authorization: Bearer $ADMIN" | grep -o '"agent-chat"'`
prints `"agent-chat"`. Not listed → the instance's `mesh_api` is older than `>=1 <2`: stop.

## 2. Connect `runner`, then `agent-chat`
`agent-chat` requires `runner`; connect it first through the Runner settings screen (its
settings are managed there, the generic connect refuses it). Then:
```bash
curl -s -X POST "$MESH/api/v1/workspaces/$WS/extensions/agent-chat/connect" \
  -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' \
  -d '{"settings":{},"consent":{"capabilities":["api:agents:read","api:projects:read","ui:project.settings","ui:workspace.settings"]}}'
```
Expect: `201`, `status` enabled. Without full consent: `400 validation_failed` with
`details.consent`. Repeat call: `409 already_connected`.
(Verify the exact consent strings against the live `GET …/extensions` entry for your version.)

## 3. Pick the agent for the project
```bash
curl -s -X PUT "$MESH/api/v1/projects/$PROJ/chat/settings" -H "Authorization: Bearer $ADMIN" \
  -H 'Content-Type: application/json' -d "{\"enabled\":true,\"agents\":[\"$AGENT\"]}"
curl -s "$MESH/api/v1/projects/$PROJ/chat/agents" -H "Authorization: Bearer $ADMIN"
```
Expect: `200`; the agent appears in the picker list.

## 4. Protocol gate (no runner yet)
```bash
curl -s -o /dev/null -w '%{http_code}\n' "$MESH/api/v1/runner/chat/claim?agents=$AGENT&timeout=1" \
  -H "X-Agent-Key: $AKEY" -H "X-Chat-Runner-Id: $(uuidgen)"
```
Expect `400` (`chat_protocol_unsupported`: header missing). Repeat with `-H 'X-Chat-Protocol: 1'`:
expect `204` (nothing queued). Negative control: a user token instead of `$AKEY` → `401`/`403`.

## 5. Register the runner
```bash
curl -s -X POST "$MESH/api/v1/runner/chat/runners" -H "X-Agent-Key: $AKEY" \
  -H 'X-Chat-Protocol: 1' -H 'Content-Type: application/json' -d '{"name":"checklist-runner"}'
```
Expect `201 {runner_id, token, …}`; the `token` is shown once. Store it in a chmod 600 file.

## 6. Configure and start the runner
```toml
# runner.toml (excerpt)
[runner]
mesh_url = "https://<your-mesh>"
[chat]
enabled = true
agents = ["<agent name>"]
credential_file = "~/.config/mesh-runner/chat.credential"  # fallback, only for an agent without key_file
chat_workspace = "none"
[[agents]]
name = "<agent name>"
key_file = "~/.config/mesh-runner/<agent>.key"
harness = "claude"
profile_dir = "~/agents/<agent>"
```
```bash
chmod 600 ~/.config/mesh-runner/*.key ~/.config/mesh-runner/chat.credential
mesh-runner once --config runner.toml
mesh-runner status --config runner.toml
```
Expect: no error from `once` and `status`. A key/credential file readable by group/others must be
refused (`... is readable by group/others; chmod 600`).

## 7. One round trip
```bash
T=$(curl -s -X POST "$MESH/api/v1/projects/$PROJ/chat/threads" -H "Authorization: Bearer $ADMIN" \
  -H 'Content-Type: application/json' -d "{\"agent_id\":\"$AGENT\",\"title\":\"checklist\"}" | jq -r .id)
curl -s -X POST "$MESH/api/v1/projects/$PROJ/chat/threads/$T/messages" -H "Authorization: Bearer $ADMIN" \
  -H 'Content-Type: application/json' -d '{"client_message_id":"'$(uuidgen)'","body":"Reply with the word pong."}'
```
Expect `201` then `202` (turn queued). With `mesh-runner run` active, within about a minute:
```bash
curl -s "$MESH/api/v1/projects/$PROJ/chat/threads/$T/messages" -H "Authorization: Bearer $ADMIN" \
  | jq -e '[.items[] | select(.role=="assistant" and .status=="done" and (.body | test("pong"; "i")))] | length > 0'
```
The list is a page `{items, has_more, next_cursor, active_turn}`, newest message first. Expect `true`
and exit 0: a finished agent message (`role` is `assistant`) containing `pong`. An empty `items`, a
`failed` message with `error_code`, or a reply without `pong` exits non-zero.

## 8. Stop
```bash
curl -s -X POST "$MESH/api/v1/projects/$PROJ/chat/threads/$T/messages" -H "Authorization: Bearer $ADMIN" \
  -H 'Content-Type: application/json' \
  -d '{"client_message_id":"'$(uuidgen)'","body":"Count slowly from 1 to 500, one number per line."}'
curl -s -X POST "$MESH/api/v1/projects/$PROJ/chat/threads/$T/stop" -H "Authorization: Bearer $ADMIN" -w '%{http_code}\n'
```
Expect `200` `{"stopped":true,"turn":{…}}` from `stop`. The runner sees `cancel_requested` with its next lease renewal or
event batch (cancel is polled, protocol §5), so within 60 s the turn ends:
```bash
curl -s "$MESH/api/v1/projects/$PROJ/chat/threads/$T" -H "Authorization: Bearer $ADMIN" | jq .
```
Expect the thread with no turn left running, and the agent message shorter than 500 numbers.

## 9. Offline expiry
```bash
curl -s -X PUT "$MESH/api/v1/workspaces/$WS/chat/settings" -H "Authorization: Bearer $ADMIN" \
  -H 'Content-Type: application/json' -d '{"offline_expiry_hours":1}'
# PUT replaces the workspace chat settings: add any other keys you set earlier
# stop the runner (Ctrl-C of `mesh-runner run`), then queue a message:
curl -s -X POST "$MESH/api/v1/projects/$PROJ/chat/threads/$T/messages" -H "Authorization: Bearer $ADMIN" \
  -H 'Content-Type: application/json' -d '{"client_message_id":"'$(uuidgen)'","body":"offline probe"}'
# after more than an hour (the sweeper runs about once a minute), start the runner again and read the thread:
curl -s "$MESH/api/v1/projects/$PROJ/chat/threads/$T/messages" -H "Authorization: Bearer $ADMIN" | jq -e '.items[0] | select(.role=="user" and .status=="failed" and .error_code=="not_delivered")'
```
Expect the probe message (`items[0]`, the newest) with `status` `failed` and `error_code`
`not_delivered` (its turn is `expired`), exit 0, and no agent reply to it once the runner is back.
Any other newest message, an empty page or a delivered probe exits non-zero. This is the one step that needs a real wait; run it last or in parallel.

## 10. Delete and purge
```bash
curl -s -o /dev/null -w '%{http_code}\n' -X DELETE "$MESH/api/v1/projects/$PROJ/chat/threads/$T" \
  -H "Authorization: Bearer $ADMIN"
```
Expect `204`; the runner receives `delete_transcript` and acks `done` (`GET /runner/chat/commands`
no longer returns it). Thread data is purged after 30 days.

## 11. Disconnect
```bash
curl -s -X DELETE "$MESH/api/v1/workspaces/$WS/extensions/agent-chat?data=keep" -H "Authorization: Bearer $ADMIN" -w '%{http_code}\n'
curl -s -o /dev/null -w '%{http_code}\n' -X DELETE "$MESH/api/v1/runner/chat/runners/<runner_id>" -H "X-Agent-Key: $AKEY"
```
Expect `204` both; the runner's next claim fails and it stops serving chats.

Record per step: command, actual output, pass/fail. Steps 6–10 depend on E5.
