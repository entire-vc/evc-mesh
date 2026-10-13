#!/usr/bin/env python3
"""Offline check: --rollback refuses a journal that names the excluded project.

The only fake is the HTTP boundary (Mesh API); it records every call so the test
can show that nothing but reads happened. Run: python3 -I test_rollback_guard.py
"""
import json
import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import layout_docs as L

PID = "11111111-1111-4111-8111-111111111111"
DID = "22222222-2222-4222-8222-222222222222"
OPID = "44444444-4444-4444-8444-444444444444"  # the old parent folder


class FakeApi:
    def __init__(self, project_slug, parent=PID, old_parent_project=PID):
        self.slug, self.parent, self.calls = project_slug, parent, []
        self.old_parent_project = old_parent_project

    def call(self, method, path, body=None):
        self.calls.append((method, path))
        if path == f"/projects/{PID}":
            return {"slug": self.slug}
        if path == f"/documents/{DID}":
            return {"project_id": PID, "slug": "spec-a", "parent_id": self.parent}
        if path == f"/documents/{OPID}":
            return {"project_id": self.old_parent_project, "slug": "specs", "parent_id": None}
        if method == "POST" and path.endswith("/documents"):
            return {"id": "66666666-6666-4666-8666-666666666666"}  # a re-created folder
        return {}

    def docs(self, pid):
        return []


def journal():
    f = tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False)
    f.write(json.dumps({"op": "move", "doc_id": DID, "project_id": PID, "project": "mesh-dev", "slug": "spec-a",
                        "old_parent_id": None, "new_parent_id": PID, "ts": "x"}) + "\n")
    f.close()
    return f.name


def journal_old_parent():
    f = tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False)
    f.write(json.dumps({"op": "move", "doc_id": DID, "project_id": PID, "project": "mesh-dev", "slug": "spec-a",
                        "old_parent_id": OPID, "new_parent_id": PID, "ts": "x"}) + "\n")
    f.close()
    return f.name


