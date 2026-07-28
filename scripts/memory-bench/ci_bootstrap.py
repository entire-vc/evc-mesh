#!/usr/bin/env python3
"""Bootstrap a throwaway workspace + bench agent key on a freshly-migrated Mesh.

The branch arm of the recall gate runs against `cmd/api` built from the PR head
against an empty database. There is no `MESH_BENCH_KEY` secret to use — the
secret belongs to prod, and using it here would be pointing the branch arm back
at the thing it exists to stop measuring.

So the arm mints its own: register the first user (which the API allows only
while `COUNT(users) == 0`, and which auto-creates that user's workspace), then
create one agent in it and keep the raw `api_key` the create call returns once.

Prints `MESH_AGENT_KEY=<key>` and `MESH_WORKSPACE_ID=<id>` on stdout in
`$GITHUB_ENV` format, so the workflow can do:

    python ci_bootstrap.py --api-url http://127.0.0.1:8005 >> "$GITHUB_ENV"

Exits non-zero with a readable reason on every failure path. A bootstrap that
half-succeeds and lets the bench run keyless produces 24 auth errors, which the
gate reports as `persistent-errors` — a true statement about the wrong layer.
"""

from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.error
import urllib.request

# Deterministic and disposable: the database is created and destroyed inside a
# single job, so these are not credentials in any meaningful sense. They are
# still not reused anywhere — a fixed password that appears in a repo must never
# be one that also opens something real.
BENCH_EMAIL = "recall-gate@ci.invalid"
BENCH_PASSWORD = "RecallGateCiOnlyNotASecret9c1f"
BENCH_AGENT_NAME = "recall-gate-bench"


def _req(method: str, url: str, body=None, token: str | None = None, timeout: int = 30):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, json.loads(resp.read() or b"{}")
    except urllib.error.HTTPError as exc:
        # HTTPError's str() discards the body, which is where the API says what
        # actually went wrong. Read it — a bootstrap failure diagnosed as
        # "HTTP 400" instead of its reason costs a whole CI round-trip.
        raw = exc.read().decode(errors="replace")
        return exc.code, {"_raw": raw}
    except urllib.error.URLError as exc:
        raise SystemExit(f"ci_bootstrap: cannot reach {url}: {exc.reason}") from exc


def wait_for_api(api_url: str, attempts: int = 60) -> None:
    """Block until /health answers 200, or fail loudly.

    Deliberately NOT the `for i in $(seq 15); do curl … || sleep 1; done` shape
    used elsewhere in this repo's CI: that loop prints "API server ready"
    unconditionally, so an API that never came up is indistinguishable from one
    that came up instantly, and the failure surfaces 10 minutes later as recall
    errors instead of here as a boot failure.
    """
    for i in range(attempts):
        try:
            with urllib.request.urlopen(f"{api_url}/health", timeout=3) as resp:
                if resp.status == 200:
                    print(f"# api ready after {i}s", file=sys.stderr)
                    return
        except Exception:  # noqa: BLE001 - any failure means "not yet"
            pass
        time.sleep(1)
    raise SystemExit(
        f"ci_bootstrap: {api_url}/health did not answer 200 in {attempts}s. "
        "The API under test never started — see the server log; do not read the "
        "recall score, there is nothing behind it."
    )


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--api-url", default="http://127.0.0.1:8005")
    ap.add_argument("--wait-secs", type=int, default=60)
    args = ap.parse_args()

    api = args.api_url.rstrip("/")
    wait_for_api(api, args.wait_secs)

    status, body = _req("POST", f"{api}/api/v1/auth/register", {
        "email": BENCH_EMAIL, "password": BENCH_PASSWORD, "name": "Recall Gate Bench",
    })
    if status not in (200, 201):
        raise SystemExit(
            f"ci_bootstrap: register failed ({status}): {body}. "
            "This arm requires a database with no users; a reused database or a "
            "closed MESH_ALLOW_REGISTRATION both land here."
        )

    token = (body.get("tokens") or {}).get("access_token")
    if not token:
        raise SystemExit(f"ci_bootstrap: register returned no access_token: {body}")

    status, workspaces = _req("GET", f"{api}/api/v1/workspaces", token=token)
    if status != 200:
        raise SystemExit(f"ci_bootstrap: listing workspaces failed ({status}): {workspaces}")

    # Whether registration auto-creates a workspace is not a contract this arm
    # should depend on — it is a product decision that can change in exactly the
    # kind of PR this gate runs on. Create one when there is none, so a change to
    # that behaviour shows up as a memory verdict rather than as a bootstrap
    # crash blamed on the author.
    if not workspaces:
        status, ws = _req(
            "POST", f"{api}/api/v1/workspaces",
            {"name": "Recall Gate Bench", "slug": "recall-gate-bench"}, token=token,
        )
        if status not in (200, 201) or not ws.get("id"):
            raise SystemExit(f"ci_bootstrap: workspace create failed ({status}): {ws}")
        workspaces = [ws]
    ws_id = workspaces[0]["id"]

    status, agent = _req(
        "POST", f"{api}/api/v1/workspaces/{ws_id}/agents",
        {"name": BENCH_AGENT_NAME, "agent_type": "claude_code"}, token=token,
    )
    if status not in (200, 201):
        raise SystemExit(f"ci_bootstrap: agent create failed ({status}): {agent}")

    # The raw key is returned exactly once, at creation. There is no read-back:
    # the column holds a bcrypt hash.
    key = agent.get("api_key")
    if not key:
        raise SystemExit(
            f"ci_bootstrap: agent created but the response carried no api_key: "
            f"{list(agent)}. Nothing can re-derive it — the stored form is a hash."
        )

    print(f"MESH_AGENT_KEY={key}")
    print(f"MESH_WORKSPACE_ID={ws_id}")
    print(f"# bootstrapped agent {agent['agent']['id']} in workspace {ws_id}",
          file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
