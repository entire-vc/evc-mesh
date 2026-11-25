#!/usr/bin/env python3
"""The per-area split of the pipeline definition: `.gitlab-ci.yml` + `.gitlab/ci/*.yml`.

    python3 scripts/ci/test_ci_config.py        (self-test with red controls first, then the repo)

Job definitions live in area files included from the root, so an edit to one area
starts only that area's jobs. The split is only worth something while these hold:

  1. every `.gitlab/ci/**/*.yml` on disk is included (an orphan file defines nothing);
  2. no top-level key is defined in two files (GitLab deep-merges includes silently);
  3. the root defines no job: an edit to it is meant to start every area;
  4. a job whose `rules:changes` lists `.gitlab-ci.yml` also lists its own area file
     and the file of every job/template it `extends`, so that edits to its definition
     still start it;
  5. it lists no other area file than its own area's dependencies (else the edit of a
     foreign area would start it again: the cost this split removes);
  6. an edit to the memory scope data file starts only the memory gate (and ci-config);
  7. a change-scoped job that names neither the root nor its own file is one of
     PATH_SCOPED: jobs whose lists never named `.gitlab-ci.yml` before the split, so a
     CI edit never started them. The split keeps that; a new job must choose.

Then it prints, per CI file, how many change-scoped jobs an edit to that file creates.
"""
from __future__ import annotations

import sys
import tempfile
from pathlib import Path

import yaml

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
sys.path.insert(0, str(HERE))
import ci_config  # noqa: E402
from go_affected_packages import matches  # noqa: E402  (GitLab globs: ** crosses '/', * does not)

ROOT_FILE = ci_config.ROOT_FILE
MEMORY_DATA = '.gitlab/ci/data/memory-scope.yml'
MEMORY_GATE = 'recall-gate-branch'
# Reads the whole definition on purpose, so it lists every CI file.
WHOLE_CONFIG_JOBS = {'ci-config'}
RESERVED = {'include', 'variables', 'workflow', 'stages', 'default'}
# Change-scoped on their own paths only, as on main before the split: a CI edit
# does not start them. Adding a job here is a decision, not a fix for a red run.
PATH_SCOPED = {
    'bin-retention', 'extension-authorship-red-green', 'keep-metadata-404-red-green',
    'web-builder-stage', 'perf-bundle-docker', 'project-tabs-overflow',
    'authed-e2e', 'ext-diag-e2e', 'themes-e2e', 'dashboard-widgets-e2e',
    'executions-e2e', 'memory-widget-e2e', 'authed-e2e-docs-keys-stress',
}


class Ref(list):
    pass


class Loader(yaml.SafeLoader):
    pass


Loader.add_constructor('!reference', lambda loader, node: Ref(loader.construct_sequence(node)))


def model(repo: Path):
    """(merged config, {key: defining file}) with paths relative to the repo."""
    owner = {}
    for path in ci_config.files(repo):
        for key in yaml.load(path.read_text(encoding='utf-8'), Loader=Loader) or {}:
            if key not in RESERVED:
                owner[key] = path.relative_to(repo).as_posix()
    return ci_config.load(repo, Loader), owner


def deref(cfg, node, seen):
    while isinstance(node, Ref):
        seen.add(node[0])
        value = cfg
        for part in node:
            value = value[part]
        node = value
    return node


def ancestors(cfg, key):
    out, todo = [], [key]
    while todo:
        job = cfg.get(todo.pop())
        ext = job.get('extends', []) if isinstance(job, dict) else []
        for parent in [ext] if isinstance(ext, str) else ext:
            if parent not in out:
                out.append(parent)
                todo.append(parent)
    return out


def changes_lists(cfg, key):
    """The `rules:changes` path lists a job is created by, and the keys its rules came from."""
    seen = set()
    rules = None
    for name in [key] + ancestors(cfg, key):  # own rules win; else the nearest parent's
        if isinstance(cfg.get(name), dict) and 'rules' in cfg[name]:
            rules = deref(cfg, cfg[name]['rules'], seen)
            break
    flat = []
    for rule in rules or []:
        rule = deref(cfg, rule, seen)
        flat += [deref(cfg, r, seen) for r in rule] if isinstance(rule, list) else [rule]
    out = []
    for rule in flat:
        if isinstance(rule, dict) and 'changes' in rule:
            ch = deref(cfg, rule['changes'], seen)
            ch = deref(cfg, ch.get('paths'), seen) if isinstance(ch, dict) else ch
            out.append([str(g) for g in ch])
    return out, seen


def jobs(cfg):
    return [k for k, v in cfg.items() if isinstance(v, dict) and not k.startswith('.') and k not in RESERVED]


def created_by(cfg, path):
    """Change-scoped jobs a merge request touching only `path` creates (always-jobs aside)."""
    return sorted(j for j in jobs(cfg) if any(any(matches(path, g) for g in lst) for lst in changes_lists(cfg, j)[0]))