def main():
    api = FakeApi("website-relaunch")
    try:
        L.rollback(api, journal())
        print("FAIL excluded project was not refused")
        sys.exit(1)
    except L.Abort as e:
        assert "excluded" in str(e)
    assert all(m == "GET" for m, _ in api.calls), api.calls
    print("ok   rollback of the excluded project is refused, only reads were made")
    api = FakeApi("lab")  # journal says mesh-dev, the project id is lab's: refused
    try:
        L.rollback(api, journal())
        print("FAIL project name mismatch was not refused")
        sys.exit(1)
    except L.Abort as e:
        assert "live project" in str(e)
    assert all(m == "GET" for m, _ in api.calls), api.calls
    print("ok   journal naming a different project than the live one is refused, only reads were made")
    f = tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False)
    f.write(json.dumps({"op": "delete_folder", "doc_id": DID, "project_id": PID, "project": "mesh-dev", "slug": "evil",
                        "title": "x", "old_parent_id": None, "new_parent_id": None, "ts": "x"}) + "\n")
    f.close()
    api = FakeApi("mesh-dev")
    try:
        L.rollback(api, f.name)
        print("FAIL deleted-folder record with a non-legacy slug was not refused")
        sys.exit(1)
    except L.Abort as e:
        assert "legacy" in str(e)
    assert api.calls == [], api.calls
    print("ok   delete_folder record with a slug that is not a legacy folder is refused before any call")
    f = tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False)
    f.write(json.dumps({"op": "move", "doc_id": DID, "project_id": PID, "project": "mesh-dev", "slug": "state",
                        "old_parent_id": None, "new_parent_id": PID, "ts": "x"}) + "\n")
    f.close()
    api = FakeApi("mesh-dev")
    try:
        L.rollback(api, f.name)
        print("FAIL protected slug was not refused")
        sys.exit(1)
    except L.Abort as e:
        assert "protected" in str(e)
    assert api.calls == [], api.calls
    print("ok   a journal line for a protected slug (state) is refused before any call")
    api = FakeApi("mesh-dev", parent="33333333-3333-4333-8333-333333333333")  # moved by someone after the apply
    try:
        L.rollback(api, journal())
        print("FAIL a document moved after the apply was rolled back over")
        sys.exit(1)
    except L.Abort as e:
        assert "moved after" in str(e)
    assert all(m == "GET" for m, _ in api.calls), api.calls
    print("ok   a document whose live parent differs from the journal is refused, only reads were made")
    f = tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False)
    f.write("[1, 2]\n")
    f.close()
    api = FakeApi("mesh-dev")
    try:
        L.rollback(api, f.name)
        print("FAIL a non-object journal line was not refused")
        sys.exit(1)
    except L.Abort as e:
        assert "not a JSON object" in str(e)
    assert api.calls == [], api.calls
    print("ok   a journal line that is a JSON array is refused with Abort, no call made")
    api = FakeApi("mesh-dev", old_parent_project="55555555-5555-4555-8555-555555555555")  # edited-in parent of another project
    try:
        L.rollback(api, journal_old_parent())
        print("FAIL an old_parent_id from another project was not refused")
        sys.exit(1)
    except L.Abort as e:
        assert "old_parent_id" in str(e)
    assert all(m == "GET" for m, _ in api.calls), api.calls
    print("ok   an old_parent_id that is not a document of the journal's project is refused, only reads were made")
    f = tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False)  # pruned-folder record claims another project
    OTHER = "55555555-5555-4555-8555-555555555555"
    f.write(json.dumps({"op": "delete_folder", "doc_id": OPID, "project_id": OTHER, "project": "other", "slug": "adrs", "title": "adrs", "ts": "x"}) + "\n")
    f.write(json.dumps({"op": "move", "doc_id": DID, "project_id": PID, "project": "mesh-dev", "slug": "spec-a",
                        "old_parent_id": OPID, "new_parent_id": PID, "ts": "x"}) + "\n")
    f.close()
    class TwoProjects(FakeApi):
        def call(self, method, path, body=None):
            if path == f"/projects/{OTHER}":
                self.calls.append((method, path))
                return {"slug": "other"}
            return super().call(method, path, body)
    api = TwoProjects("mesh-dev")
    try:
        L.rollback(api, f.name)
        print("FAIL a pruned old_parent_id of another project was not refused")
        sys.exit(1)
    except L.Abort as e:
        assert "pruned folder of another project" in str(e), e
    assert all(m == "GET" for m, _ in api.calls), api.calls
    print("ok   an old_parent_id that is a pruned folder of another project is refused, only reads were made")
    f2 = tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False)  # positive control: pruned folder of the same project
    f2.write(json.dumps({"op": "delete_folder", "doc_id": OPID, "project_id": PID, "project": "mesh-dev", "slug": "adrs", "title": "adrs", "ts": "x"}) + "\n")
    f2.write(json.dumps({"op": "move", "doc_id": DID, "project_id": PID, "project": "mesh-dev", "slug": "spec-a",
                        "old_parent_id": OPID, "new_parent_id": PID, "ts": "x"}) + "\n")
    f2.close()
    api = FakeApi("mesh-dev")
    L.rollback(api, f2.name)
    assert ("PATCH", f"/documents/{DID}") in api.calls
    print("ok   positive control: a pruned folder of the same project is re-created and rolled back to")
    api = FakeApi("mesh-dev")  # positive control: the old parent is a folder of the same project
    L.rollback(api, journal_old_parent())
    assert ("PATCH", f"/documents/{DID}") in api.calls
    print("ok   positive control: an old parent in the same project is rolled back to")
    api = FakeApi("mesh-dev")  # positive control: the same journal on an ordinary project goes through
    L.rollback(api, journal())
    assert ("PATCH", f"/documents/{DID}") in api.calls
    print("ok   positive control: an ordinary project is rolled back")


if __name__ == "__main__":
    main()
