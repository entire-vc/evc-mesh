#!/usr/bin/env python3
"""Compare main and branch recall p95 on one local memory-bench corpus.

Run inside recall-gate-branch, while its real embedder is alive. Both API
binaries use the same throwaway database and key. Requests alternate order to
balance cache warmth and runner drift; the branch keeps MEMORY_READ_LOG on.
The report retains both SHAs and samples, including periodic journal flushes.
"""
from __future__ import annotations

import argparse
import http.client
import json
import math
import os
from pathlib import Path
import re
import time
from urllib.parse import urlencode, urlsplit

from ci_bootstrap import wait_for_api


class Client:
    def __init__(self, url: str, key: str):
        parsed = urlsplit(url)
        if parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "localhost", "::1"}:
            raise ValueError("read-log p95 requires a local throwaway API")
        self.conn = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=30)
        self.headers = {"X-Agent-Key": key, "Content-Type": "application/json",
                        "X-Mesh-Caller": "script", "User-Agent": "memory-bench-read-log-p95"}

    def request(self, method: str, path: str, body=None):
        data = json.dumps(body).encode() if body is not None else None
        start = time.perf_counter_ns()
        self.conn.request(method, path, body=data, headers=self.headers)
        response = self.conn.getresponse()
        raw = response.read()
        elapsed_ms = (time.perf_counter_ns() - start) / 1_000_000
        if response.status not in (200, 201):
            raise RuntimeError(f"read-log p95: {method} {path} returned {response.status}")
        return json.loads(raw), elapsed_ms

    def recall(self, question: str, branch: bool) -> float:
        query = urlencode({"q": question, "tags": "read-log-p95", "limit": 80,
                           "min_importance": 0})
        result, elapsed = self.request("GET", "/api/v1/memories/search?" + query)
        if (not result.get("items") or result.get("search_mode") != "hybrid"
                or result.get("degraded") is not False or result.get("dense_rows", 0) <= 0):
            raise RuntimeError("read-log p95 requires nonempty, hybrid recall on both builds")
        if branch and any(not item.get("source") for item in result["items"]):
            raise RuntimeError("branch recall is missing source")
        return elapsed


def percentile95(samples: list[float]) -> float:
    if not samples:
        raise ValueError("p95 requires samples")
    return sorted(samples)[math.ceil(len(samples) * 0.95) - 1]


def report(main: list[float], branch: list[float], main_sha: str, branch_sha: str) -> dict:
    baseline, current = percentile95(main), percentile95(branch)
    delta = (current / baseline - 1) * 100
    return {"main_sha": main_sha, "branch_sha": branch_sha, "main_p95_ms": baseline,
            "branch_p95_ms": current, "delta_percent": delta, "tolerance_percent": 5,
            "pass": baseline * 0.95 <= current <= baseline * 1.05,
            "samples_ms": {"main": main, "branch": branch}}


def seed(client: Client, dataset: list[dict]) -> int:
    # One complete haystack from the existing 24-question bench. This has the
    # same corpus size as one retrieval-gate question, and shares neither keys
    # nor tags with that gate's fixtures.
    sessions = dataset[0]["haystack_sessions"]
    for index, turns in enumerate(sessions):
        content = "\n".join(f"{turn['role']}: {turn['content']}" for turn in turns)
        client.request("POST", "/api/v1/memories", {
            "key": f"read-log-p95-{index}", "content": content, "scope": "workspace",
            "tags": ["read-log-p95"], "relevance": 1,
        })
    # Wait for the observed embedding backlog, not an assumed settling delay.
    deadline = time.monotonic() + 180
    while True:
        client.conn.request("GET", "/metrics")
        response = client.conn.getresponse()
        raw = response.read().decode()
        depth = re.search(r"^mesh_memory_embed_inflight\s+([\d.eE+-]+)$", raw, re.M)
        if response.status != 200 or depth is None:
            raise RuntimeError("cannot observe embedding backlog before p95 measurement")
        if float(depth.group(1)) == 0:
            return len(sessions)
        if time.monotonic() >= deadline:
            raise RuntimeError("embedding backlog did not drain before p95 measurement")
        time.sleep(1)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--main-url", required=True)
    parser.add_argument("--branch-url", required=True)
    parser.add_argument("--main-sha", required=True)
    parser.add_argument("--main-read-log", choices=("on", "off"), required=True,
                        help="MEMORY_READ_LOG of the main build, kept in the receipt")
    parser.add_argument("--branch-sha", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    key = os.environ["MESH_AGENT_KEY"]  # named carrier; never printed
    clients = {"main": Client(args.main_url, key), "branch": Client(args.branch_url, key)}
    for url in (args.main_url, args.branch_url):
        wait_for_api(url)
    dataset = json.loads((Path(__file__).parent / "data/lme_s_24.json").read_text())
    corpus_size = seed(clients["branch"], dataset)
    queries = [entry["question"] for entry in dataset]
    for question in queries:
        for name, client in clients.items():
            client.recall(question, name == "branch")
    samples: dict[str, list[float]] = {"main": [], "branch": []}
    start = time.monotonic()
    index = 0
    # At least two 15-second flush intervals, with no periodic work disabled.
    while index < 480 or time.monotonic() - start < 35:
        if time.monotonic() - start > 600:
            raise RuntimeError("p95 sample budget exhausted")
        order = ("main", "branch") if index % 2 == 0 else ("branch", "main")
        for name in order:
            samples[name].append(clients[name].recall(queries[index % len(queries)], name == "branch"))
        index += 1
    result = report(samples["main"], samples["branch"], args.main_sha, args.branch_sha)
    result.update({"main_memory_read_log": args.main_read_log, "branch_memory_read_log": "on",
                   "corpus": "lme_s_24.json:first-haystack", "corpus_size": corpus_size,
                   "query_count": len(queries), "duration_seconds": time.monotonic() - start})
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2) + "\n")
    print(f"recall p95 main={result['main_p95_ms']:.3f}ms branch={result['branch_p95_ms']:.3f}ms "
          f"delta={result['delta_percent']:+.2f}% tolerance=±5% samples={index} "
          f"verdict={'PASS' if result['pass'] else 'FAIL'}")
    return 0 if result["pass"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
