#!/usr/bin/env python3
"""Read-only preflight with the same arguments as measurement. rc=2 is blind."""
import asyncio

from mesh_client_stdio import MeshMemoryClient, flatten_exc


def check():
    try:
        asyncio.run(MeshMemoryClient(question_id="contract-preflight").preflight())
    except Exception as exc:
        print(f"INCONCLUSIVE: recall preflight failed before corpus ingestion: {flatten_exc(exc)}")
        print("GATE_REASON: recall-preflight — 0 questions attempted; no fixtures ingested")
        return 2
    print("Recall preflight: compatible; corpus measurement may proceed")
    return 0


if __name__ == "__main__":
    raise SystemExit(check())
