#!/usr/bin/env python3
"""Executable RED/GREEN and full-window proof against real MCP stdio + API.

Only the branch arm's isolated bench workspace is permitted. No mock responses.
All writes are read back by key and cleaned up even if the window fails.
"""
import asyncio
import hashlib
from importlib.metadata import version
import json
import os
from pathlib import Path
import subprocess
import sys

import mesh_client_stdio as client


async def prove():
    client.assert_bench_workspace()
    if os.environ.get("RECALL_GRAPH_ENABLED") != "true":
        raise ValueError("window proof requires the graph arm enabled")
    from mcp import ClientSession, StdioServerParameters
    from mcp.client.stdio import stdio_client

    c = client.MeshMemoryClient(question_id="contract-window", age_mode="ingest-now")
    params = StdioServerParameters(command=client.MESH_MCP_COMMAND,
                                   args=client.MESH_MCP_ARGS, env=client._mesh_env())
    async with stdio_client(params) as (read, write):
        async with ClientSession(read, write) as session:
            await asyncio.wait_for(session.initialize(), client.INIT_TIMEOUT)
            # The previous harness payload, on this exact pair of binaries.
            # Literal historical _search payload, independent of the new builder.
            old = {"query": "bench contract preflight", "tags_any": [c.bench_tag],
                   "scope": "workspace", "limit": 80,
                   "order_by": "relevance:desc", "min_importance": 0}
            red = client._parse_tool_payload(await session.call_tool("recall", old))
            assert "limit must be an integer between 1 and 50" in str(red.get("error")), red
            print("RED: previous limit80 payload rejected; zero fixtures written")
            other = dict(old, limit=50, recency_weight=0.1)
            rejected = client._parse_tool_payload(await session.call_tool("recall", other))
            assert "unknown recall parameter" in str(rejected.get("error")), rejected
            print("RED: recency_weight rejected; zero fixtures written")
            client.RECALL_CANDIDATE_LIMIT = 80
            try:
                try:
                    await c._preflight_session(session)
                except RuntimeError as exc:
                    assert "limit must be an integer between 1 and 50" in str(exc)
                    print("RED: preflight stopped on the first deterministic rejection")
                else:
                    raise AssertionError("preflight accepted the incompatible limit")
            finally:
                client.RECALL_CANDIDATE_LIMIT = 50
                c.tool_error = None
            await c._preflight_session(session)
            empty = client._parse_tool_payload(await session.call_tool("recall", client.recall_arguments("bench contract preflight", c.bench_tag)))
            assert empty["items"] == [], "preflight wrote fixtures"
            print("GREEN: same measurement builder, all continuation offsets accepted before ingestion")
            entries = json.loads(Path(__file__).with_name("data").joinpath("lme_s_24.json").read_text())
            longest = max(entries, key=lambda e: len(e["haystack_sessions"]))
            assert len(longest["haystack_sessions"]) == 54
            token = "windowproof"
            try:
                for index, turns in enumerate(longest["haystack_sessions"]):
                    # Synthetic reachability control, independent of the scored
                    # corpus: common searchable token + distinct original text.
                    text = f"{token} archive entry {index}. " + json.dumps(turns, ensure_ascii=False)
                    mid = await c._store(session, text, index, "")
                    assert mid, f"store {index} returned no ID"
                    c._pending.append(mid)
                    saved = client._parse_tool_payload(await session.call_tool("get_memory", {
                        "key": f"{c.key_prefix}-s{index}", "scope": client.STORE_SCOPE,
                    }))
                    assert saved.get("id") == mid, saved
                await asyncio.to_thread(client._wait_for_embed_drain_sync,
                                        os.environ["MESH_API_URL"], c.qid)
                top = await c._search(session, token, 10)
                ids = [r["id"] for r in c.ranked_records]
                assert len(ids) == len(set(ids)) == 54, f"window returned {len(ids)}/54 unique IDs"
                assert set(ids) == set(c._pending), "window lost a saved session"
                first_ids = c.recall_pages[0]["ids"]
                assert ids[:len(first_ids)] == first_ids, "continuation changed first-page ranks"
                assert len(c.recall_pages) > 1 and set(ids) - set(first_ids), "no continuation-only session reached"
                assert [r["id"] for r in top] == c.recall_pages[0]["ids"][:10]
                later_gold = ids[-1]
                assert ids.index(later_gold) + 1 == 54 and later_gold not in {r["id"] for r in top}
                receipt = {
                    "api_commit": os.environ["CI_COMMIT_SHA"],
                    "mcp_ref": os.environ["MCP_SOURCE_REF"], "mcp_commit": os.environ["MCP_COMMIT"],
                    "mcp_sdk": version("mcp"), "python": sys.version.split()[0],
                    "baseline_sha256": hashlib.sha256(Path(__file__).with_name("baseline_retrieval_branch.json").read_bytes()).hexdigest(),
                    "graph_enabled": True, "saved_and_reread": 54, "unique_recalled": len(ids),
                    "pages": c.recall_pages, "top10": [r["id"] for r in top], "later_gold_rank": 54,
                }
                Path("results").mkdir(exist_ok=True)
                Path("results/contract-proof.json").write_text(json.dumps(receipt, indent=2) + "\n")
                print(f"GREEN: saved/reread/recalled 54/54; graph=true; top10 preserved; later gold rank=54; pages={[p['offset'] for p in c.recall_pages]}")
            finally:
                await c._sweep(session, asyncio.Semaphore(1), deep=False)
                assert not c._pending, "contract proof fixtures were not cleaned up"


if __name__ == "__main__":
    try:
        # Exercise the ordinary CLI, including its rc/reason, on a deterministic
        # contract fault. This is the original wire payload, not a fake reply.
        incompatible = subprocess.run(
            [sys.executable, "-c", "import mesh_client_stdio as c; c.RECALL_CANDIDATE_LIMIT=80; import run_ci; raise SystemExit(run_ci.main())",
             "--retrieval-only", "--arm", "branch"],
            capture_output=True, text=True, timeout=90,
        )
        assert incompatible.returncode == 2, incompatible.stdout + incompatible.stderr
        assert "GATE_REASON: recall-preflight" in incompatible.stdout, incompatible.stdout
        assert "limit must be an integer between 1 and 50" in incompatible.stdout, incompatible.stdout
        print("RED: ordinary run_ci CLI limit80 => recall-preflight INCONCLUSIVE rc=2; zero questions attempted")
        # Fault injection at the real process/API boundary, not a fake response.
        unavailable = subprocess.run(
            [sys.executable, "recall_preflight.py"],
            env={**os.environ, "MESH_API_URL": "http://127.0.0.1:1"},
            capture_output=True, text=True, timeout=90,
        )
        assert unavailable.returncode == 2, unavailable.stdout
        assert "GATE_REASON: recall-preflight" in unavailable.stdout, unavailable.stdout
        print("RED: unavailable API => recall-preflight INCONCLUSIVE rc=2")
        asyncio.run(prove())
    except Exception as exc:
        print(f"GATE_REASON: recall-contract-proof — INCONCLUSIVE: {client.flatten_exc(exc)}")
        raise SystemExit(2)
