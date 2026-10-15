#!/usr/bin/env python3
"""Lay out root-level Mesh documents into the fleet's canon folders.

Input is the layout table (a Mesh Doc or a local markdown file): one
"### <project-slug> (N)" section per project, one row per root document
`| id8 | slug | title | date | author | folder |`.

Modes
  (default) / --dry-run   read only; prints what -> where and root counts before/after
  --apply --project SLUG  moves documents of ONE project, writes a rollback journal;
                          --prune-merged also soft-deletes emptied legacy folders
  --rollback JOURNAL      puts every moved document back (and removes folders this
                          run created, if they are empty again) or restores renamed slugs
  --rename-slugs          (with the modes above, no table needed) rename slugs that the
                          layout generator mangled (adrs001-x -> adr-001-x); the server keeps
                          the old path as an alias. Dry-run prints "was -> becomes"; see README

Auth comes from the environment only and is never printed:
  MESH_API_URL  + MESH_AGENT_KEY   (X-Agent-Key)   or   MESH_BEARER_TOKEN
Optional MESH_WORKSPACE_ID narrows project lookup to one workspace.

Everything is validated before the first write: an unknown folder label, an id
that resolves to nothing / to two documents, a slug that does not match the
document the id points at, or two documents that would collide on a slug in the
same target folder aborts the run with a non-zero exit and changes nothing.
Any HTTP 409 during apply stops the run at once; it is never retried or skipped.
"""
import argparse
import json
import os
import re
import sys
import unicodedata
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone

# table label -> canon folder slug. None = do not move.
SLUG_CUT = 45  # the layout table cuts slugs at this length

LABEL_TO_FOLDER = {
    "specs": "specs", "prd": "specs",
    "adrs": "decisions", "decisions": "decisions",
    "plans": "plans",
    "research": "research", "rnd": "research",
    "reports": "reports",
    "runbooks": "guides", "guides": "guides",
    "e2e": "e2e",
    "registry": "registry",
    "archive": "archive",
    "scratch-delete": None,   # soft delete is a separate, link-checked step
    "?": "archive",           # lead decides; no answer = archive (parent card)
}
CANON_TITLES = {
    "specs": "Требования и спецификации", "decisions": "Решения", "plans": "Планы",
    "research": "Исследования", "reports": "Отчёты и аудиты", "guides": "Инструкции",
    "e2e": "Сценарии проверки", "registry": "Реестры", "archive": "Архив",
}
# folder slugs merged into a canon folder: legacy slug -> canon slug
LEGACY_FOLDERS = {"adrs": "decisions", "rnd": "research", "runbooks": "guides", "prd": "specs"}
PROTECTED_SLUGS = {"state", "product-state", "obsidian-share"}
CANON = set(CANON_TITLES)
# never touched, even when visible: demo workspace of the portal reviewer
EXCLUDED_PROJECTS = {"website-relaunch"}

ROW = re.compile(r"^\|\s*([0-9a-f]{8})\s*\|\s*([^|]*?)\s*\|\s*(.*?)\s*\|\s*[^|]*\|\s*[^|]*\|\s*([^|]+?)\s*\|\s*$")
HEAD = re.compile(r"^###\s+([\w.-]+)\s+\((\d+)\)\s*$")


class Abort(Exception):
    pass


