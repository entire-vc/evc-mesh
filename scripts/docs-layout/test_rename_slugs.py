#!/usr/bin/env python3
"""Offline check of --rename-slugs. Run: python3 -I test_rename_slugs.py

The only fake is the HTTP boundary: a small in-memory Mesh that keeps documents,
applies PATCH {"slug"} the way the server does (409 on a sibling collision) and
records every call. Red controls: a collision aborts before the first write, a
server that ignores `slug` stops the run at the first document, and a forged
journal is refused before any write.
"""
import json
import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import layout_docs as L

PID = "11111111-1111-4111-8111-111111111111"
PROJECT = {"id": PID, "slug": "lab"}


def uid(n):
    return f"{n:08x}-0000-4000-8000-000000000000"


class FakeMesh:
    def __init__(self, docs, supports_slug=True):
        self.docs_ = {d["id"]: dict(d, project_id=PID) for d in docs}
        self.supports_slug, self.calls = supports_slug, []

    def docs(self, pid):
        return [dict(d) for d in self.docs_.values()]

    def call(self, method, path, body=None):
        self.calls.append((method, path, body))
        if path == f"/projects/{PID}":
            return {"slug": "lab"}
        did = path.rsplit("/", 1)[-1]
        d = self.docs_.get(did)
        if d is None:
            raise L.Abort(f"HTTP 404 on {method} {path}")
        if method == "GET":
            return dict(d)
        if method == "PATCH" and body and "slug" in body and self.supports_slug:
            for o in self.docs_.values():
                if o["id"] != did and o["parent_id"] == d["parent_id"] and o["slug"] == body["slug"]:
                    raise L.Abort(f"HTTP 409 on {method} {path}: slug taken")
            d["slug"] = body["slug"]
        return dict(d)

    def writes(self):
        return [c for c in self.calls if c[0] != "GET"]


def doc(n, slug, parent, title="T"):
    return {"id": uid(n), "slug": slug, "title": title, "parent_id": parent}


def tree():
    dec, gui, spe, res = uid(1), uid(2), uid(3), uid(4)
    return [
        doc(1, "decisions", None), doc(2, "guides", None), doc(3, "specs", None), doc(4, "research", None),
        doc(10, "adrs001-use-nats", dec), doc(11, "adrscommit-identity-guard", dec), doc(12, "adr-007-fine", dec),
        doc(13, "runbooksargus-pulse-protocol", gui), doc(14, "раннбук-deploy", gui),
        doc(15, "prdinvoices", spe), doc(16, "rndspike", res),
        doc(17, "---", dec, title="Мост через Neva"), doc(18, "--", dec, title="!!!"),
        doc(19, "adrs-already-hyphen", dec),
        doc(20, "adrs002-not-in-decisions", spe),  # prefix looks mangled, parent says otherwise
    ]


def by_slug(m, n):
    return m.docs_[uid(n)]["slug"]


