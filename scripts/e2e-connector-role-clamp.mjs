#!/usr/bin/env node
/**
 * Black-box check that an OAuth connector (mot_ token) is held to its user's
 * workspace role on routes that carry no rbac() — MCP-OAuth sec, #266eae6a.
 *
 * Real API, real Postgres, real OAuth flow (DCR → authorize → consent → code →
 * token). Nothing is mocked. Every assertion is about a RESULT (a status code
 * from a real request, a row that did or did not change), and each denial is
 * paired with an allowed control on the same route in the same run, so a "403"
 * that merely means "the route is broken" cannot pass.
 *
 * Usage:  API=http://localhost:8096 node scripts/e2e-connector-role-clamp.mjs
 * Exit 0 = every check passed. Exit 1 = a check failed (or the run broke).
 *
 * Precondition for the project-scoped routes: a connector reaches a project only
 * once it is a project member (RequireProjectMember never lets an agent in on
 * the strength of a workspace role). The script adds the connectors as project
 * members exactly as an admin would, because that is the state in which the gap
 * is reachable.
 */
import { createHash, randomBytes } from "node:crypto";

const API = (process.env.API ?? "http://localhost:8096").replace(/\/$/, "");
const REDIRECT = "http://127.0.0.1:5599/cb";
const PASSWORD = "ConnectorClamp1";
const RUN = randomBytes(3).toString("hex");

