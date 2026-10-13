#!/usr/bin/env python3
"""Offline check: plan_project rejects a table that lists one document twice or has
an empty slug (the destination would depend on row order / any slug would match).
Pure function, no API. Run: python3 -I test_plan_rows.py
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import layout_docs as L

DOC = {"id": "aaaaaaaa-1111-4111-8111-111111111111", "slug": "spec-a", "parent_id": None, "title": "A"}


def row(label, slug="spec-a"):
    return {"id8": "aaaaaaaa", "slug": slug, "label": label}


def rejected(rows, needle):
    try:
        L.plan_project("p", rows, [dict(DOC)])
    except L.Abort as e:
        return needle in str(e)
    return False


def planned_ok(slug_in_table, live_slug):
    doc = dict(DOC, slug=live_slug)
    try:
        L.plan_project("p", [row("specs", slug=slug_in_table)], [doc])
    except L.Abort:
        return False
    return True


def mesh_accepts(url):
    os.environ.update(MESH_API_URL=url, MESH_AGENT_KEY="k")
    try:
        L.Mesh()
    except L.Abort:
        return False
    return True


def main():
    assert not rejected([row("specs")], "more than once"), "positive control: a single row must plan"
    print("ok   positive control: one row plans")
    assert rejected([row("specs"), row("plans")], "more than once")
    print("ok   same document in two rows with different labels is rejected")
    assert rejected([row("specs", slug="")], "empty table slug")
    print("ok   empty table slug is rejected")
    long_slug = "a" * 60
    assert planned_ok(long_slug[:45], long_slug) and planned_ok("spec-a", "spec-a")
    assert not planned_ok("s", "spec-a") and not planned_ok(long_slug[:44], long_slug)
    print("ok   a slug prefix is accepted only as the 45-char cut (or exact); 's' for 'spec-a' is rejected")
    assert mesh_accepts("https://mesh.example") and mesh_accepts("http://localhost:8106")
    assert not mesh_accepts("http://mesh.example") and not mesh_accepts("http://localhost.evil.example")
    print("ok   plain http is refused except for localhost; https and localhost accepted")


if __name__ == "__main__":
    main()