def problems(repo: Path, path_scoped=PATH_SCOPED) -> list[str]:
    out = []
    try:
        cfg, owner = model(repo)
    except (ValueError, FileNotFoundError) as exc:
        return [str(exc)]
    included = {p.relative_to(repo).as_posix() for p in ci_config.files(repo)}
    on_disk = {p.relative_to(repo).as_posix() for p in (repo / '.gitlab/ci').rglob('*.yml')}
    out += [f'{f}: on disk but not in the root include: list' for f in sorted(on_disk - included)]
    out += [f'{k}: a job in the root file; jobs belong in an area file' for k in jobs(cfg) if owner[k] == ROOT_FILE]
    area_deps = {}
    for job in jobs(cfg):
        lists, refs = changes_lists(cfg, job)
        deps = {owner[k] for k in ancestors(cfg, job) + sorted(refs) if k in owner}
        area_deps.setdefault(owner[job], set()).update(deps | {owner[job]})
    for job in jobs(cfg):
        if job in WHOLE_CONFIG_JOBS or owner[job] == ROOT_FILE:
            continue
        lists, refs = changes_lists(cfg, job)
        if lists and job not in path_scoped and not any(
                any(matches(f, g) for g in lst) for lst in lists for f in (ROOT_FILE, owner[job])):
            out.append(f'{job}: rules:changes names neither {ROOT_FILE} nor {owner[job]}, '
                       'so an edit to its definition does not start it')
        need = {owner[job]} | {owner[k] for k in ancestors(cfg, job) if owner.get(k, ROOT_FILE) != ROOT_FILE}
        for lst in lists:
            if ROOT_FILE in lst:
                for f in sorted(need):
                    if not any(matches(f, g) for g in lst):
                        out.append(f'{job}: rules:changes lists {ROOT_FILE} but not {f}')
            for f in sorted(included - {ROOT_FILE} - area_deps[owner[job]]):
                if any(matches(f, g) for g in lst):
                    out.append(f'{job}: rules:changes lists {f}, which is not a dependency of {owner[job]}')
    if MEMORY_DATA in included:
        got = set(created_by(cfg, MEMORY_DATA))
        want = {MEMORY_GATE} | (WHOLE_CONFIG_JOBS & set(jobs(cfg)))
        if got != want:
            out.append(f'an edit to {MEMORY_DATA} creates {sorted(got)}, expected {sorted(want)}')
    return out


def fixture(files: dict[str, str]) -> Path:
    tmp = Path(tempfile.mkdtemp(prefix='ci-config-'))
    for rel, text in files.items():
        (tmp / rel).parent.mkdir(parents=True, exist_ok=True)
        (tmp / rel).write_text(text)
    return tmp


def selftest() -> None:
    root = 'include:\n  - local: .gitlab/ci/a.yml\n  - local: .gitlab/ci/b.yml\n\nstages: [test]\n'
    a = ('.ta:\n  script: [x]\n'
         'ja:\n  script: [x]\n  rules:\n    - changes: [.gitlab-ci.yml, .gitlab/ci/a.yml]\n')
    b = ('jb:\n  extends: .ta\n  rules:\n    - changes: [.gitlab-ci.yml, .gitlab/ci/b.yml, .gitlab/ci/a.yml]\n'
         'jc:\n  script: [x]\n'
         'jp:\n  script: [x]\n  rules:\n    - changes: [src/**/*]\n')
    good = {ROOT_FILE: root, '.gitlab/ci/a.yml': a, '.gitlab/ci/b.yml': b}
    assert problems(fixture(good), {'jp'}) == [], problems(fixture(good), {'jp'})
    red = {
        'own area file missing': {'.gitlab/ci/a.yml': a.replace(', .gitlab/ci/a.yml]', ']')},
        'extended file missing': {'.gitlab/ci/b.yml': b.replace(', .gitlab/ci/a.yml]', ']')},
        'foreign area listed': {'.gitlab/ci/a.yml': a.replace('a.yml]', 'a.yml, .gitlab/ci/b.yml]')},
        'orphan area file': {'.gitlab/ci/c.yml': 'jd:\n  script: [x]\n'},
        'key in two files': {'.gitlab/ci/b.yml': b + 'ja:\n  script: [y]\n'},
        'job in the root': {ROOT_FILE: root + 'jr:\n  script: [x]\n'},
    }
    for label, change in red.items():
        assert problems(fixture({**good, **change}), {'jp'}), f'red control passed: {label}'
    assert problems(fixture(good), set()), 'red control passed: path-only job not in PATH_SCOPED'
    print(f'selftest ok: 1 green fixture, {len(red) + 1} red controls caught')


def main() -> int:
    selftest()
    found = problems(REPO)
    for p in found:
        print(f'ci-config: {p}')
    if found:
        return 1
    cfg, _ = model(REPO)
    for path in ci_config.files(REPO):
        rel = path.relative_to(REPO).as_posix()
        print(f'{rel}: an edit creates {len(created_by(cfg, rel))} change-scoped job(s)')
    print(f'ci-config: OK ({len(jobs(cfg))} jobs in {len(ci_config.files(REPO))} files)')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
