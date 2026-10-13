#!/usr/bin/env python3
"""End-to-end test of layout_docs.py against a LOCAL stand (scripts/local-stack.sh up).

Never point this at prod: it creates a project and documents.

  LOCAL_API=http://localhost:8106 LOCAL_EMAIL=... LOCAL_PASSWORD=... python3 -I test_layout_docs.py

Checks: dry-run writes nothing; red controls (unknown label, unknown id) abort and
change nothing; apply moves the right documents and leaves state / program roots
alone; a second apply is a no-op; rollback returns the tree (id + parent) to the
original; the same with --prune-merged (legacy folders are re-created, ids differ).
"""
import json
import os
import subprocess
import sys
import tempfile
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


def tree(token, pid):
    out, page = [], 1
    while True:
        d = call("GET", f"/projects/{pid}/documents?page={page}&page_size=100", token=token)
        out += d["items"]
        if not d.get("has_more"):
            return sorted((x["id"], x["parent_id"]) for x in out)
        page += 1


def run(args, env):
    p = subprocess.run([sys.executable, "-I", SCRIPT] + args, env=env, capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr


def check(cond, msg):
    print(("ok   " if cond else "FAIL ") + msg)
    if not cond:
        sys.exit(1)


def seed(token, ws, suffix):
    pr = call("POST", f"/workspaces/{ws}/projects", {"name": "Layout test " + suffix, "slug": "layout-test-" + suffix}, token)
    pid = pr["id"]
    mk = lambda slug, parent=None: call("POST", f"/projects/{pid}/documents",
                                        {"title": slug, "slug": slug, "parent_id": parent, "body": "x" if slug != "adrs" else ""}, token)
    d = {}
    d["adrs"] = mk("adrs")                      # legacy folder, empty page
    d["adr-1"] = mk("adr-1", d["adrs"]["id"])   # child of the legacy folder
    d["specs"] = mk("specs")                    # canon folder that already exists
    for s in ("spec-a", "runbook-a", "report-a", "note-q", "state", "prog-root", "scratch-a"):
        d[s] = mk(s)
    d["prog-child"] = mk("prog-child", d["prog-root"]["id"])
    return pid, d


def table(project, d, labels):
    rows = "\n".join(f"| {d[s]['id'][:8]} | {s} | {s} | 2026-01-01 | t | {lab} |" for s, lab in labels.items())
    return (f"### {project} ({len(labels)})\n\n| id | slug | title | дата | автор | папка |\n|---|---|---|---|---|---|\n{rows}\n")


def main():
    reg = call("POST", "/auth/login", {"email": os.environ["LOCAL_EMAIL"], "password": os.environ["LOCAL_PASSWORD"]})
    token = reg["tokens"]["access_token"]
    ws = call("GET", "/workspaces", token=token)[0]["id"]
    suffix = os.urandom(3).hex()
    pid, d = seed(token, ws, suffix)
    slug = "layout-test-" + suffix
    labels = {"spec-a": "specs", "runbook-a": "runbooks", "report-a": "reports", "note-q": "?",
              "state": "?", "prog-root": "specs", "scratch-a": "scratch-delete", "adr-1": "adrs"}
    tmp = tempfile.mkdtemp(prefix="layout-test-")
    good, bad_label, bad_id = (os.path.join(tmp, n) for n in ("good.md", "bad_label.md", "bad_id.md"))
    open(good, "w").write(table(slug, d, labels))
    open(bad_label, "w").write(table(slug, d, {**labels, "spec-a": "wat"}))
    open(bad_id, "w").write(table(slug, d, labels).replace(d["spec-a"]["id"][:8], "deadbeef"))
    env = {**os.environ, "MESH_API_URL": os.environ["LOCAL_API"], "MESH_BEARER_TOKEN": token, "MESH_WORKSPACE_ID": ws}
    env.pop("MESH_AGENT_KEY", None)
    base = tree(token, pid)

    rc, out = run(["--table", good], env)
    check(rc == 0 and tree(token, pid) == base, "dry-run succeeds and changes nothing")
    for name, path in (("unknown folder label", bad_label), ("unknown document id", bad_id)):
        for mode in ([], ["--apply", "--project", slug, "--journal", os.path.join(tmp, "x.jsonl")]):
            rc, out = run(["--table", path] + mode, env)
            check(rc != 0 and tree(token, pid) == base, f"red control: {name} {'apply' if mode else 'dry-run'} aborts, tree untouched")
    check(not os.path.exists(os.path.join(tmp, "x.jsonl")), "red control: no journal written")

    rc, out = run(["--table", good], env)
    check("с --prune-merged не ниже" in out, "dry-run reports root counts with and without pruning")
    for prune in (False, True):
        j = os.path.join(tmp, f"j-{prune}.jsonl")
        extra = ["--prune-merged"] if prune else []
        rc, out = run(["--table", good, "--apply", "--project", slug, "--journal", j] + extra, env)
        check(rc == 0, f"apply (prune={prune}) rc 0: {out[-200:] if rc else ''}")
        after = {x["slug"]: x for x in call("GET", f"/projects/{pid}/documents?page_size=100", token=token)["items"]}
        parent_slug = lambda s: next((y["slug"] for y in after.values() if y["id"] == after[s]["parent_id"]), None)
        check(parent_slug("spec-a") == "specs" and parent_slug("runbook-a") == "guides"
              and parent_slug("report-a") == "reports" and parent_slug("note-q") == "archive"
              and parent_slug("adr-1") == "decisions", "documents landed in canon folders")
        check(after["state"]["parent_id"] is None and after["prog-root"]["parent_id"] is None
              and after["scratch-a"]["parent_id"] is None and parent_slug("prog-child") == "prog-root",
              "state, program root and scratch-delete untouched")
        check(("adrs" not in after) == prune, f"legacy folder adrs {'pruned' if prune else 'kept'}")
        n = len(open(j).read().splitlines())
        rc, out = run(["--table", good, "--apply", "--project", slug, "--journal", j] + extra, env)
        check(rc == 0 and len(open(j).read().splitlines()) == n, "second apply is a no-op (journal did not grow)")
        # red control: a journal that points a delete at some other document is refused, nothing changes
        tampered = os.path.join(tmp, f"t-{prune}.jsonl")
        with open(tampered, "w") as fh:
            fh.write(json.dumps({"op": "create_folder", "doc_id": d["state"]["id"], "project_id": pid, "slug": "specs",
                                 "old_parent_id": None, "new_parent_id": None, "ts": "x"}) + "\n")
        snap = tree(token, pid)
        rc, out = run(["--rollback", tampered], env)
        check(rc != 0 and tree(token, pid) == snap, "red control: tampered journal (wrong document) refused, tree untouched")
        rc, out = run(["--rollback", j], env)
        check(rc == 0, f"rollback rc 0: {out[-200:] if rc else ''}")
        now = tree(token, pid)
        if not prune:
            check(now == base, "after rollback the tree (id + parent) is identical to the original")
        else:
            by_id = lambda t: {i: p for i, p in t}
            old_adrs, new_adrs = d["adrs"]["id"], next(x["id"] for x in call("GET", f"/projects/{pid}/documents?page_size=100", token=token)["items"] if x["slug"] == "adrs")
            norm = lambda t: sorted((i if i != new_adrs else old_adrs, p if p != new_adrs else old_adrs) for i, p in t)
            check(norm(now) == base, "after rollback with prune the tree is identical (legacy folder re-created, new id)")
            # re-seed legacy state is back; reset the base for nothing further
    print("ALL OK")


if __name__ == "__main__":
    main()