def parse_table(text):
    """-> {project_slug: [ {id8, slug, title, label} ]}; checks the (N) counts."""
    out, cur, want = {}, None, {}
    for line in text.splitlines():
        m = HEAD.match(line)
        if m:
            cur = m.group(1)
            out[cur] = []
            want[cur] = int(m.group(2))
            continue
        m = ROW.match(line)
        if m and cur and m.group(1) != "id":
            out[cur].append({"id8": m.group(1), "slug": m.group(2), "title": m.group(3), "label": m.group(4)})
    for p, n in want.items():
        if len(out[p]) != n:
            raise Abort(f"table: project {p} declares {n} rows, parsed {len(out[p])}")
    return out


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """The key travels in a header; a redirect would hand it to whatever host answers."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise Abort(f"HTTP {code} redirect refused: the API key is never sent to another URL")


_OPENER = urllib.request.build_opener(_NoRedirect)


class Mesh:
    def __init__(self):
        base = os.environ.get("MESH_API_URL", "").rstrip("/")
        if not base:
            raise Abort("MESH_API_URL is not set")
        host = urllib.parse.urlparse(base).hostname or ""
        if not (base.startswith("https://") or (base.startswith("http://") and host in ("localhost", "127.0.0.1", "::1"))):
            raise Abort("MESH_API_URL must be https (plain http only for localhost): the key is sent on every request")
        self.base = base if base.endswith("/api/v1") else base + "/api/v1"
        key, tok = os.environ.get("MESH_AGENT_KEY"), os.environ.get("MESH_BEARER_TOKEN")
        if key:
            self.hdr = {"X-Agent-Key": key}
        elif tok:
            self.hdr = {"Authorization": "Bearer " + tok}
        else:
            raise Abort("set MESH_AGENT_KEY or MESH_BEARER_TOKEN")

    def call(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={**self.hdr, "Content-Type": "application/json"})
        try:
            # base URL is checked to be http(s) in __init__, so no file:// scheme can reach here
            with _OPENER.open(req, timeout=60) as r:  # nosemgrep: python.lang.security.audit.dynamic-urllib-use-detected.dynamic-urllib-use-detected
                raw = r.read()
                return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as e:
            detail = e.read()[:300].decode("utf-8", "replace")
            raise Abort(f"HTTP {e.code} on {method} {path}: {detail}")

    def projects(self):
        ws = os.environ.get("MESH_WORKSPACE_ID")
        if not ws:
            raise Abort("MESH_WORKSPACE_ID is not set")
        res, page = [], 1
        while True:
            d = self.call("GET", f"/workspaces/{ws}/projects?page={page}&page_size=100")
            res += d["items"]
            if not d.get("has_more"):
                return res
            page += 1

    def docs(self, project_id):
        res, page = [], 1
        while True:
            d = self.call("GET", f"/projects/{project_id}/documents?page={page}&page_size=100")
            res += d["items"]
            if not d.get("has_more"):
                return res
            page += 1


def plan_project(slug, rows, docs, unknown_policy="archive"):
    """Pure function: validate rows against live docs and compute the operations.

    Returns dict with ops (create_folder / move / delete_folder), skipped list and
    root counts before/after. Raises Abort on any inconsistency.
    """
    by_id = {d["id"]: d for d in docs}
    children = {}
    for d in docs:
        children.setdefault(d["parent_id"], []).append(d)
    roots = children.get(None, [])
    errors = []

    folder_of = {}  # canon slug -> existing root doc (folder)
    legacy_docs = {}
    for d in roots:
        if d["slug"] in CANON:
            folder_of[d["slug"]] = d
        elif d["slug"] in LEGACY_FOLDERS and d["slug"] not in PROTECTED_SLUGS:
            legacy_docs[d["slug"]] = d

    planned = []  # (doc, target_canon_slug)
    skipped = []
    seen_ids = set()
    for r in rows:
        label = r["label"]
        if not r["slug"]:
            errors.append(f"{slug}/{r['id8']}: empty table slug")
            continue
        if r["id8"] in seen_ids:
            errors.append(f"{slug}/{r['id8']} {r['slug']}: document listed more than once")
            continue
        seen_ids.add(r["id8"])
        if label not in LABEL_TO_FOLDER:
            errors.append(f"{slug}/{r['id8']} {r['slug']}: unknown folder label {label!r}")
            continue
        match = [d for d in docs if d["id"].startswith(r["id8"])]
        if len(match) != 1:
            errors.append(f"{slug}/{r['id8']} {r['slug']}: id resolves to {len(match)} documents")
            continue
        d = match[0]
        # the table cuts long slugs at 45 chars, so a prefix is a match
        if not (d["slug"] == r["slug"] or (len(r["slug"]) == SLUG_CUT and d["slug"].startswith(r["slug"]))):
            errors.append(f"{slug}/{r['id8']}: table slug {r['slug']!r} != live slug {d['slug']!r}")
            continue
        target = LABEL_TO_FOLDER[label]
        if label == "?" and unknown_policy == "skip":
            target = None
        if d["slug"] in PROTECTED_SLUGS:
            skipped.append((d, "state / protected slug"))
        elif d["parent_id"] is not None:
            skipped.append((d, "not a root document (already placed)"))
        elif children.get(d["id"]):
            skipped.append((d, "program root with children"))
        elif target is None:
            skipped.append((d, "scratch-delete: separate step" if label == "scratch-delete" else "left in root"))
        elif d["slug"] in CANON or d["slug"] in LEGACY_FOLDERS:
            skipped.append((d, "is itself a canon/legacy folder"))
        else:
            planned.append((d, target))

    # legacy folder children -> canon
    merges = []  # (legacy_doc, [children], canon)
    for lslug, ld in legacy_docs.items():
        kids = children.get(ld["id"], [])
        merges.append((ld, kids, LEGACY_FOLDERS[lslug]))

    # collisions: slug uniqueness within the target folder
    taken = {}  # canon -> {slug: doc id}
    for cslug, fd in folder_of.items():
        taken[cslug] = {k["slug"]: k["id"] for k in children.get(fd["id"], [])}
    for d, t in planned + [(k, c) for _, kids, c in merges for k in kids]:
        bucket = taken.setdefault(t, {})
        if d["slug"] in bucket and bucket[d["slug"]] != d["id"]:
            errors.append(f"{slug}: slug {d['slug']!r} would collide in {t}/ (docs {d['id'][:8]} and {bucket[d['slug']][:8]})")
        bucket[d["slug"]] = d["id"]

    if errors:
        raise Abort("\n".join(errors))

    ops = []
    needed = {t for _, t in planned} | {c for _, kids, c in merges if kids}
    for t in sorted(needed):
        if t not in folder_of:
            ops.append({"op": "create_folder", "slug": t, "title": CANON_TITLES[t]})
    for d, t in planned:
        ops.append({"op": "move", "doc_id": d["id"], "slug": d["slug"], "title": d["title"],
                    "old_parent_id": d["parent_id"], "target": t})
    for ld, kids, c in merges:
        for k in kids:
            ops.append({"op": "move", "doc_id": k["id"], "slug": k["slug"], "title": k["title"],
                        "old_parent_id": ld["id"], "target": c, "merged_from": ld["slug"]})
        ops.append({"op": "delete_folder", "doc_id": ld["id"], "slug": ld["slug"]})

    moved_from_root = sum(1 for o in ops if o["op"] == "move" and o["old_parent_id"] is None)
    deleted = sum(1 for o in ops if o["op"] == "delete_folder")
    created = sum(1 for o in ops if o["op"] == "create_folder")
    after = len(roots) - moved_from_root + created
    return {"ops": ops, "skipped": skipped,
            "root_before": len(roots),
            "root_after": after,                 # legacy folders kept (default apply)
            "root_after_pruned": after - deleted}  # lower bound with --prune-merged: apply keeps a legacy folder whose page has text


def render_plan(slug, plan):
    L = [f"### {slug}: корень {plan['root_before']} → {plan['root_after']} (с --prune-merged не ниже {plan['root_after_pruned']}: легаси-папка со своим текстом остаётся)"]
    L.append("| операция | slug | куда | откуда |\n|---|---|---|---|")
    for o in plan["ops"]:
        if o["op"] == "create_folder":
            L.append(f"| создать папку | {o['slug']} | корень | |")
        elif o["op"] == "move":
            L.append(f"| перенос {o['doc_id'][:8]} | {o['slug']} | {o['target']}/ | {o.get('merged_from') or 'корень'} |")
        else:
            L.append(f"| удалить пустую (мягко) | {o['slug']} | | слияние |")
    for d, why in plan["skipped"]:
        L.append(f"| пропуск {d['id'][:8]} | {d['slug']} | | {why} |")
    return "\n".join(L)


def now():
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def journal_write(fh, rec):
    fh.write(json.dumps(rec, ensure_ascii=False) + "\n")
    fh.flush()
    os.fsync(fh.fileno())


# ---- --rename-slugs --------------------------------------------------------
# Server refuses these as the old or the new slug of a rename (document_service.go
# renameProtectedSlugs); a candidate that would hit one is skipped, never sent.
RENAME_RESERVED = PROTECTED_SLUGS | CANON | set(LEGACY_FOLDERS)
# canon folder that confirms a merged-prefix slug really is one of that kind
PREFIX_RULES = [
    # (regex on the slug, replacement prefix, expected parent canon folder, label)
    (re.compile(r"^adrs(\d+)(.*)$"), "adr-", "decisions", "adrs<NNN>"),
    (re.compile(r"^adrs(?!-)(.+)$"), "adr-", "decisions", "adrs<name>"),
    (re.compile(r"^runbooks(?!-)(.+)$"), "runbook-", "guides", "runbooks<name>"),
    (re.compile(r"^(?:раннбук|ранбук)-?(.+)$"), "runbook-", "guides", "раннбук"),
    (re.compile(r"^prd(?!-)(.+)$"), "prd-", "specs", "prd<name>"),
    (re.compile(r"^rnd(?!-)(.+)$"), "rnd-", "research", "rnd<name>"),
]


def slugify(text):
    """Python twin of pkg/mdoc.Slugify: the only slug form the server accepts on rename."""
    out = []
    for ch in text.lower():
        cat = unicodedata.category(ch)
        if cat.startswith("L") or cat == "Nd" or ch in "-_":
            out.append(ch)
        elif ch.isspace():
            out.append("-")
    return "".join(out).strip("-")


def has_letter_or_digit(text):
    return any(unicodedata.category(c).startswith("L") or unicodedata.category(c) == "Nd" for c in text)


def propose_slug(doc, parent_slug):
    """-> (new_slug | None, rule, note). None = not a candidate or not renamable."""
    old = doc["slug"]
    if old in RENAME_RESERVED:
        return None, "", ""
    if not has_letter_or_digit(old):
        new = slugify(doc.get("title") or "")
        if not new:
            return None, "only hyphens", "title gives no slug: left as is"
        return new, "only hyphens", "irreversible"
    for rx, prefix, folder, label in PREFIX_RULES:
        m = rx.match(old)
        if not m:
            continue
        if parent_slug != folder:
            return None, label, f"parent is {parent_slug or 'root'}, not {folder}/: left as is"
        new = slugify(prefix + (m.group(1) + m.group(2) if label == "adrs<NNN>" else m.group(1)))
        if slugify(new) != new or not has_letter_or_digit(new) or new in RENAME_RESERVED:
            return None, label, f"{new!r} is not a valid slug: left as is"
        return new, label, ""
    return None, "", ""


def plan_renames(slug, docs):
    """Pure function: -> {"ops": [...], "skipped": [...]}; Abort on any collision."""
    by_id = {d["id"]: d for d in docs}
    ops, skipped, errors = [], [], []
    taken = {}  # parent_id -> {slug: doc id}
    for d in docs:
        taken.setdefault(d["parent_id"], {})[d["slug"]] = d["id"]
    for d in docs:
        parent = by_id.get(d["parent_id"])
        new, rule, note = propose_slug(d, parent["slug"] if parent else None)
        if new is None:
            if note:
                skipped.append((d, rule, note))
            continue
        if new == d["slug"]:
            continue
        bucket = taken[d["parent_id"]]
        if new in bucket and bucket[new] != d["id"]:
            errors.append(f"{slug}: {d['slug']!r} -> {new!r} collides with document {bucket[new][:8]} in the same folder")
            continue
        del bucket[d["slug"]]
        bucket[new] = d["id"]
        ops.append({"op": "rename", "doc_id": d["id"], "title": d["title"], "old_slug": d["slug"],
                    "new_slug": new, "rule": rule, "irreversible": note == "irreversible",
                    "parent_slug": parent["slug"] if parent else None})
    if errors:
        raise Abort("\n".join(errors))
    return {"ops": ops, "skipped": skipped}


def render_renames(slug, plan):
    L = [f"### {slug}: кандидатов на переименование {len(plan['ops'])}"]
    L.append("| doc | было | стало | правило |\n|---|---|---|---|")
    for o in plan["ops"]:
        flag = " (необратимо: откат не вернёт слаг)" if o["irreversible"] else ""
        L.append(f"| {o['doc_id'][:8]} | {o['old_slug']} | {o['new_slug']} | {o['rule']}{flag} |")
    for d, rule, why in plan["skipped"]:
        L.append(f"| пропуск {d['id'][:8]} | {d['slug']} | | {rule}: {why} |")
    return "\n".join(L)


def apply_renames(api, project, plan, journal_path, allow_irreversible=False):
    with open(journal_path, "a", encoding="utf-8") as fh:
        for o in plan["ops"]:
            if o["irreversible"] and not allow_irreversible:
                print(f"skip {o['doc_id'][:8]} {o['old_slug']!r}: rollback could not restore it (use --allow-irreversible)", file=sys.stderr)
                continue
            d = api.call("PATCH", f"/documents/{o['doc_id']}", {"slug": o["new_slug"]})
            if d.get("slug") != o["new_slug"]:  # a server without slug edits answers 200 and ignores the field
                raise Abort(f"{o['doc_id']}: server answered slug {d.get('slug')!r}, expected {o['new_slug']!r}; "
                            f"does this server support slug rename? stopped")
            journal_write(fh, {"op": "rename", "project": project["slug"], "project_id": project["id"], "doc_id": o["doc_id"],
                               "slug": o["new_slug"], "old_slug": o["old_slug"], "new_slug": o["new_slug"],
                               "irreversible": o["irreversible"], "ts": now()})


def apply_project(api, project, docs, plan, journal_path, prune=False):
    pid = project["id"]
    root_folders = {d["slug"]: d for d in docs if d["parent_id"] is None and d["slug"] in CANON}
    with open(journal_path, "a", encoding="utf-8") as fh:
        for o in plan["ops"]:
            if o["op"] == "create_folder":
                if o["slug"] in root_folders:
                    continue
                d = api.call("POST", f"/projects/{pid}/documents", {"title": o["title"], "slug": o["slug"], "body": ""})
                root_folders[o["slug"]] = d
                journal_write(fh, {"op": "create_folder", "project": project["slug"], "project_id": pid, "doc_id": d["id"], "slug": o["slug"],
                                   "old_parent_id": None, "new_parent_id": None, "ts": now()})
            elif o["op"] == "move":
                new_parent = root_folders[o["target"]]["id"]
                if o["old_parent_id"] == new_parent:
                    continue  # idempotent: already there
                api.call("PATCH", f"/documents/{o['doc_id']}", {"parent_id": new_parent})
                journal_write(fh, {"op": "move", "project": project["slug"], "project_id": pid, "doc_id": o["doc_id"], "slug": o["slug"],
                                   "old_parent_id": o["old_parent_id"], "new_parent_id": new_parent, "ts": now()})
            else:  # delete_folder: opt-in, and only if the folder is really empty now
                if not prune:
                    continue
                left = [d for d in api.docs(pid) if d["parent_id"] == o["doc_id"]]
                if left:
                    print(f"keep {o['slug']}/: still has {len(left)} children", file=sys.stderr)
                    continue
                full = api.call("GET", f"/documents/{o['doc_id']}")
                if (full.get("body") or "").strip():
                    print(f"keep {o['slug']}/: the folder page has its own text", file=sys.stderr)
                    continue
                api.call("DELETE", f"/documents/{o['doc_id']}")
                journal_write(fh, {"op": "delete_folder", "project": project["slug"], "doc_id": o["doc_id"], "slug": o["slug"],
                                   "title": full.get("title", o["slug"]), "project_id": pid,
                                   "old_parent_id": None, "new_parent_id": None, "ts": now()})


UUID_RE = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")


def read_journal(path):
    """Parse and shape-check the journal; nothing is written before it passes."""
    recs = []
    for n, line in enumerate(open(path, encoding="utf-8"), 1):
        if not line.strip():
            continue
        try:
            r = json.loads(line)
        except ValueError:
            raise Abort(f"journal line {n}: not JSON")
        if not isinstance(r, dict):
            raise Abort(f"journal line {n}: not a JSON object")
        if r.get("op") not in ("create_folder", "move", "delete_folder", "rename"):
            raise Abort(f"journal line {n}: unknown op {r.get('op')!r}")
        for k in ("doc_id", "project_id"):
            if not isinstance(r.get(k), str) or not UUID_RE.match(r[k]):
                raise Abort(f"journal line {n}: {k} is not a UUID")
        if not isinstance(r.get("slug"), str):
            raise Abort(f"journal line {n}: slug missing")
        if r["slug"] in PROTECTED_SLUGS:
            raise Abort(f"journal line {n}: {r['slug']!r} is protected and is never part of a layout journal")
        if r["op"] == "delete_folder" and r["slug"] not in LEGACY_FOLDERS:
            raise Abort(f"journal line {n}: delete_folder of {r['slug']!r}, only legacy folders are ever deleted")
        if r["op"] == "create_folder" and r["slug"] not in CANON:
            raise Abort(f"journal line {n}: create_folder of {r['slug']!r}, only canon folders are ever created")
        if r["op"] == "rename":
            old, new = r.get("old_slug"), r.get("new_slug")
            if not (isinstance(old, str) and isinstance(new, str)) or r["slug"] != new:
                raise Abort(f"journal line {n}: rename needs old_slug/new_slug strings and slug == new_slug")
            if old in RENAME_RESERVED or new in RENAME_RESERVED:
                raise Abort(f"journal line {n}: rename touches a reserved slug ({old!r} -> {new!r})")
            if slugify(new) != new or not has_letter_or_digit(new):
                raise Abort(f"journal line {n}: new_slug {new!r} is not a slug")
            if not r.get("irreversible") and (slugify(old) != old or not has_letter_or_digit(old)):
                raise Abort(f"journal line {n}: old_slug {old!r} is not a slug and the record is not marked irreversible")
            if r.get("irreversible") and has_letter_or_digit(old):
                raise Abort(f"journal line {n}: irreversible is only for an old_slug without a letter or digit, got {old!r}")
        if r["op"] == "delete_folder" and len(str(r.get("title") or "")) > 200:
            raise Abort(f"journal line {n}: title too long")
        for k in ("old_parent_id", "new_parent_id"):
            if r.get(k) is not None and not (isinstance(r[k], str) and UUID_RE.match(r[k])):
                raise Abort(f"journal line {n}: {k} is not a UUID")
        recs.append(r)
    return recs


def own_doc(api, r):
    """The journal names a document; the live document must be that one (project and slug)."""
    doc = api.call("GET", f"/documents/{r['doc_id']}")
    if doc["project_id"] != r["project_id"] or doc["slug"] != r["slug"]:
        raise Abort(f"journal record for {r['doc_id']} does not match the live document "
                    f"(project/slug {doc['project_id']}/{doc['slug']}); rollback refused")
    return doc


def rollback(api, journal_path):
    recs = read_journal(journal_path)
    live_slug = {}
    for pid in {r["project_id"] for r in recs}:
        live_slug[pid] = api.call("GET", f"/projects/{pid}").get("slug")
        if live_slug[pid] in EXCLUDED_PROJECTS:
            raise Abort(f"journal touches excluded project {live_slug[pid]}; rollback refused")
    for r in recs:  # every record, deletes included, must name the project it really belongs to
        if r.get("project") != live_slug[r["project_id"]]:
            raise Abort(f"journal record for {r['doc_id']} says project {r.get('project')!r}, "
                        f"live project is {live_slug[r['project_id']]!r}; rollback refused")
    last_move = {}
    for r in recs:
        if r["op"] == "move":
            last_move[r["doc_id"]] = r
    for r in recs:  # check ownership of every target (created folders too) before the first write
        if r["op"] == "delete_folder":
            continue
        doc = own_doc(api, r)
        if r["op"] == "move" and last_move[r["doc_id"]] is r and doc.get("parent_id") != r["new_parent_id"]:
            raise Abort(f"document {r['doc_id']} was moved after the journal was written "
                        f"(parent {doc.get('parent_id')}, journal says {r['new_parent_id']}); rollback refused")
    renames = [r for r in recs if r["op"] == "rename"]
    if renames:
        by_project = {}
        for pid in {r["project_id"] for r in renames}:
            by_project[pid] = api.docs(pid)
        seen = {}
        for r in renames:  # the old slug must still be free in the document's folder, else the server answers 409 mid-way
            live = next((d for d in by_project[r["project_id"]] if d["id"] == r["doc_id"]), None)
            if live is None:
                raise Abort(f"document {r['doc_id']} is not in project {r['project']!r}; rollback refused")
            parent = next((d for d in by_project[r["project_id"]] if d["id"] == live["parent_id"]), None)
            derived = propose_slug({"slug": r["old_slug"], "title": live.get("title")}, parent["slug"] if parent else None)[0]
            if derived != r["new_slug"]:  # the journal is untrusted: old_slug must be what the rename rule maps to new_slug
                raise Abort(f"journal record for {r['doc_id'][:8]}: old_slug {r['old_slug']!r} does not rename to {r['new_slug']!r} by the rule; rollback refused")
            if r.get("irreversible"):  # rule-checked above; its old slug has no letter, so rollback leaves it as is
                continue
            for d in by_project[r["project_id"]]:
                if d["parent_id"] == live["parent_id"] and d["slug"] == r["old_slug"] and d["id"] != r["doc_id"]:
                    raise Abort(f"cannot restore {r['old_slug']!r}: document {d['id'][:8]} now has it in the same folder; rollback refused")
            if (r["project_id"], live["parent_id"], r["old_slug"]) in seen and seen[(r["project_id"], live["parent_id"], r["old_slug"])] != r["doc_id"]:
                raise Abort(f"journal restores {r['old_slug']!r} to two documents of one folder; rollback refused")
            seen[(r["project_id"], live["parent_id"], r["old_slug"])] = r["doc_id"]
    pruned = {r["doc_id"]: r["project_id"] for r in recs if r["op"] == "delete_folder"}  # re-created on rollback
    for r in recs:  # the destination must be a real folder of the same project, never an edited-in id
        old = r.get("old_parent_id")
        if r["op"] != "move" or old is None:
            continue
        if old in pruned:  # soft-deleted, so not readable: it is replaced by a fresh folder, but only in its own project
            if pruned[old] != r["project_id"]:
                raise Abort(f"journal record for {r['doc_id']}: old_parent_id {old} is a pruned folder of "
                            f"another project; rollback refused")
            continue
        parent = api.call("GET", f"/documents/{old}")
        if parent.get("project_id") != r["project_id"]:
            raise Abort(f"journal record for {r['doc_id']}: old_parent_id {old} is not a document of "
                        f"project {r['project']!r}; rollback refused")
    restored = 0
    for r in reversed(renames):
        if r.get("irreversible"):
            print(f"left {r['doc_id'][:8]} as {r['new_slug']!r}: its old slug {r['old_slug']!r} is not a valid slug", file=sys.stderr)
            continue
        api.call("PATCH", f"/documents/{r['doc_id']}", {"slug": r["old_slug"]})
        restored += 1
    remap = {}  # id of a pruned legacy folder -> id of its re-created twin
    for r in reversed(recs):
        if r["op"] == "delete_folder":
            d = api.call("POST", f"/projects/{r['project_id']}/documents",
                         {"title": r.get("title") or r["slug"], "slug": r["slug"], "body": ""})
            remap[r["doc_id"]] = d["id"]
    for r in reversed(recs):
        if r["op"] == "move":
            old = remap.get(r["old_parent_id"], r["old_parent_id"])
            body = {"parent_id": old} if old else {"clear_parent": True}
            api.call("PATCH", f"/documents/{r['doc_id']}", body)
    for r in reversed(recs):
        if r["op"] == "create_folder":
            doc = own_doc(api, r)
            kids = [d for d in api.docs(doc["project_id"]) if d["parent_id"] == r["doc_id"]]
            if kids:
                print(f"keep {r['slug']}/: {len(kids)} documents were added to it after the apply", file=sys.stderr)
            else:
                api.call("DELETE", f"/documents/{r['doc_id']}")
    print(f"rolled back {sum(1 for r in recs if r['op']=='move')} moves, {restored} renames")
    if remap:
        print(f"re-created {len(remap)} pruned legacy folder(s); their ids are new")


def load_table(args, api):
    if args.table:
        text = open(args.table, encoding="utf-8").read()
    else:
        d = api.call("GET", f"/documents/{args.table_doc}")
        text = d["body"]
    return parse_table(text)


def run_rename(api, a):
    if a.apply and a.project in EXCLUDED_PROJECTS:
        raise Abort(f"project {a.project} is excluded and is never touched")
    projects = {p["slug"]: p for p in api.projects() if p["slug"] not in EXCLUDED_PROJECTS}
    if a.apply and a.project not in projects:
        raise Abort(f"project {a.project} is not visible to this key")
    if a.project and a.project not in projects:
        raise Abort(f"project {a.project} is not visible to this key")
    selected = [a.project] if a.project else sorted(projects)
    plans, errors = {}, []
    for slug in selected:
        try:
            plans[slug] = plan_renames(slug, api.docs(projects[slug]["id"]))
        except Abort as e:
            errors.append(str(e))
    if errors:
        raise Abort("\n".join(errors))
    total = sum(len(p["ops"]) for p in plans.values())
    out = [f"# Dry-run переименования слагов: {len(plans)} проектов, кандидатов {total}\n"]
    out += [render_renames(slug, p) + "\n" for slug, p in plans.items() if p["ops"] or p["skipped"]]
    out.append(f"**Итого кандидатов: {total}**")
    text = "\n".join(out)
    if a.report:
        open(a.report, "w", encoding="utf-8").write(text + "\n")
    print(text)
    if a.apply:
        path = a.journal or f"layout-rename-journal-{a.project}-{datetime.now(timezone.utc):%Y%m%dT%H%M%SZ}.jsonl"
        apply_renames(api, projects[a.project], plans[a.project], path, a.allow_irreversible)
        print(f"applied; journal: {path}")
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--table", help="local markdown file with the layout table")
    ap.add_argument("--table-doc", default="16a66f73-a30e-4308-bdfd-a02f222f7d6a", help="Mesh doc id of the table")
    ap.add_argument("--dry-run", action="store_true", help="default; read only")
    ap.add_argument("--apply", action="store_true")
    ap.add_argument("--project", help="project slug (required with --apply)")
    ap.add_argument("--prune-merged", action="store_true",
                    help="with --apply: also soft-delete merged legacy folders (adrs/rnd/runbooks/prd) once empty")
    ap.add_argument("--rollback", metavar="JOURNAL")
    ap.add_argument("--journal", help="journal path for --apply (default: layout-journal-<project>-<ts>.jsonl)")
    ap.add_argument("--expect-total", type=int, help="fail unless the table has exactly this many rows")
    ap.add_argument("--unknown-policy", choices=["archive", "skip"], default="archive",
                    help="what to do with rows labelled '?': archive (default) or leave in root")
    ap.add_argument("--allow-invisible", action="store_true",
                    help="dry-run only: a project this key cannot see is reported, not fatal")
    ap.add_argument("--report", help="write the dry-run report (markdown) to this file")
    ap.add_argument("--rename-slugs", action="store_true",
                    help="rename slugs mangled by the layout (adrs001-x -> adr-001-x); dry-run by default, --apply needs --project")
    ap.add_argument("--allow-irreversible", action="store_true",
                    help="with --rename-slugs --apply: also rename hyphen-only slugs (--rollback cannot restore those)")
    a = ap.parse_args(argv)
    if a.apply and a.rollback:
        ap.error("--apply and --rollback are exclusive")
    if a.apply and not a.project:
        ap.error("--apply needs --project <slug>")
    try:
        api = Mesh()
        if a.rollback:
            rollback(api, a.rollback)
            return 0
        if a.rename_slugs:
            return run_rename(api, a)
        table = load_table(a, api)
        total = sum(len(v) for v in table.values())
        if a.expect_total is not None and total != a.expect_total:
            raise Abort(f"table has {total} rows, expected {a.expect_total}")
        if a.apply and a.project in EXCLUDED_PROJECTS:
            raise Abort(f"project {a.project} is excluded and is never touched")
        excluded = [p for p in table if p in EXCLUDED_PROJECTS]
        for p in excluded:
            del table[p]
        projects = {p["slug"]: p for p in api.projects()}
        selected = [a.project] if a.apply else list(table)
        if a.apply and a.project not in table:
            raise Abort(f"project {a.project} is not in the table")
        plans, errors, invisible = {}, [], []
        for slug in selected:
            if slug not in projects:
                if a.allow_invisible and not a.apply:
                    invisible.append(slug)
                else:
                    errors.append(f"{slug}: project not visible to this key")
                continue
            docs = api.docs(projects[slug]["id"])
            try:
                plans[slug] = (docs, plan_project(slug, table[slug], docs, a.unknown_policy))
            except Abort as e:
                errors.append(str(e))
        if errors:
            raise Abort("\n".join(errors))
        out = [f"# Dry-run раскладки: {len(plans)} проектов, {sum(len(v) for v in table.values()) if not a.apply else len(table[a.project])} строк таблицы\n"]
        sb = sa = 0
        for slug, (docs, plan) in plans.items():
            out.append(render_plan(slug, plan) + "\n")
            sb += plan["root_before"]
            sa += plan["root_after_pruned"]
        out.append(f"**Итого корень: {sb} → {sa}** (с --prune-merged: не ниже этой оценки)")
        if excluded:
            out.append("\nИСКЛЮЧЕНО (не трогаем): " + ", ".join(excluded))
        if invisible:
            out.append("\nНЕ ПРОВЕРЕНО (проект не виден ключу): " + ", ".join(
                f"{p} ({len(table[p])})" for p in invisible))
        text = "\n".join(out)
        if a.report:
            open(a.report, "w", encoding="utf-8").write(text + "\n")
        print(text)
        if a.apply:
            slug = a.project
            path = a.journal or f"layout-journal-{slug}-{datetime.now(timezone.utc):%Y%m%dT%H%M%SZ}.jsonl"
            docs, plan = plans[slug]
            apply_project(api, projects[slug], docs, plan, path, a.prune_merged)
            print(f"applied; journal: {path}")
        return 0
    except Abort as e:
        print(f"ABORT: {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