const results = [];
function check(name, ok, detail = "") {
  results.push({ name, ok });
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? `  — ${detail}` : ""}`);
}

async function call(method, path, { token, agentKey, body, form } = {}) {
  const headers = {};
  if (token) headers.authorization = `Bearer ${token}`;
  if (agentKey) headers["x-agent-key"] = agentKey;
  let payload;
  if (form) {
    headers["content-type"] = "application/x-www-form-urlencoded";
    payload = new URLSearchParams(form);
  } else if (body !== undefined) {
    headers["content-type"] = "application/json";
    payload = JSON.stringify(body);
  }
  const res = await fetch(`${API}${path}`, { method, headers, body: payload, redirect: "manual" });
  const text = await res.text();
  let json = null;
  try {
    json = JSON.parse(text);
  } catch {
    /* non-JSON */
  }
  return { status: res.status, json, text, headers: res.headers };
}

const b64url = (buf) => buf.toString("base64url");
const pkce = () => {
  const verifier = b64url(randomBytes(32));
  return { verifier, challenge: b64url(createHash("sha256").update(verifier).digest()) };
};

async function register(label) {
  const email = `clamp-${label}-${RUN}@example.test`;
  const r = await call("POST", "/api/v1/auth/register", { body: { email, password: PASSWORD, name: `Clamp ${label}` } });
  if (r.status !== 201 && r.status !== 200) throw new Error(`register ${label}: ${r.status} ${r.text}`);
  return { email, token: r.json.tokens.access_token, userId: r.json.user?.id ?? r.json.user_id };
}

/** Runs the whole OAuth flow for `user` into workspace `wsId`; returns the mot_ token. */
async function connect(user, wsId, label) {
  const reg = await call("POST", "/oauth/register", {
    body: {
      client_name: `clamp-${label}-${RUN}`,
      redirect_uris: [REDIRECT],
      token_endpoint_auth_method: "none",
      grant_types: ["authorization_code", "refresh_token"],
    },
  });
  if (reg.status !== 201) throw new Error(`DCR ${label}: ${reg.status} ${reg.text}`);
  const clientId = reg.json.client_id;
  const { verifier, challenge } = pkce();
  const decision = {
    client_id: clientId,
    redirect_uri: REDIRECT,
    response_type: "code",
    code_challenge: challenge,
    code_challenge_method: "S256",
    scope: "mesh offline_access",
    state: `s-${label}`,
    workspace_id: wsId,
    allow: true,
  };
  const dec = await call("POST", "/api/v1/oauth/consent", { token: user.token, body: decision });
  if (dec.status !== 200) throw new Error(`consent ${label}: ${dec.status} ${dec.text}`);
  const code = new URL(dec.json.redirect_uri).searchParams.get("code");
  const tok = await call("POST", "/oauth/token", {
    form: { grant_type: "authorization_code", client_id: clientId, redirect_uri: REDIRECT, code, code_verifier: verifier },
  });
  if (tok.status !== 200 || !tok.json.access_token?.startsWith("mot_")) throw new Error(`token ${label}: ${tok.status} ${tok.text}`);
  return tok.json.access_token;
}

const isRoleDenied = (r) => r.status === 403 && /role|permission/i.test(r.text);
/** Not stopped by the role clamp: anything but 401/403. (A downstream 4xx/5xx is fine — it means the request got PAST the guard.) */
const passedGuard = (r) => r.status !== 401 && r.status !== 403;

async function main() {
  // ---- fixture: owner + member + a project, a task, an X-Agent-Key agent --------
  const owner = await register("owner");
  const workspaces = await call("GET", "/api/v1/workspaces", { token: owner.token });
  const wsId = (Array.isArray(workspaces.json) ? workspaces.json : workspaces.json.items ?? workspaces.json.workspaces)[0].id;

  const member = await register("member");
  const addMember = await call("POST", `/api/v1/workspaces/${wsId}/members`, {
    token: owner.token,
    body: { email: member.email, role: "member" },
  });
  if (addMember.status >= 300) throw new Error(`add member: ${addMember.status} ${addMember.text}`);
  const memberUserId = addMember.json.user_id ?? addMember.json.user?.id ?? member.userId;

  const proj = await call("POST", `/api/v1/workspaces/${wsId}/projects`, {
    token: owner.token,
    body: { name: `Clamp ${RUN}`, slug: `clamp-${RUN}` },
  });
  if (proj.status >= 300) throw new Error(`create project: ${proj.status} ${proj.text}`);
  const projId = proj.json.id;

  const statuses = await call("GET", `/api/v1/projects/${projId}/statuses`, { token: owner.token });
  const statusList = Array.isArray(statuses.json) ? statuses.json : statuses.json.items ?? statuses.json.statuses;
  const task = await call("POST", `/api/v1/projects/${projId}/tasks`, {
    token: owner.token,
    body: { title: `Clamp task ${RUN}`, status_id: statusList[0].id },
  });
  if (task.status >= 300) throw new Error(`create task: ${task.status} ${task.text}`);
  const taskId = task.json.id;

  const agent = await call("POST", `/api/v1/workspaces/${wsId}/agents`, {
    token: owner.token,
    body: { name: `clamp-agk-${RUN}`, agent_type: "custom" },
  });
  if (agent.status >= 300) throw new Error(`register agent: ${agent.status} ${agent.text}`);
  const agentKey = agent.json.api_key ?? agent.json.key;
  const agentId = agent.json.agent?.id ?? agent.json.id;

  // The human member must be a project member to reach projAccess routes.
  const addHuman = await call("POST", `/api/v1/projects/${projId}/members`, { token: owner.token, body: { user_id: memberUserId, role: "member" } });
  if (addHuman.status >= 300) throw new Error(`add human project member: ${addHuman.status} ${addHuman.text}`);
  const addAgk = await call("POST", `/api/v1/projects/${projId}/members/agents`, { token: owner.token, body: { agent_id: agentId, role: "member" } });
  if (addAgk.status >= 300) throw new Error(`add agent project member: ${addAgk.status} ${addAgk.text}`);

  // ---- connectors ---------------------------------------------------------------
  const ownerMot = await connect(owner, wsId, "owner");
  const memberMot = await connect(member, wsId, "member");
  for (const [label, mot] of [["owner", ownerMot], ["member", memberMot]]) {
    const me = await call("GET", "/api/v1/agents/me", { token: mot });
    check(`${label} connector authenticates (GET /agents/me)`, me.status === 200, `status=${me.status}`);
    const add = await call("POST", `/api/v1/projects/${projId}/members/agents`, { token: owner.token, body: { agent_id: me.json.id, role: "member" } });
    if (add.status >= 300) throw new Error(`add ${label} connector to project: ${add.status} ${add.text}`);
  }

  // ---- 1. the card's acceptance case: member connector cannot PATCH /projects/:id -
  const patchBody = { description: `edited-by-connector-${RUN}` };
  const memberPatch = await call("PATCH", `/api/v1/projects/${projId}`, { token: memberMot, body: patchBody });
  check("member connector: PATCH /projects/:id → 403 naming the role", isRoleDenied(memberPatch), `status=${memberPatch.status}`);
  const afterDenied = await call("GET", `/api/v1/projects/${projId}`, { token: owner.token });
  check("member connector: the denied PATCH changed nothing (description untouched)", afterDenied.json?.description !== patchBody.description, `description=${JSON.stringify(afterDenied.json?.description)}`);
  const ownerPatch = await call("PATCH", `/api/v1/projects/${projId}`, { token: ownerMot, body: patchBody });
  check("control — admin/owner connector: PATCH /projects/:id → 200", ownerPatch.status === 200, `status=${ownerPatch.status}`);
  const afterAllowed = await call("GET", `/api/v1/projects/${projId}`, { token: owner.token });
  check("control — the allowed PATCH really changed the project", afterAllowed.json?.description === patchBody.description, `description=${JSON.stringify(afterAllowed.json?.description)}`);

  // ---- 2. the other project-structure routes ------------------------------------
  const stBody = { name: `Clamp status ${RUN}`, slug: `clamp-status-${RUN}`, category: "todo", color: "#888888" };
  const memberStatus = await call("POST", `/api/v1/projects/${projId}/statuses`, { token: memberMot, body: stBody });
  check("member connector: POST /projects/:id/statuses → 403 naming the role", isRoleDenied(memberStatus), `status=${memberStatus.status}`);
  const ownerStatus = await call("POST", `/api/v1/projects/${projId}/statuses`, { token: ownerMot, body: stBody });
  check("control — owner connector: POST /projects/:id/statuses → 2xx", ownerStatus.status >= 200 && ownerStatus.status < 300, `status=${ownerStatus.status}`);

  // ---- 3. memory: writable by a member (that is what the MCP server does), index rebuild is not
  const memBody = { key: `clamp-mem-${RUN}`, content: "written through a connector", scope: "workspace" };
  const memberMem = await call("POST", "/api/v1/memories", { token: memberMot, body: memBody });
  check("member connector: POST /memories still works (MCP remember)", memberMem.status >= 200 && memberMem.status < 300, `status=${memberMem.status}`);
  const memberReindex = await call("POST", "/api/v1/memories/reindex", { token: memberMot, body: {} });
  check("member connector: POST /memories/reindex → 403 naming the role", isRoleDenied(memberReindex), `status=${memberReindex.status}`);
  const ownerReindex = await call("POST", "/api/v1/memories/reindex", { token: ownerMot, body: {} });
  check("control — owner connector: POST /memories/reindex passes the guard", passedGuard(ownerReindex), `status=${ownerReindex.status}`);

  // ---- 4. member work that must keep working: checkout, project knowledge --------
  const memberCheckout = await call("POST", `/api/v1/tasks/${taskId}/checkout`, { token: memberMot, body: {} });
  check("member connector: POST /tasks/:id/checkout still works", memberCheckout.status >= 200 && memberCheckout.status < 300, `status=${memberCheckout.status}`);
  const memberKnowledge = await call("POST", `/api/v1/projects/${projId}/knowledge`, {
    token: memberMot,
    body: { key: `clamp-know-${RUN}`, value: "project knowledge from a connector" },
  });
  check("member connector: POST /projects/:id/knowledge still works (2xx)", memberKnowledge.status >= 200 && memberKnowledge.status < 300, `status=${memberKnowledge.status}`);

  // ---- 5. regression: humans and X-Agent-Key agents are exactly as before --------
  const agkPatch = await call("PATCH", `/api/v1/projects/${projId}`, { agentKey, body: { description: `agk-${RUN}` } });
  check("regression — X-Agent-Key agent: PATCH /projects/:id → 200 (unchanged)", agkPatch.status === 200, `status=${agkPatch.status}`);
  const agkStatus = await call("POST", `/api/v1/projects/${projId}/statuses`, {
    agentKey,
    body: { name: `Agk status ${RUN}`, slug: `agk-status-${RUN}`, category: "todo", color: "#777777" },
  });
  check("regression — X-Agent-Key agent: POST /projects/:id/statuses → 2xx (unchanged)", agkStatus.status >= 200 && agkStatus.status < 300, `status=${agkStatus.status}`);
  const agkReindex = await call("POST", "/api/v1/memories/reindex", { agentKey, body: {} });
  check("regression — X-Agent-Key agent: POST /memories/reindex passes the guard (unchanged)", passedGuard(agkReindex), `status=${agkReindex.status}`);
  const humanPatch = await call("PATCH", `/api/v1/projects/${projId}`, { token: member.token, body: { description: `human-${RUN}` } });
  check("regression — human member: PATCH /projects/:id → 200 (unchanged)", humanPatch.status === 200, `status=${humanPatch.status}`);

  // ---- 5b. a connector may not name a URL the server will call ---------------------
  const cbBody = { callback_url: "http://127.0.0.1:1/connector-chose-this" };
  const memberCb = await call("PATCH", "/api/v1/agents/me", { token: memberMot, body: cbBody });
  check("member connector: PATCH /agents/me with a callback_url → 403", memberCb.status === 403 && /callback/i.test(memberCb.text), `status=${memberCb.status}`);
  const memberMeAfter = await call("GET", "/api/v1/agents/me", { token: memberMot });
  check("member connector: the refused callback_url was not stored", !memberMeAfter.json?.callback_url, `callback_url=${JSON.stringify(memberMeAfter.json?.callback_url)}`);
  const memberDesc = await call("PATCH", "/api/v1/agents/me", { token: memberMot, body: { profile_description: `connector-${RUN}` } });
  check("control — member connector: PATCH /agents/me with a description → 200", memberDesc.status === 200, `status=${memberDesc.status}`);
  const agkCb = await call("PATCH", "/api/v1/agents/me", { agentKey, body: cbBody });
  check("regression — X-Agent-Key agent: PATCH /agents/me with a callback_url → 200 (unchanged)", agkCb.status === 200, `status=${agkCb.status}`);

  // ---- 6. the user's role drops: the SAME connector token loses the write on the next request
  const demote = await call("PATCH", `/api/v1/workspaces/${wsId}/members/${memberUserId}`, { token: owner.token, body: { role: "viewer" } });
  check("owner demotes the member to viewer", demote.status >= 200 && demote.status < 300, `status=${demote.status}`);
  const viewerMem = await call("POST", "/api/v1/memories", { token: memberMot, body: { ...memBody, key: `clamp-mem2-${RUN}` } });
  check("demoted user's connector: POST /memories → 403 naming the role (same token, next request)", isRoleDenied(viewerMem), `status=${viewerMem.status}`);
  const viewerCheckout = await call("DELETE", `/api/v1/tasks/${taskId}/checkout`, { token: memberMot });
  check("demoted user's connector: DELETE /tasks/:id/checkout → 403 naming the role", isRoleDenied(viewerCheckout), `status=${viewerCheckout.status}`);
  const viewerRead = await call("GET", `/api/v1/tasks/${taskId}`, { token: memberMot });
  check("control — demoted user's connector can still READ the task (reads are not clamped)", viewerRead.status === 200, `status=${viewerRead.status}`);
}

main()
  .catch((err) => {
    console.log(`FAIL  run aborted: ${err.message}`);
    results.push({ name: "run completed", ok: false });
  })
  .finally(() => {
    const failed = results.filter((r) => !r.ok);
    console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
    process.exit(failed.length === 0 ? 0 : 1);
  });
