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
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
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

BENCH_IDS_LOG = os.environ.get(
    "BENCH_IDS_LOG", os.path.expanduser("~/bench/store_ids.jsonl")
)


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


class MeshMemoryClient:
    """MCP client over Mesh remember/recall via a fresh stdio server per question."""

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
        self.bench_tag = f"bench-{question_id}"

    def ingest_and_search(
        self,
        *,
        sessions: list[list[dict]],
        dates: list[str],
        format_session_text,
        query: str,
        top_k: int,
    ) -> list[dict[str, Any]]:
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
                self._ids = []

                async def _one(idx, turns, date):
                    async with sem:
                        mid = await self._store(
                            session, format_session_text(turns, date=date), idx, date
                        )
                        if mid:
                            self._ids.append(mid)

                await asyncio.gather(*[
                    _one(idx, turns, date)
                    for idx, (turns, date) in enumerate(
                        zip(sessions, dates, strict=False)
                    )
                ])
                results = await self._search(session, query, top_k)

                async def _del(mid):
                    async with sem:
                        try:
                            await session.call_tool("forget", {"memory_id": mid})
                        except Exception:
                            pass

                await asyncio.gather(*[_del(m) for m in self._ids])
                return results

    async def _store(self, session, content, idx, date):
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
        if isinstance(payload, dict) and payload.get("error"):
            raise RuntimeError(f"remember failed: {payload['error']}")
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
            raise RuntimeError(f"recall failed: {payload['error']}")
        items = payload.get("items") or payload.get("results") or []
        if not isinstance(items, list):
            items = []
        mine = [it for it in items if self.bench_tag in (it.get("tags") or [])]
        return [_to_record(it) for it in mine[:top_k]]
