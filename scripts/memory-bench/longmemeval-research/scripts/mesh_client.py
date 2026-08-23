"""Mesh-recall client (STDIO transport) for the LongMemEval memory benchmark.

Drop-in replacement for metronix-memory's `MetronixMCPClient`: same
`ingest_and_search(sessions, dates, format_session_text, query, top_k)`
contract, but drives OUR memory (Mesh `remember`/`recall` MCP tools) over the
**stdio** transport (mesh-mcp is a stdio server, not HTTP).

Per-question isolation: every store is tagged `bench-<question_id>` (plus a
shared `lme-bench` umbrella tag for cleanup) and every recall is filtered to the
per-question tag via `tags_any`, so question N's memories never leak into
question M -- mirrors metronix's fresh-agent-per-question isolation.

Temporal-reasoning fix (Option B, 2026-07-08):
  After storing the 48 regular sessions, we also store one compact
  [Temporal Session Index] memory that lists EVERY session in chronological
  order with its conversation date + first/last-user-message snippets.
  This gives the LLM a complete date map so it can locate events and compute
  exact elapsed time even when the relevant session has low FTS relevance.
  For queries that look temporal (keyword-matched), the index is returned first
  followed by all sessions in date order (not just the FTS top-k).

The mesh-mcp binary + its MESH_* env are read from os.environ (populated from
.env.benchmark by env_config.load_dotenv). No secrets are hard-coded here.
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import re
import unicodedata
from typing import Any

logger = logging.getLogger(__name__)

# mesh-mcp stdio launch (matches ~/ClaudeCowork/bob/.mcp.json evc-mesh entry,
# minus the mcp-wrap stderr-logging shim which we don't need here).
MESH_MCP_COMMAND = os.path.expanduser("~/bin/mesh-mcp")
MESH_MCP_ARGS = ["--transport", "stdio"]

STORE_SCOPE = "workspace"           # workspace scope + per-question tag = isolation
SHARED_TAG = "lme-bench"            # umbrella tag on every store, for cleanup
TEMPORAL_INDEX_TAG = "temporal-index"  # marks the compact chronological index
INIT_TIMEOUT = 60.0
STORE_CONCURRENCY = 1               # sequential — reliable under #304-rebuilt mesh-mcp

# Which workspace the fixtures land in is decided by MESH_AGENT_KEY, NOT by
# STORE_SCOPE: the server parses `agk_{workspace-slug}_{random}` and resolves the
# workspace from that slug (agent_service.Authenticate). Every memory read path
# — FTS, the OR-fallback, VectorSearch, graph recall and ListWithNullEmbedding —
# hard-filters `workspace_id = $1`, so a key belonging to a throwaway workspace
# makes bench fixtures structurally invisible to prod recall and to every count
# over the prod workspace.
#
# Until 2026-07-13 this key was the PROD key (slug ws-006901c7): every fixture was
# written into the live Entire VC workspace at scope=workspace, i.e. surfaceable by
# any agent's wake-up recall() mid-run, and counted in the embedding-debt figure.
# The guard below is POSITIVE (assert the expected bench slug) rather than a
# prod denylist — a denylist silently fails open against any future prod slug.
BENCH_WORKSPACE_SLUG = os.environ.get("BENCH_WORKSPACE_SLUG", "lme-bench")


def _key_workspace_slug(api_key: str) -> str:
    """Slug the SERVER will resolve this key to — mirrors parseWorkspaceSlugFromKey:
    strip `agk_`, then split at the LAST underscore (slugs may contain hyphens)."""
    if not api_key.startswith("agk_"):
        raise RuntimeError("MESH_AGENT_KEY is not an agent key (no `agk_` prefix)")
    rest = api_key[4:]
    idx = rest.rfind("_")
    if idx <= 0:
        raise RuntimeError("MESH_AGENT_KEY is malformed (no slug separator)")
    return rest[:idx]


def assert_bench_workspace() -> str:
    """Fail CLOSED before a single fixture is written if the key points anywhere
    other than the dedicated bench workspace. Read at call time, never at import
    (an env-derived module constant binds before .env.benchmark is loaded)."""
    key = os.environ.get("MESH_AGENT_KEY", "")
    if not key:
        raise RuntimeError("MESH_AGENT_KEY is unset — refusing to run the bench")
    slug = _key_workspace_slug(key)
    if slug != BENCH_WORKSPACE_SLUG:
        raise RuntimeError(
            f"REFUSING TO RUN: MESH_AGENT_KEY resolves to workspace '{slug}', not the "
            f"dedicated bench workspace '{BENCH_WORKSPACE_SLUG}'. The bench writes "
            f"~49 fixtures per question at scope=workspace; against a live workspace "
            f"they are visible to every agent's recall() and pollute every count over "
            f"the memories table. Point MESH_AGENT_KEY at the bench workspace key "
            f"(or set BENCH_WORKSPACE_SLUG if you really mean to bench elsewhere)."
        )
    return slug

# Server-side filter verified working 2026-07-04 (was using wrong param `tags`).
RECALL_CANDIDATE_LIMIT = 50         # server max — retrieve ALL bench sessions
RECALL_ORDER_BY = "relevance:desc"  # FTS order; temporal selection done client-side

# Every stored memory's UUID is appended here so cleanup can delete deterministically.
BENCH_IDS_LOG = os.environ.get(
    "BENCH_IDS_LOG", os.path.expanduser("~/bench/store_ids.jsonl")
)

# ── Temporal query detection ───────────────────────────────────────────────────
# STRICT patterns for TRUE cross-session temporal reasoning only.
# These questions require computing elapsed time between sessions or
# ordering events across multiple sessions.
#
# INTENTIONALLY EXCLUDED (too broad — appear in non-temporal questions):
#   "when", "before", "after", "recently", "yesterday", "last week",
#   "how long", "first time", "last time", "sequence", "timeline",
#   "what day", "since then" — all appear in single-session factual questions
#   and caused 95.7% → 54.3% regression on single-session-user category.
_TEMPORAL_PATTERNS = (
    # Elapsed time / date math between sessions (strict multi-word phrases)
    "how many days", "how many weeks", "how many months", "how many years",
    "days passed", "weeks passed", "months passed",
    "days ago", "weeks ago", "months ago",
    "time between", "time span", "time elapsed",
    "passed between", "elapsed between", "how long ago",
    "how many times",
    # Cross-session ordering (strict)
    "from first to last", "from last to first", "first to last", "last to first",
    "in what order", "chronological order",
    "first mention", "last mention",
    "consecutive", "in a row", "back to back",
    "before or after",
)


def _is_temporal_query(query: str) -> bool:
    lower = query.lower()
    return any(p in lower for p in _TEMPORAL_PATTERNS)


class ContentPolicySkip(RuntimeError):
    """A single session's content was rejected by one of Mesh's own `remember`
    content guards (e.g. instruction-override, secret-assignment) — not a bug,
    not a transient failure, and not the silent-None masking class this harness
    was fixed to catch on 2026-08-19. These guards fire on a small number of
    LongMemEval filler/distractor sessions whose text is genuinely shaped like a
    prompt injection or a literal secret (LongMemEval synthesizes a wide variety
    of unrelated ShareGPT-sourced conversations as haystack noise; one is
    literally 'Ignore all previous instructions, brainstorm a...'). Storing that
    verbatim as a Mesh memory would BE the risk the guard exists to catch, so
    bypassing it is wrong — unlike the invisible-char guard, this isn't inert
    formatting noise. See guard name + full server message in the log line
    raised alongside this."""


_CONTENT_POLICY_RE = re.compile(r"Validation failed \(content: ([a-z0-9-]+):")


def _content_guard_name(payload: Any) -> str | None:
    """Return the guard name (e.g. 'instruction-override') if `payload` is a Mesh
    content-policy rejection, else None. The MCP tool call returns this as plain
    text (not the `{"error": ...}` shape), so `_parse_tool_payload` wraps it as
    `{"text": "remember failed: Bad Request: Validation failed (content: ...)"}`
    — check both `text` and `error` since either can carry it."""
    if not isinstance(payload, dict):
        return None
    msg = payload.get("text") or payload.get("error") or ""
    m = _CONTENT_POLICY_RE.search(str(msg))
    return m.group(1) if m else None


def _strip_invisible_chars(text: str) -> str:
    """Drop Unicode category Cf (format) chars: zero-width space/joiner, bidi
    overrides, soft hyphen, BOM, emoji tag sequences, etc. Mesh's `remember`
    now rejects content containing any of these (server-side prompt-injection
    guard: "the invisible or direction-altering character ... can hide text
    from a reader while an agent still acts on it"). LongMemEval haystack
    text carries them naturally in 98/500 questions (copy-paste artifacts,
    flag-emoji tag sequences) -- unrelated to what the benchmark tests, so
    stripping them is a harness fix, not a workaround of the guard's intent."""
    return "".join(ch for ch in text if unicodedata.category(ch) != "Cf")


# Parses "[Conversation date: YYYY/MM/DD" from the start of stored session content.
_DATE_RE = re.compile(r"\[Conversation date: (\d{4}/\d{2}/\d{2})")


def _parse_session_date(content: str) -> str:
    """Return YYYY/MM/DD extracted from [Conversation date: ...] header, else ''."""
    m = _DATE_RE.match(content)
    return m.group(1) if m else ""


def _build_temporal_index(sessions: list, dates: list) -> str:
    """Compact chronological index of all sessions for temporal date math.

    For each session records:
      - The conversation date
      - Session index (matches stored key suffix)
      - First-user-message snippet (captures opening topic)
      - Last-user-message snippet (often contains the key closing event:
        'I attended the X exhibit at Y museum today')

    Stored as the bench-{qid}-temporal-index memory and returned FIRST for
    temporal queries so the LLM can find event dates without reading 48 full
    sessions.
    """
    entries: list[tuple[str, int, str]] = []
    for idx, (turns, date) in enumerate(zip(sessions, dates)):
        user_turns = [t for t in turns if t.get("role") == "user"]
        first_snip = (
            user_turns[0]["content"][:200].replace("\n", " ") if user_turns else ""
        )
        last_snip = (
            user_turns[-1]["content"][:250].replace("\n", " ") if user_turns else ""
        )
        if not last_snip or first_snip == last_snip:
            snippet = first_snip[:200]
        else:
            snippet = f"[start] {first_snip[:150]} ... [end] {last_snip[:200]}"
        entries.append((date, idx, snippet))

    # Sort chronologically so the LLM can scan dates in order.
    entries.sort(key=lambda x: x[0])

    lines = [
        f"[Temporal Session Index — {len(sessions)} sessions in chronological order]",
        "Scan this index to locate events and their dates. Each line is one session.",
        "Format: DATE (session-N): topic_snippet",
        "",
    ]
    for date, idx, snippet in entries:
        lines.append(f"{date} (session-{idx}): {snippet}")

    return "\n".join(lines)


# ── Helpers ───────────────────────────────────────────────────────────────────

def _log_store_id(memory_id: str, qid: str, key: str) -> None:
    if not memory_id:
        return
    try:
        with open(BENCH_IDS_LOG, "a", encoding="utf-8") as fh:
            fh.write(json.dumps({"id": memory_id, "qid": qid, "key": key}) + "\n")
    except OSError:
        pass


def _mesh_env() -> dict[str, str]:
    """Full child env: inherit PATH etc. + the MESH_* keys the server needs."""
    env = dict(os.environ)
    for key in ("MESH_API_URL", "MESH_AGENT_KEY", "RECALL_GRAPH_ENABLED"):
        val = os.environ.get(key)
        if val is not None:
            env[key] = val
    return env


def _parse_tool_payload(result: Any) -> dict[str, Any]:
    if result is None:
        return {}
    if isinstance(result, dict):
        return result
    content = getattr(result, "content", None)
    if not content:
        return {}
    for block in content:
        text = getattr(block, "text", None)
        if not text:
            continue
        try:
            parsed = json.loads(text)
            if isinstance(parsed, list):
                return {"items": parsed}
            if isinstance(parsed, dict):
                return parsed
        except json.JSONDecodeError:
            return {"text": text}
    return {}


def _coerce_score(value: Any) -> float | None:
    try:
        return float(value) if value is not None else None
    except (TypeError, ValueError):
        return None


def _to_record(item: dict[str, Any]) -> dict[str, Any]:
    """Map a Mesh recall item → {record:{content}, score} for the harness."""
    content = (
        item.get("content")
        or (item.get("record") or {}).get("content")
        or item.get("text")
        or ""
    )
    score = item.get("score")
    if score is None:
        score = item.get("relevance", item.get("decayed_relevance"))
    return {"record": {"content": content}, "score": _coerce_score(score)}


# ── Main client ───────────────────────────────────────────────────────────────

class MeshMemoryClient:
    """MCP client over Mesh remember/recall via a fresh stdio server per question."""

    def __init__(
        self,
        *,
        question_id: str,
        mcp_url: str | None = None,   # ignored (stdio); kept for call-site compat
        api_key: str | None = None,   # ignored (env-driven)
        workspace_id: str | None = None,
        agent_id: str | None = None,
        **_ignore: Any,
    ) -> None:
        self.qid = question_id
        # The tag doubles as the memory KEY prefix, and Mesh validates keys against
        # ^[a-z0-9][a-z0-9-]*[a-z0-9]$ — no underscores. 132 of the 500 question ids
        # carry one (`gpt4_f49edff3`), including 91 of the 133 temporal-reasoning
        # questions, so every `remember` for those 400'd and the question was answered
        # from an EMPTY context. That is the real source of the temporal-reasoning
        # plateau; three mechanism attempts (write-time index, wider patterns, no
        # top_k truncation) all moved a number that recall never got to influence.
        # Verified no collisions: 500 ids → 500 distinct sanitized tags.
        self.bench_tag = "bench-" + re.sub(r"[^a-z0-9-]+", "-", question_id.lower()).strip("-")

    def ingest_and_search(
        self,
        *,
        sessions: list[list[dict]],
        dates: list[str],
        format_session_text,
        query: str,
        top_k: int,
    ) -> list[dict[str, Any]]:
        # Fail closed BEFORE the first write if the key points at a live workspace.
        assert_bench_workspace()
        return asyncio.run(
            self._run(sessions, dates, format_session_text, query, top_k)
        )

    async def _run(self, sessions, dates, format_session_text, query, top_k):
        from mcp import ClientSession, StdioServerParameters
        from mcp.client.stdio import stdio_client

        params = StdioServerParameters(
            command=MESH_MCP_COMMAND,
            args=MESH_MCP_ARGS,
            env=_mesh_env(),
        )
        async with stdio_client(params) as (read_stream, write_stream):
            async with ClientSession(read_stream, write_stream) as session:
                await asyncio.wait_for(session.initialize(), timeout=INIT_TIMEOUT)
                sem = asyncio.Semaphore(STORE_CONCURRENCY)
                self._ids: list[str] = []
                self._skipped_sessions: list[tuple[int, str]] = []

                async def _one(idx, turns, date):
                    async with sem:
                        try:
                            mid = await self._store(
                                session, format_session_text(turns, date=date), idx, date
                            )
                        except ContentPolicySkip as exc:
                            # One filler session rejected by a Mesh content guard —
                            # drop it, don't abort the question. Recorded (not silently
                            # swallowed) so corruption from THIS cause stays visible
                            # and distinguishable from a genuine empty-store bug.
                            logger.warning(
                                "skipping %s-s%d (qid=%s): content-policy rejection: %s",
                                self.bench_tag, idx, self.qid, exc,
                            )
                            self._skipped_sessions.append((idx, str(exc)))
                            return
                        if mid:
                            self._ids.append(mid)

                # Cleanup is a `finally`, not a trailing statement: a raising _store
                # (e.g. `remember failed:` on a 5xx) or a raising _search used to skip
                # the deletes entirely and strand every fixture already written. That is
                # not hypothetical — the 2026-07-13 11:20 run left 80 rows alive in prod.
                try:
                    # Store regular sessions sequentially (STORE_CONCURRENCY=1)
                    await asyncio.gather(*[
                        _one(idx, turns, date)
                        for idx, (turns, date) in enumerate(
                            zip(sessions, dates, strict=False)
                        )
                    ])

                    # Store compact temporal index — written AFTER sessions so it's
                    # available for the recall call below. Also tagged with bench_tag
                    # so it's cleaned up with everything else.
                    temporal_content = _build_temporal_index(sessions, dates)
                    tid = await self._store_temporal_index(session, temporal_content)
                    if tid:
                        self._ids.append(tid)

                    results = await self._search(session, query, top_k)
                finally:
                    await self._forget_all(session, sem)

                return results

    async def _forget_all(self, session, sem):
        """Delete every fixture this question wrote. Runs on the success path AND on
        any exception. Best-effort per row (a failed delete must not mask the original
        error), but each id is logged so a leak is visible instead of silent."""
        async def _del(mid):
            async with sem:
                try:
                    await session.call_tool("forget", {"memory_id": mid})
                    return None
                except Exception as exc:  # noqa: BLE001 — never mask the real error
                    logger.warning("bench cleanup: forget(%s) failed: %s", mid, exc)
                    return mid

        stranded = [m for m in await asyncio.gather(*[_del(m) for m in self._ids]) if m]
        if stranded:
            logger.error(
                "bench cleanup: %d/%d fixtures STRANDED in the bench workspace (ids: %s)",
                len(stranded), len(self._ids), ", ".join(stranded[:5]),
            )
        self._ids = []

    async def _store(self, session, content, idx, date):
        content = _strip_invisible_chars(content)
        result = await session.call_tool(
            "remember",
            {
                "key": f"{self.bench_tag}-s{idx}",
                "content": content,
                "scope": STORE_SCOPE,
                "tags": [self.bench_tag, SHARED_TAG, f"session-{idx}"],
            },
        )
        payload = _parse_tool_payload(result)
        guard = _content_guard_name(payload)
        if isinstance(payload, dict) and payload.get("error"):
            # Log HERE, not just raise: the real message does not survive the
            # ClientSession/stdio_client __aexit__ unwind (anyio TaskGroup collapses
            # it into "unhandled errors in a TaskGroup (1 sub-exception)" with no
            # detail — same defect class as #4045f449/#dea7a367). This is the only
            # place the actual payload["error"] is still known.
            logger.error(
                "remember failed for %s-s%d (qid=%s): %s",
                self.bench_tag, idx, self.qid, payload["error"],
            )
            if guard:
                raise ContentPolicySkip(f"{guard}: {payload['error']}")
            raise RuntimeError(f"remember failed: {payload['error']}")
        mem = payload.get("memory") if isinstance(payload, dict) else None
        if isinstance(mem, dict) and mem.get("id"):
            _log_store_id(mem.get("id", ""), self.qid, mem.get("key", ""))
            return mem.get("id")
        # Fail LOUD. Returning None here is what hid the invalid-key 400 for 132 of the
        # 500 questions: the caller only did `if mid: append`, the run continued, and the
        # question was scored against an empty memory store — indistinguishable in the
        # results file from "recall retrieved nothing useful". A store that did not store
        # is not a degraded run, it is an invalid one.
        #
        # EXCEPTION, added 2026-08-21: a Mesh content-policy guard (instruction-override,
        # secret-assignment, ...) rejecting ONE filler session is not that failure class —
        # it's a legitimate per-session rejection of content that is genuinely shaped like
        # what the guard exists to catch (LongMemEval haystack noise sourced from ShareGPT
        # includes sessions literally starting "Ignore all previous instructions...").
        # Raise ContentPolicySkip so the caller can drop just this session instead of
        # aborting the whole question — see ContentPolicySkip's docstring.
        logger.error(
            "remember returned no memory id for %s-s%d (qid=%s); payload=%s",
            self.bench_tag, idx, self.qid, str(payload)[:300],
        )
        if guard:
            raise ContentPolicySkip(f"{guard}: {str(payload)[:300]}")
        raise RuntimeError(
            f"remember returned no memory id for {self.bench_tag}-s{idx}; payload={str(payload)[:300]}"
        )

    async def _store_temporal_index(self, session, content: str) -> str | None:
        """Store the compact chronological session index for temporal reasoning."""
        content = _strip_invisible_chars(content)
        result = await session.call_tool(
            "remember",
            {
                "key": f"{self.bench_tag}-temporal-index",
                "content": content,
                "scope": STORE_SCOPE,
                "tags": [self.bench_tag, SHARED_TAG, TEMPORAL_INDEX_TAG],
            },
        )
        payload = _parse_tool_payload(result)
        if isinstance(payload, dict) and payload.get("error"):
            logger.warning("temporal index store failed: %s", payload["error"])
            return None
        mem = payload.get("memory") if isinstance(payload, dict) else None
        if isinstance(mem, dict):
            _log_store_id(mem.get("id", ""), self.qid, mem.get("key", ""))
            return mem.get("id")
        return None

    def _diag(self, query, items, mine, selected):
        """Opt-in retrieval diagnostic (set LME_DIAG_FILE).

        Answers the one question the results file cannot: when a temporal answer is
        wrong, was the needed session even IN the context? Without it, "temporal is
        stuck at 30%" is compatible with two opposite causes — recall never handed the
        session over, or it did and the answering model could not reason over it — and
        the fix for one is worthless against the other.
        """
        path = os.getenv("LME_DIAG_FILE")
        if not path:
            return
        tags = lambda rec: [t for t in (rec.get("tags") or []) if t.startswith("session-")]
        try:
            with open(path, "a") as fh:
                fh.write(json.dumps({
                    "qid": self.qid,
                    "query": query[:120],
                    "temporal_path": _is_temporal_query(query),
                    "n_items_returned": len(items),   # what the server sent back
                    "n_mine": len(mine),              # after this question's tag filter
                    "n_selected": len(selected),      # what the LLM actually got
                    "sessions_selected": sorted(
                        int(t.split("-")[1]) for rec in selected for t in tags(rec)
                        if t.split("-")[1].isdigit()
                    ),
                }) + "\n")
        except Exception as e:                        # diagnostics never break a run
            log.warning("diag write failed: %s", e)

    async def _search(self, session, query, top_k):
        args = {
            "query": query,
            "tags_any": [self.bench_tag],      # server-side filter (verified 2026-07-04)
            "scope": STORE_SCOPE,
            "limit": RECALL_CANDIDATE_LIMIT,
            "order_by": RECALL_ORDER_BY,
            "min_importance": 0,
        }
        result = await session.call_tool("recall", args)
        payload = _parse_tool_payload(result)
        if isinstance(payload, dict) and payload.get("error"):
            logger.error("recall failed (qid=%s): %s", self.qid, payload["error"])
            raise RuntimeError(f"recall failed: {payload['error']}")
        items = payload.get("items") or payload.get("results") or []
        if not isinstance(items, list):
            items = []
        # CLIENT-SIDE isolation: keep only this question's stores.
        mine = [it for it in items if self.bench_tag in (it.get("tags") or [])]

        if not mine and os.getenv("LME_DIAG_FILE"):
            # Empty recall on a question whose 48 sessions were just written is the
            # single most important thing to explain, and the results file cannot:
            # it stores only the hypothesis. Re-ask the SAME call with a progressively
            # weaker query to separate "the query text matched nothing" from "the
            # filter/scope/store is wrong" — those have opposite fixes.
            for label, probe_q in (
                ("top5-longest-words", " ".join(sorted(re.findall(r"[A-Za-z]{4,}", query),
                                                       key=len, reverse=True)[:5])),
                ("single-common-word", "the"),
            ):
                try:
                    p = _parse_tool_payload(await session.call_tool("recall", {**args, "query": probe_q}))
                    it = p.get("items") or p.get("results") or []
                    n_mine = len([x for x in it if self.bench_tag in (x.get("tags") or [])])
                except Exception as e:
                    it, n_mine, probe_q = [], -1, f"{probe_q} (error: {e})"
                with open(os.environ["LME_DIAG_FILE"], "a") as fh:
                    fh.write(json.dumps({"qid": self.qid, "empty_probe": label,
                                         "probe_query": probe_q[:80],
                                         "returned": len(it), "mine": n_mine}) + "\n")

        if _is_temporal_query(query):
            selected = self._select_temporal_items(mine, top_k)
            self._diag(query, items, mine, selected)
            return [_to_record(it) for it in selected]

        # The temporal index is stored for EVERY question (see _run), tagged with
        # the same bench_tag as the regular sessions, so it lands in `mine` and
        # competes for a top_k slot on the non-temporal path too. It is a compact
        # topic-snippet summary, not session content -- occupying a slot here
        # displaces an actual answer-bearing session and never itself contains
        # the answer. Confirmed root cause of the 2026-08-20 knowledge-update
        # regression (91.03%->61.54%): the temporal-index code path
        # (_select_temporal_items) was untouched by that regression -- 24/25
        # flipped questions went through this exact branch.
        non_index = [it for it in mine if TEMPORAL_INDEX_TAG not in (it.get("tags") or [])]
        selected = non_index[:top_k]
        self._diag(query, items, mine, selected)
        return [_to_record(it) for it in selected]

    def _select_temporal(self, mine: list[dict], top_k: int) -> list[dict]:
        """Temporal-aware selection: temporal index + ALL sessions (no FTS truncation).

        Root cause of the 30.8% plateau (confirmed 2026-07-09): the temporal index
        tells the LLM WHEN things happened, but the top_k FTS-ranked slice still
        decided WHICH session content the LLM gets to read — and FTS scored against
        an abstract temporal phrasing ("how many days between X and Y") routinely
        fails to rank the one or two sessions that actually contain X and Y inside
        the top 10. Since `_search` already retrieves the full per-question set in
        one recall call (RECALL_CANDIDATE_LIMIT=50 >= 49 stores/question — no extra
        API cost), we drop the top_k cap for the temporal path entirely and hand the
        LLM every session verbatim. This only affects the temporal-classified 27% of
        queries; non-temporal categories keep the top_k-truncated path unchanged.
        """
        return [_to_record(it) for it in self._select_temporal_items(mine, top_k)]

    def _select_temporal_items(self, mine: list[dict], top_k: int) -> list[dict]:
        """Temporal index first, then the top_k FTS-ranked sessions.

        The no-truncation variant (hand the LLM all 48 sessions) was measured on
        2026-08-19 and did NOT work: temporal 25.9% vs 29.6% for the truncated path on
        the same stratified set — one question apart, i.e. noise — while overall fell to
        63.4%. It also inflated the prompt for exactly the questions it was meant to
        help, and those are the ones the generation provider then dropped with an empty
        `choices` (`'NoneType' object is not subscriptable`), which biases the sample by
        removing the hardest questions from the denominator.

        It was never going to work: the diagnostic showed the failing questions were not
        losing the right session to the top_k cut — they were getting NOTHING back,
        because their fixtures had never been stored (invalid key, see __init__).
        Returns raw items, not records, so the diagnostic can read their tags."""
        temporal_idx = [
            it for it in mine
            if TEMPORAL_INDEX_TAG in (it.get("tags") or [])
        ]
        sessions_only = [
            it for it in mine
            if TEMPORAL_INDEX_TAG not in (it.get("tags") or [])
        ]
        return temporal_idx + sessions_only[:top_k]