def main():
    # 1. plan: candidates, rule, irreversible, skips
    plan = L.plan_renames("lab", tree())
    got = {o["old_slug"]: o["new_slug"] for o in plan["ops"]}
    assert got == {
        "adrs001-use-nats": "adr-001-use-nats",
        "adrscommit-identity-guard": "adr-commit-identity-guard",
        "runbooksargus-pulse-protocol": "runbook-argus-pulse-protocol",
        "раннбук-deploy": "runbook-deploy",
        "prdinvoices": "prd-invoices",
        "rndspike": "rnd-spike",
        "---": "мост-через-neva",
    }, got
    assert {o["old_slug"] for o in plan["ops"] if o["irreversible"]} == {"---"}
    skipped = {d["slug"] for d, _, _ in plan["skipped"]}
    assert skipped == {"--", "adrs002-not-in-decisions"}, skipped
    print("ok   candidates, new slugs, irreversible flag and skips")
    text = L.render_renames("lab", plan)
    assert "| adrs001-use-nats | adr-001-use-nats |" in text and "кандидатов на переименование 7" in text
    print("ok   dry-run table 'was -> becomes' with the count")

    # 2. red control: collision with a sibling aborts, nothing is written
    docs = tree() + [doc(30, "adr-001-use-nats", uid(1))]
    try:
        L.plan_renames("lab", docs)
        print("FAIL collision with a sibling was not refused")
        sys.exit(1)
    except L.Abort as e:
        assert "collides" in str(e)
    both = tree() + [doc(31, "adrs-x", uid(1)), doc(32, "adrsx", uid(1))]  # adrs-x is left alone, adrsx -> adr-x
    L.plan_renames("lab", both)
    twin = tree() + [doc(33, "adrscommit-identity-guard-", uid(1))]  # two candidates -> same new slug
    twin[-1]["slug"] = "adrscommit-identity-guard"
    try:
        L.plan_renames("lab", twin)
        print("FAIL two candidates for one slug were not refused")
        sys.exit(1)
    except L.Abort as e:
        assert "collides" in str(e)
    print("ok   red control: a collision (sibling or twin candidate) aborts the plan")

    # 3. apply + journal + rollback round trip
    m = FakeMesh(tree())
    jp = os.path.join(tempfile.mkdtemp(), "journal.jsonl")
    L.apply_renames(m, PROJECT, plan, jp, allow_irreversible=False)
    assert by_slug(m, 10) == "adr-001-use-nats" and by_slug(m, 17) == "---", "irreversible one is skipped by default"
    recs = L.read_journal(jp)
    assert len(recs) == 6 and all(r["op"] == "rename" for r in recs)
    L.rollback(m, jp)
    assert [d["slug"] for d in m.docs_.values()] == [d["slug"] for d in tree()], "rollback restores every old slug"
    print("ok   apply writes a journal, rollback restores the old slugs")

    # 4. --allow-irreversible renames the hyphen-only one; rollback leaves it and says so
    m = FakeMesh(tree())
    L.apply_renames(m, PROJECT, plan, jp + "2", allow_irreversible=True)
    assert by_slug(m, 17) == "мост-через-neva"
    L.rollback(m, jp + "2")
    assert by_slug(m, 17) == "мост-через-neva" and by_slug(m, 10) == "adrs001-use-nats"
    print("ok   irreversible rename is opt-in; rollback restores the rest and leaves it")

    # 5. red control: a server that ignores `slug` stops the run at the first document
    m = FakeMesh(tree(), supports_slug=False)
    try:
        L.apply_renames(m, PROJECT, plan, jp + "3")
        print("FAIL a server without slug rename was not detected")
        sys.exit(1)
    except L.Abort as e:
        assert "support slug rename" in str(e)
    assert len(m.writes()) == 1 and not os.path.getsize(jp + "3"), "one PATCH, nothing journaled"
    print("ok   red control: server that ignores slug -> stop after the first PATCH, no journal")

    # 6. red control: forged journals are refused before any write
    m = FakeMesh(tree())
    L.apply_renames(m, PROJECT, plan, jp + "4")
    good = [json.loads(x) for x in open(jp + "4")]

    def forged(mod, why, needle):
        recs = [dict(r) for r in good]
        mod(recs)
        fd, p = tempfile.mkstemp(suffix=".jsonl")
        os.close(fd)
        with open(p, "w") as fh:
            fh.write("\n".join(json.dumps(r) for r in recs) + "\n")
        before = len(m.writes())
        try:
            L.rollback(m, p)
            print(f"FAIL forged journal accepted: {why}")
            sys.exit(1)
        except L.Abort as e:
            assert needle in str(e), (why, str(e))
        assert len(m.writes()) == before, f"{why}: a write happened"
        print(f"ok   forged journal refused ({why})")

    forged(lambda r: r[0].update(old_slug="state"), "old slug is a protected one", "reserved")
    forged(lambda r: r[0].update(old_slug="decisions"), "old slug is a canon folder", "reserved")
    forged(lambda r: r[0].update(old_slug="Not A Slug"), "old slug is not a slug", "not a slug")
    forged(lambda r: r[0].update(new_slug="x", slug="adr-001-use-nats"), "slug differs from new_slug", "slug == new_slug")
    forged(lambda r: r[0].update(project="marketing"), "project name differs from the live one", "live project")
    forged(lambda r: r[0].update(slug="tampered", new_slug="tampered"), "live slug differs from the journal", "does not match the live")
    forged(lambda r: r[0].update(doc_id=uid(11)), "doc id of another document", "does not match the live")
    forged(lambda r: r[0].update(project_id="not-a-uuid"), "project_id is not a UUID", "UUID")
    forged(lambda r: r[0].update(old_slug="attacker-chosen", irreversible=True), "ordinary rename flagged irreversible with a forged old slug", "irreversible is only")
    forged(lambda r: r[0].update(old_slug="attacker-chosen"), "old slug is another valid slug the rule never maps to new_slug", "by the rule")

    # 7. the old slug was taken by another document meanwhile: refused up front, nothing written
    m.docs_[uid(31)] = dict(doc(31, "adrs001-use-nats", uid(1)), project_id=PID)
    before = len(m.writes())
    try:
        L.rollback(m, jp + "4")
        print("FAIL rollback onto a taken slug was not refused")
        sys.exit(1)
    except L.Abort as e:
        assert "now has it" in str(e)
    assert len(m.writes()) == before
    print("ok   rollback onto a slug taken in the meantime is refused before any write")

    # 8. excluded project is never touched
    class One(FakeMesh):
        def projects(self):
            return [{"id": PID, "slug": "website-relaunch"}, PROJECT]
    api = One(tree())
    ns = type("A", (), dict(apply=True, project="website-relaunch", journal=None, report=None,
                            allow_irreversible=False))()
    try:
        L.run_rename(api, ns)
        print("FAIL excluded project was not refused")
        sys.exit(1)
    except L.Abort as e:
        assert "excluded" in str(e)
    assert not api.writes()
    print("ok   --rename-slugs --apply on the excluded project is refused")
    print("ALL OK")


if __name__ == "__main__":
    main()
