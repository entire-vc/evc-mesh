#!/usr/bin/env python3
"""End-to-end test of layout_docs.py --rename-slugs against a LOCAL stand (scripts/local-stack.sh up).

Never point this at prod: it creates a project and documents.

  LOCAL_API=http://localhost:8106 LOCAL_EMAIL=... LOCAL_PASSWORD=... python3 -I test_rename_slugs_e2e.py

Checks: dry-run writes nothing; apply renames the mangled slugs; the old slug and
the old path still resolve to the same document (moved_to), by UUID too; a second
apply is a no-op; a forged journal is refused with nothing changed; rollback
restores every old slug.
"""
import json
import os
import subprocess
import sys
import tempfile
import urllib.parse
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "layout_docs.py")
API = os.environ["LOCAL_API"].rstrip("/") + "/api/v1"
assert API.startswith(("http://", "https://")), "LOCAL_API must be http(s)"


def call(method, path, body=None, token=None):
    req = urllib.request.Request(API + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json",
                                          **({"Authorization": "Bearer " + token} if token else {})})
    # API comes from LOCAL_API of the operator; only http(s) is accepted above
    with urllib.request.urlopen(req, timeout=30) as r:  # nosemgrep: python.lang.security.audit.dynamic-urllib-use-detected.dynamic-urllib-use-detected
        raw = r.read()
        return json.loads(raw) if raw else {}



def run(args, env):
    p = subprocess.run([sys.executable, "-I", SCRIPT] + args, env=env, capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr


def check(cond, msg):
    print(("ok   " if cond else "FAIL ") + msg)
    if not cond:
        sys.exit(1)


def slugs(token, pid):
    out, page = {}, 1
    while True:
        d = call("GET", f"/projects/{pid}/documents?page={page}&page_size=100", token=token)
        out.update({x["id"]: x["slug"] for x in d["items"]})
        if not d.get("has_more"):
            return out
        page += 1


def main():
    reg = call("POST", "/auth/login", {"email": os.environ["LOCAL_EMAIL"], "password": os.environ["LOCAL_PASSWORD"]})
    token = reg["tokens"]["access_token"]
    ws = call("GET", "/workspaces", token=token)[0]["id"]
    suffix = os.urandom(3).hex()
    slug = "rename-test-" + suffix
    pid = call("POST", f"/workspaces/{ws}/projects", {"name": "Rename test " + suffix, "slug": slug}, token)["id"]
    mk = lambda s, parent=None, title=None: call("POST", f"/projects/{pid}/documents",
                                                 {"title": title or s, "slug": s, "parent_id": parent, "body": "x"}, token)
    dec, gui = mk("decisions"), mk("guides")
    want = {}
    for old, new, parent in (("adrs001-use-nats", "adr-001-use-nats", dec), ("adrscommit-identity-guard", "adr-commit-identity-guard", dec),
                             ("runbooksargus-pulse", "runbook-argus-pulse", gui)):
        want[mk(old, parent["id"])["id"]] = (old, new)
    keep = mk("adr-007-fine", dec["id"])
    child = mk("child-of-renamed", next(iter(want)))  # a document below a renamed one: its path alias must survive too
    tmp = tempfile.mkdtemp(prefix="rename-test-")
    env = {**os.environ, "MESH_API_URL": os.environ["LOCAL_API"], "MESH_BEARER_TOKEN": token, "MESH_WORKSPACE_ID": ws}
    env.pop("MESH_AGENT_KEY", None)
    before = slugs(token, pid)

    rc, out = run(["--rename-slugs", "--project", slug], env)
    check(rc == 0 and slugs(token, pid) == before, "dry-run succeeds and changes nothing")
    check("| adrs001-use-nats | adr-001-use-nats |" in out and "Итого кандидатов: 3" in out, "dry-run prints the 'was -> becomes' table and the count")

    jp = os.path.join(tmp, "rename.jsonl")
    rc, out = run(["--rename-slugs", "--apply", "--project", slug, "--journal", jp], env)
    after = slugs(token, pid)
    check(rc == 0 and all(after[i] == new for i, (_, new) in want.items()), "apply renames the candidates")
    check(after[keep["id"]] == "adr-007-fine" and after[dec["id"]] == "decisions", "documents that are fine and canon folders are untouched")
    for did, (old, new) in want.items():
        folder = "decisions" if old.startswith("adrs") else "guides"
        # the server records the old FULL path as the alias; a bare old slug is not an alias
        for ref in (f"{folder}/{old}",):
            r = call("GET", f"/projects/{pid}/documents/by-path/{urllib.parse.quote(ref, safe='/')}", token=token)
            check(r["id"] == did and r.get("moved_to"), f"old reference {ref!r} resolves to the same document (moved_to={r.get('moved_to')!r})")
        check(call("GET", f"/documents/{did}", token=token)["id"] == did, f"{did[:8]} is reachable by its UUID")
    r = call("GET", f"/projects/{pid}/documents/by-path/decisions/adrs001-use-nats/child-of-renamed", token=token)
    check(r["id"] == child["id"], "a path below the renamed document still resolves (subtree aliases)")

    rc, out = run(["--rename-slugs", "--apply", "--project", slug, "--journal", os.path.join(tmp, "again.jsonl")], env)
    check(rc == 0 and "Итого кандидатов: 0" in out, "a second apply finds nothing to do")

    forged = os.path.join(tmp, "forged.jsonl")
    lines = [json.loads(x) for x in open(jp)]
    lines[0]["old_slug"] = "decisions"
    open(forged, "w").write("\n".join(json.dumps(x) for x in lines) + "\n")
    rc, out = run(["--rollback", forged], env)
    check(rc == 2 and "reserved" in out and slugs(token, pid) == after, "red control: a forged journal is refused and nothing changes")
    lines = [json.loads(x) for x in open(jp)]
    lines[0]["doc_id"] = keep["id"]
    open(forged, "w").write("\n".join(json.dumps(x) for x in lines) + "\n")
    rc, out = run(["--rollback", forged], env)
    check(rc == 2 and slugs(token, pid) == after, "red control: a journal naming another document is refused and nothing changes")

    rc, out = run(["--rollback", jp], env)
    check(rc == 0 and slugs(token, pid) == before, "rollback restores every old slug")
    print("ALL OK")


if __name__ == "__main__":
    main()
