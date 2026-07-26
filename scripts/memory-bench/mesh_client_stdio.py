"""Mesh-recall client (STDIO transport) for the LongMemEval memory benchmark.

Drop-in replacement for MetronixMCPClient: same
`ingest_and_search(sessions, dates, format_session_text, query, top_k)`
contract, but drives Mesh `remember`/`recall` MCP tools over the stdio
transport (mesh-mcp is a stdio server, not HTTP).

Binary path: resolved from MESH_MCP_BIN env var first, then ~/bin/mesh-mcp.
This allows CI to cross-compile and inject the binary path without modifying
the script.

Per-question isolation: every store is tagged `bench-<question_id>` (plus a
shared `lme-bench` umbrella tag for cleanup) and every recall is filtered to
the per-question tag via `tags_any`, so question N's memories never leak into
question M.

The TAG carries the question id verbatim; the memory `key` carries a sanitized
form of it (see `sanitize_key_component`). Only the key is validated by the
server, and the tag is what recall and cleanup filter on.
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import logging
import os
import re
import time
from typing import Any

logger = logging.getLogger(__name__)

# Binary path: CI cross-compiles and sets MESH_MCP_BIN to the output path.
MESH_MCP_COMMAND = os.environ.get("MESH_MCP_BIN") or os.path.expanduser("~/bin/mesh-mcp")
MESH_MCP_ARGS = ["--transport", "stdio"]

STORE_SCOPE = "workspace"
SHARED_TAG = "lme-bench"
INIT_TIMEOUT = 60.0
# Sequential stores: the mesh-mcp stdio server closes under high concurrency.
STORE_CONCURRENCY = 1

RECALL_CANDIDATE_LIMIT = 50
RECALL_ORDER_BY = "relevance:desc"

# mesh-mcp authenticates against the Mesh API at startup and EXITS if that call
# fails. The client only ever sees the downstream symptom — the stdio pipe dying
# ("Connection closed") — never the cause, which mesh-mcp prints to its own
# stderr (e.g. "Agent authentication failed: Bad Gateway: API error 502").
#
# That happens routinely and for a reason we cause ourselves: a push to main
# triggers BOTH this benchmark and the backend deploy, so the bench is running
# while mesh-api is being restarted underneath it. Every question that lands in
# the restart window errors, the error rate blows past the budget, and the gate
# reports INCONCLUSIVE — i.e. the safety net switches itself off precisely on
# the commits that changed memory. A PR run can hit the same window whenever an
# unrelated merge deploys mid-run.
#
# A restart is transient, so treat it as such: re-attempt the question against a
# fresh mesh-mcp (which re-authenticates). The circuit breaker below keeps a
# genuinely dead API from paying the backoff 24 times over.
# Four attempts spanning ~50s of backoff: comfortably longer than a systemd
# restart of mesh-api, and cheap because it is only ever paid on failure.
CONNECT_RETRIES = 4
CONNECT_BACKOFF_SECS = (5.0, 15.0, 30.0)
# Matched against the exception text. Deliberately ANCHORED rather than a bare
# substring hunt for "502": the LongMemEval fixtures contain "502"/"503"/"504" as
# incidental digits over a hundred times, and any exception that echoed content
# (or merely quoted a hex id) could then be misread as a restart — retried,
# delayed, and finally reported under a cause that was never true. A retry
# predicate that is loose in the direction of "transient" hides real bugs.
_TRANSIENT_RE = re.compile(
    r"connection closed"
    r"|bad gateway|service unavailable|gateway time-?out"
    r"|\b(?:api error|status(?: code)?|http)\s*[:=]?\s*50[234]\b"
    r"|\b50[234]\s+(?:bad gateway|service unavailable|gateway)",
    re.I,
)
# After this many questions exhaust every retry, stop retrying: the API is down,
# not restarting, and burning the backoff per question would run out the job's
# clock instead of failing fast and loudly.
BREAKER_TRIP_AFTER = 2


def _is_transient(exc: BaseException) -> bool:
    """True if `exc` (or anything nested in its ExceptionGroup) looks like the
    API being briefly unreachable rather than the harness being broken."""
    if isinstance(exc, BaseExceptionGroup):
        return any(_is_transient(sub) for sub in exc.exceptions)
    return bool(_TRANSIENT_RE.search(f"{type(exc).__name__}: {exc}"))


def flatten_exc(exc: BaseException) -> str:
    """Render an exception with its ExceptionGroup leaves inlined.

    An anyio TaskGroup surfaces failures as `ExceptionGroup: unhandled errors in
    a TaskGroup (1 sub-exception)` — a string that names the plumbing and hides
    the fault. The bench then reports that useless line as its reason for going
    blind, so the gate says "could not measure" without ever saying why.
    """
    if isinstance(exc, BaseExceptionGroup):
        leaves = "; ".join(flatten_exc(sub) for sub in exc.exceptions)
        return leaves or str(exc)
    return f"{type(exc).__name__}: {exc}" if str(exc) else type(exc).__name__

# Mesh reports which retrieval arms actually served a recall:
#   "hybrid"    — BM25 + dense/vector arm (embedder healthy)
#   "bm25-only" — the embedder failed (or is unconfigured) and Recall FAILED OPEN
# A server too old to report it yields UNKNOWN. Scores from different modes are
# NOT comparable, so the gate must know which mode it measured — otherwise a dead
# embedder reads as a code regression and wedges every PR in the repo.
SEARCH_MODE_UNKNOWN = "unknown"

BENCH_IDS_LOG = os.environ.get(
    "BENCH_IDS_LOG", os.path.expanduser("~/bench/store_ids.jsonl")
)

# Mesh validates a memory `key` against this pattern server-side and answers a
# non-conforming one with `400 Validation failed`. Nothing validates a TAG.
MESH_KEY_RE = re.compile(r"^[a-z0-9][a-z0-9-]*[a-z0-9]$")
_KEY_UNSAFE_RE = re.compile(r"[^a-z0-9-]+")
_KEY_HYPHEN_RUN_RE = re.compile(r"-{2,}")


def sanitize_key_component(raw: str) -> str:
    """Fold a question id into something Mesh will accept inside a memory `key`.

    The bench used the question id raw in both the tag and the key. 22 of the 24
    LongMemEval-S ids are bare hex and slipped through; the two `gpt4_*` ones
    carry an `_`, so their very first `remember` was rejected 400 and the
    question died before storing a single haystack session. Both are
    `temporal-reasoning`, so that 4-question category was never once measured
    above 2/4 in the 6 days the gate had been running — it did not fail loudly,
    it went quietly unmeasured, which is the failure mode this whole gate exists
    to remove.

    Sanitizing is LOSSY, and that matters here more than it usually would:
    `remember` UPSERTs on the key, so two questions that folded onto one key
    would not error — the second would silently overwrite the first's fixture and
    both would then be scored against half a haystack. So ids that are already
    key-safe are passed through UNCHANGED (keeping today's keys byte-identical,
    and the mapping trivially injective for them), and any id that had to be
    folded earns a digest of its RAW form, which restores injectivity for the
    rest. Digest is blake2s, used as a short non-cryptographic discriminator.
    """
    slug = _KEY_UNSAFE_RE.sub("-", raw.lower())
    slug = _KEY_HYPHEN_RUN_RE.sub("-", slug).strip("-")
    # `slug` truthy as well as equal: an empty id folds to an empty slug, which
    # equals itself and would take the passthrough, yielding `bench--s0`. That
    # happens to satisfy the pattern, which is exactly why it needs saying — it
    # would pass the validator and the test, and still be a key built out of
    # nothing.
    if slug and slug == raw:
        return slug
    digest = hashlib.blake2s(raw.encode("utf-8"), digest_size=4).hexdigest()
    # `or "q"`: an id of nothing but separators folds to the empty string, and a
    # key starting with the digest's hyphen is invalid all over again.
    return f"{slug or 'q'}-{digest}"


def _log_store_id(memory_id: str, qid: str, key: str) -> None:
    if not memory_id:
        return
    try:
        with open(BENCH_IDS_LOG, "a", encoding="utf-8") as fh:
            fh.write(json.dumps({"id": memory_id, "qid": qid, "key": key}) + "\n")
    except OSError:
        pass


def _mesh_env() -> dict[str, str]:
    env = dict(os.environ)
    for key in ("MESH_API_URL", "MESH_AGENT_KEY", "RECALL_GRAPH_ENABLED"):
        val = os.environ.get(key)
        if val is not None:
            env[key] = val
    return env


def _parse_tool_payload(result: Any) -> dict[str, Any]:
    """Turn a `call_tool` result into the dict callers check with `.get("error")`.

    Every mesh-mcp tool answers success with `jsonResult(v)` — a single JSON
    text block — and failure with `mcpsdk.NewToolResultError(msg)`, which sets
    `isError=True` and puts a PLAIN, NON-JSON string in that same slot. This
    function used to try `json.loads` on that string regardless, fail, and swallow
    the failure into `{"text": text}` — a shape `_store`/`_search` don't recognise
    as an error (they only check the `"error"` key), so a genuine server-side tool
    error (auth expiry, validation, a panic recovery message) was silently treated
    as an empty-but-valid response: zero items, unknown search_mode, no exception,
    nothing logged. That is precisely how evc-mesh#352 happened — one recall call
    came back `isError=True`, was read as "nothing retrieved", and the mode-unknown
    it produced collapsed the ENTIRE 24-question run to INCONCLUSIVE, masking a
    real -0.5 regression in single-session-assistant as mere gate blindness.
    A non-JSON body that ISN'T flagged isError is just as untrustworthy — no
    mesh-mcp tool ever produces one on success — so it is folded into the same
    `{"error": ...}` shape rather than given a free pass.
    """
    if result is None:
        return {}
    if isinstance(result, dict):
        return result
    content = getattr(result, "content", None)
    text = None
    for block in content or []:
        block_text = getattr(block, "text", None)
        if block_text:
            text = block_text
            break
    if getattr(result, "isError", False):
        return {"error": text or "tool call failed (isError, no text content)"}
    if not text:
        return {}
    try:
        parsed = json.loads(text)
    except json.JSONDecodeError:
        return {"error": f"non-JSON tool response: {text[:200]!r}"}
    if isinstance(parsed, list):
        return {"items": parsed}
    if isinstance(parsed, dict):
        return parsed
    return {}


def _coerce_score(value: Any) -> float | None:
    try:
        return float(value) if value is not None else None
    except (TypeError, ValueError):
        return None


def _to_record(item: dict[str, Any]) -> dict[str, Any]:
    content = (
        item.get("content")
        or (item.get("record") or {}).get("content")
        or item.get("text")
        or ""
    )
    score = item.get("score")
    if score is None:
        score = item.get("relevance", item.get("decayed_relevance"))
    return {
        "record": {"content": content},
        "score": _coerce_score(score),
        # Carried so the retrieval-only gate can map a hit back to its haystack
        # session: stores are keyed `bench-<sanitized-qid>-s<idx>` and tagged
        # `session-<idx>`. Only the `-s<idx>` suffix is parsed, so sanitizing the
        # id part does not affect the mapping.
        "key": item.get("key") or "",
        "tags": item.get("tags") or [],
    }


class MeshMemoryClient:
    """MCP client over Mesh remember/recall via a fresh stdio server per question."""

    # Shared across questions in a run: see BREAKER_TRIP_AFTER.
    _exhausted_questions = 0

    def __init__(
        self,
        *,
        question_id: str,
        mcp_url: str | None = None,
        api_key: str | None = None,
        workspace_id: str | None = None,
        agent_id: str | None = None,
        **_ignore: Any,
    ) -> None:
        self.qid = question_id
        # The tag keeps the id VERBATIM: it is the recall filter and the cleanup
        # handle, nothing validates it, and a sanitized tag would no longer match
        # rows an earlier run left behind.
        self.bench_tag = f"bench-{question_id}"
        # The key is the one field the server validates — see
        # sanitize_key_component for why it cannot just reuse the tag.
        self.key_prefix = f"bench-{sanitize_key_component(question_id)}"
        # Last tool-level rejection seen on THIS attempt, kept where the unwind
        # cannot overwrite it. See _tool_failure.
        self.tool_error: str | None = None
        # Stored-but-not-yet-deleted memory ids. Lives on the CLIENT, not on a
        # single connection, so a retry can finish the cleanup that a died-mid-run
        # attempt could not. `_dirty` means "an attempt abandoned rows" — including
        # rows whose id we never received — so the next attempt sweeps by TAG.
        self._pending: list[str] = []
        self._dirty = False
        # Populated by _search from the recall envelope. Stays UNKNOWN if the
        # server does not report it (older Mesh) — never silently assumed healthy.
        self.search_mode: str = SEARCH_MODE_UNKNOWN
        self.degraded: bool | None = None

    def ingest_and_search(
        self,
        *,
        sessions: list[list[dict]],
        dates: list[str],
        format_session_text,
        query: str,
        top_k: int,
    ) -> list[dict[str, Any]]:
        attempts = (
            1
            if type(self)._exhausted_questions >= BREAKER_TRIP_AFTER
            else CONNECT_RETRIES
        )
        for attempt in range(1, attempts + 1):
            # Cleared per attempt: a rejection recorded by a previous attempt must
            # never be promoted over THIS attempt's own, different failure.
            self.tool_error = None
            try:
                out = asyncio.run(
                    self._run(sessions, dates, format_session_text, query, top_k)
                )
            except BaseException as exc:  # noqa: BLE001 — re-raised below
                last = self._surfaced(exc)
                if attempt == attempts or not _is_transient(last):
                    break
                delay = CONNECT_BACKOFF_SECS[
                    min(attempt - 1, len(CONNECT_BACKOFF_SECS) - 1)
                ]
                logger.warning(
                    "%s: transient Mesh failure (%s) — attempt %d/%d, retrying in %.0fs",
                    self.qid,
                    flatten_exc(last),
                    attempt,
                    attempts,
                    delay,
                )
                time.sleep(delay)
            else:
                # A question that recovered proves the API is merely restarting,
                # not down — re-arm the breaker for whoever hits the next blip.
                type(self)._exhausted_questions = 0
                return out

        type(self)._exhausted_questions += 1
        if self._dirty or self._pending:
            # Every attempt is spent and fixtures are still in the store. Say so
            # loudly and greppably: a silent leak here is how bench haystacks end
            # up competing with real memories in agents' recall results.
            logger.error(
                "ORPHANED FIXTURES: %d memory rows left in the store for %s "
                "(tag %s) — cleanup never got a live connection. Purge with: "
                "recall/forget by tags_any=[%s]",
                len(self._pending), self.qid, self.bench_tag, self.bench_tag,
            )
        raise last

    def _tool_failure(self, tool: str, detail: Any) -> RuntimeError:
        """Record a tool-level rejection on the CLIENT, then return it to raise.

        A `remember`/`recall` rejection is raised from inside the anyio task groups
        that `stdio_client` and `ClientSession` own. Unwinding out of them cancels
        the transport, and the teardown's own `BrokenResourceError` REPLACES the
        exception that started the unwind — so `ingest_and_search` re-raised a
        transport fault for what was a `400 Validation failed` on a bad key, and
        the gate's error report named the plumbing instead of the cause. Six days
        of runs said `BrokenResourceError`; not one of them said `key must match`.

        The cause is only knowable here, before the unwind begins. Keep it on the
        client, which outlives the connection, and let `_surfaced` prefer it.
        """
        self.tool_error = f"{tool} failed: {detail}"
        return RuntimeError(self.tool_error)

    def _surfaced(self, exc: BaseException) -> BaseException:
        """The exception worth reporting: a recorded tool error beats its unwind.

        Only promotes when the tool error is not already legible in `exc`, so a
        failure that DID propagate cleanly is reported exactly once, and a
        transport failure with no tool error behind it is passed through untouched.
        """
        if not self.tool_error or self.tool_error in flatten_exc(exc):
            return exc
        promoted = RuntimeError(
            f"{self.tool_error} "
            f"(the transport teardown then reported {flatten_exc(exc)})"
        )
        promoted.__cause__ = exc
        return promoted

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

                async def _one(idx, turns, date):
                    async with sem:
                        mid = await self._store(
                            session, format_session_text(turns, date=date), idx, date
                        )
                        if mid:
                            self._pending.append(mid)

                # A previous attempt died holding rows. Now that we have a live
                # connection again, clear them before ingesting on top.
                if self._dirty:
                    await self._sweep(session, sem, deep=True)

                # Cleanup MUST run even if ingest or recall raises midway.
                try:
                    await asyncio.gather(*[
                        _one(idx, turns, date)
                        for idx, (turns, date) in enumerate(
                            zip(sessions, dates, strict=False)
                        )
                    ])
                    return await self._search(session, query, top_k)
                except BaseException:
                    # This attempt is abandoning rows on a connection that may
                    # already be dead. Whatever cleanup fails here is the next
                    # attempt's problem to finish.
                    self._dirty = True
                    raise
                finally:
                    await self._sweep(session, sem, deep=False)

    async def _sweep(self, session, sem, *, deep: bool) -> None:
        """Delete this question's fixtures. Survives across attempts.

        The old cleanup ran its deletes down the corpse of the very connection
        whose death caused the failure, and swallowed the errors — so an attempt
        that died mid-ingest simply abandoned its haystack. The bench stores at
        scope=workspace, so abandoned fixtures then surface in real agents'
        recall() results as if they were fleet memories. That is not theoretical:
        32 orphaned sessions were once found live in the shared workspace.

        `deep` also sweeps BY TAG. Deleting by id can only reach rows whose
        `remember` returned — and the case this retry path exists for is the
        connection dropping *during* a call, which commits the row server-side
        while the id never reaches us. Those rows are unreachable by id and
        invisible to `_pending`; the tag is the only handle left on them.
        """
        async def _forget(mid: str) -> bool:
            async with sem:
                try:
                    await session.call_tool("forget", {"memory_id": mid})
                    return True
                except Exception:
                    return False

        failed = [mid for mid in self._pending if not await _forget(mid)]

        if deep:
            try:
                async with sem:
                    found = await session.call_tool(
                        "recall",
                        {
                            "query": self.bench_tag,
                            "tags_any": [self.bench_tag],
                            "scope": STORE_SCOPE,
                            "limit": RECALL_CANDIDATE_LIMIT,
                            "min_importance": 0,
                        },
                    )
                strays = [
                    mid
                    for item in _parse_tool_payload(found).get("items") or []
                    if (mid := item.get("id")) and mid not in failed
                ]
                failed += [mid for mid in strays if not await _forget(mid)]
            except Exception as exc:
                logger.warning("%s: tag sweep failed: %s", self.qid, flatten_exc(exc))

        self._pending = failed
        if deep:
            # A completed tag sweep is the only thing that can prove nothing was
            # left behind, so it is the only thing allowed to clear the flag.
            self._dirty = bool(failed)
        elif failed:
            self._dirty = True
        # NB: a shallow sweep must NEVER clear `_dirty`. It runs in the `finally`
        # of a failing attempt, *after* the `except` set the flag — clearing it
        # here would tell the next attempt there is nothing to sweep, and the rows
        # whose ids we never received would be abandoned for good.

    async def _store(self, session, content, idx, date):
        result = await session.call_tool(
            "remember",
            {
                "key": f"{self.key_prefix}-s{idx}",
                "content": content,
                "scope": STORE_SCOPE,
                "tags": [self.bench_tag, SHARED_TAG, f"session-{idx}"],
            },
        )
        payload = _parse_tool_payload(result)
        if isinstance(payload, dict) and payload.get("error"):
            raise self._tool_failure("remember", payload["error"])
        mem = payload.get("memory") if isinstance(payload, dict) else None
        if isinstance(mem, dict):
            _log_store_id(mem.get("id", ""), self.qid, mem.get("key", ""))
            return mem.get("id")
        return None

    async def _search(self, session, query, top_k):
        args = {
            "query": query,
            "tags_any": [self.bench_tag],
            "scope": STORE_SCOPE,
            "limit": RECALL_CANDIDATE_LIMIT,
            "order_by": RECALL_ORDER_BY,
            "min_importance": 0,
        }
        _rw = float(os.environ.get("BENCH_RECENCY_WEIGHT", "0") or 0)
        if _rw > 0:
            args["recency_weight"] = _rw
        result = await session.call_tool("recall", args)
        payload = _parse_tool_payload(result)
        if isinstance(payload, dict) and payload.get("error"):
            raise self._tool_failure("recall", payload["error"])

        # Which arms actually served this recall. The MCP layer copies the REST
        # envelope verbatim, so these keys arrive untouched — when present.
        mode = payload.get("search_mode") if isinstance(payload, dict) else None
        self.search_mode = mode if isinstance(mode, str) and mode else SEARCH_MODE_UNKNOWN
        deg = payload.get("degraded") if isinstance(payload, dict) else None
        self.degraded = bool(deg) if isinstance(deg, bool) else None

        items = payload.get("items") or payload.get("results") or []
        if not isinstance(items, list):
            items = []
        mine = [it for it in items if self.bench_tag in (it.get("tags") or [])]
        return [_to_record(it) for it in mine[:top_k]]
