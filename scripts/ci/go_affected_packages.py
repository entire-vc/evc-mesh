#!/usr/bin/env python3
"""Choose the Go packages whose tests a merge request must run.

    python3 scripts/ci/go_affected_packages.py --base "$CI_MERGE_REQUEST_DIFF_BASE_SHA" \
        [--head HEAD] [--tags integration] [--only PATH ...] [--empty-means-all]

Output contract (what the CI jobs rely on):

  stdout  one entry per line, nothing else:
            ALL                run the whole suite (`./...`);
            <import path>      run this package;
            (nothing)          run nothing.
  stderr  exactly one `selection: ...` line saying which of those it is and why.
          It is the line to read in the job log.

The exit status is always 0: every failure inside the selector turns into `ALL`.

What is selected, for a diff `base..head`:

  1. every package with a changed non-test file (or a changed //go:embed file);
  2. every package that imports one of those, transitively;
  3. every package whose TESTS import a package from (1) or (2);
  4. every package whose own tests changed (`_test.go`, testdata) - and ONLY that
     package: a test-only edit cannot change how a dependent behaves;
  5. every package that declares, in go_test_file_deps.txt, that its tests read a
     file the diff touched. Several tests here parse other packages' sources,
     `cmd/api/main.go`, `migrations/` or `web/src/locales/`: no import edge shows it.

It answers `ALL` instead of guessing when anything cannot be resolved: go.mod or
go.sum, a migration, a CI file, a shared test helper, a changed file that no
package owns, a diff base that is not in the checkout, a `go list` error, an empty
selection for a Go change, or any exception. A selection that reaches every package
is also reported as `ALL`, so the full run stays the exact `./...` it was before.

Requires: git, go. Stdlib only.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path, PurePosixPath

DEPS_FILE = Path(__file__).with_name('go_test_file_deps.txt')

# Everything that can influence a Go test result, besides the globs that
# go_test_file_deps.txt adds (see scope_globs). A changed file that matches NONE of
# them cannot, so it never starts a Go test job. `.gitlab-ci.yml` carries the same
# list as `rules:changes` between the go-test-scope markers, and
# test_go_affected_packages.py fails when the two differ.
BASE_SCOPE_GLOBS = (
    '**/*.go',
    'go.mod',
    'go.sum',
    'go.work',
    'go.work.sum',
    'migrations/**/*',
    '.gitlab-ci.yml',
    'scripts/ci/**/*',
    'docs/ci-templates/**/*',
    'docs/api/**/*',
    'cmd/**/*',
    'internal/**/*',
    'pkg/**/*',
    'examples/**/*',
    'tests/integration/**/*',
)

# A change here can alter ANY test result: the dependency set, the schema every
# database test runs against, the CI definition itself.
FULL_GLOBS = {
    'go.mod': 'go.mod changed',
    'go.sum': 'go.sum changed',
    'go.work': 'go.work changed',
    'go.work.sum': 'go.work.sum changed',
    'migrations/**/*': 'a database migration changed (every database test runs on it)',
    '.gitlab-ci.yml': 'a CI file changed',
    'scripts/ci/**/*': 'a CI file changed',
    'docs/ci-templates/**/*': 'a CI file changed',
}

# A directory named like a shared test helper: a change there reaches tests through
# no import edge the selector can prove complete, so the whole suite runs.
SHARED_HELPER_DIR = re.compile(
    r'(?:^|/)(?:testutils?|testhelpers?|testsupport|testkit|testing|fixtures?|mocks?|fakes?|'
    r'[a-z0-9_]*test)(?:/|$)')
TESTDATA_DIR = re.compile(r'(?:^|/)testdata(?:/|$)')


class Full(Exception):
    """The selection cannot be narrowed: run everything and say why."""


def glob_to_regex(glob: str) -> re.Pattern:
    out, i = [], 0
    while i < len(glob):
        if glob.startswith('**/', i):
            out.append('(?:.*/)?')
            i += 3
        elif glob.startswith('**', i):
            out.append('.*')
            i += 2
        elif glob[i] == '*':
            out.append('[^/]*')
            i += 1
        elif glob[i] == '?':
            out.append('[^/]')
            i += 1
        else:
            out.append(re.escape(glob[i]))
            i += 1
    return re.compile('^' + ''.join(out) + '$')


def matches(path: str, glob: str) -> bool:
    return glob_to_regex(glob).match(path) is not None


def load_deps(path: Path) -> list[tuple[str, list[str]]]:
    """`<package dir> <- <glob> [<glob> ...]` per line."""
    entries = []
    for n, line in enumerate(path.read_text(encoding='utf-8').splitlines(), 1):
        line = line.split(' #', 1)[0].strip()
        if not line or line.startswith('#'):
            continue
        m = re.fullmatch(r'(\S+)\s+<-\s+(.+)', line)
        if not m:
            raise Full(f'{path.name}:{n}: cannot parse {line!r}')
        entries.append((m.group(1), m.group(2).split()))
    return entries


def scope_globs(deps_file: Path = DEPS_FILE) -> list[str]:
    seen: list[str] = []
    for g in [*BASE_SCOPE_GLOBS, *(g for _, globs in load_deps(deps_file) for g in globs)]:
        if g not in seen:
            seen.append(g)
    return seen


def sh(*cmd: str, cwd: str | None = None) -> str:
    try:
        return subprocess.run(cmd, check=True, capture_output=True, text=True, cwd=cwd).stdout
    except (OSError, subprocess.CalledProcessError) as exc:
        detail = getattr(exc, 'stderr', '') or str(exc)
        raise Full(f'`{" ".join(cmd[:3])}` failed: {detail.strip()[:200]}') from exc


@dataclass
class Pkg:
    path: str
    dir: str                                          # repo-relative, '.' for the module root
    nontest: set[str] = field(default_factory=set)    # files of the importable package
    tests: set[str] = field(default_factory=set)      # _test.go files and test embeds
    ignored: set[str] = field(default_factory=set)    # excluded by build constraints
    imports: set[str] = field(default_factory=set)
    test_imports: set[str] = field(default_factory=set)


def decode_stream(text: str) -> list[dict]:
    dec, i, out = json.JSONDecoder(), 0, []
    while i < len(text):
        while i < len(text) and text[i].isspace():
            i += 1
        if i >= len(text):
            break
        obj, i = dec.raw_decode(text, i)
        out.append(obj)
    return out


NONTEST_KEYS = ('GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'SFiles',
                'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'EmbedFiles')


def load_packages(root: Path, tags: str) -> dict[str, Pkg]:
    # -race: the tests run with it, and files tagged `race` exist only then.
    cmd = ['go', 'list', '-e', '-race', '-json']
    if tags:
        cmd.append(f'-tags={tags}')
    packages: dict[str, Pkg] = {}
    for p in decode_stream(sh(*cmd, './...', cwd=str(root))):
        if p.get('Error') or p.get('DepsErrors') or p.get('Incomplete'):
            err = (p.get('Error') or {}).get('Err') or 'incomplete package'
            raise Full(f'go list reports an error for {p.get("ImportPath")}: {err}')
        d = PurePosixPath(os.path.relpath(p['Dir'], root)).as_posix()

        def rel(*keys: str) -> set[str]:
            return {f'{d}/{n}' if d != '.' else n for k in keys for n in p.get(k) or []}

        packages[p['ImportPath']] = Pkg(
            path=p['ImportPath'], dir=d,
            nontest=rel(*NONTEST_KEYS),
            tests=rel('TestGoFiles', 'XTestGoFiles', 'TestEmbedFiles', 'XTestEmbedFiles'),
            ignored=rel('IgnoredGoFiles', 'IgnoredOtherFiles'),
            imports=set(p.get('Imports') or []),
            test_imports=set(p.get('TestImports') or []) | set(p.get('XTestImports') or []),
        )
    if not packages:
        raise Full('go list returned no packages')
    return packages


def excluded_by_constraints(root: Path, tags: str, directory: str) -> set[str]:
    """Go files of `directory` that build constraints exclude in this configuration."""
    cmd = ['go', 'list', '-e', '-race', '-json']
    if tags:
        cmd.append(f'-tags={tags}')
    probe = subprocess.run([*cmd, f'./{directory}'], cwd=root, capture_output=True, text=True)
    if probe.returncode != 0:
        return set()
    return {f'{directory}/{n}' if directory != '.' else n
            for p in decode_stream(probe.stdout) for n in p.get('IgnoredGoFiles') or []}


def changed_files(root: Path, base: str, head: str) -> list[tuple[str, str]]:
    if not base or set(base) == {'0'}:
        raise Full('no diff base (CI_MERGE_REQUEST_DIFF_BASE_SHA is empty)')
    for ref in (base, head):
        probe = subprocess.run(['git', 'cat-file', '-e', f'{ref}^{{commit}}'], cwd=root,
                               capture_output=True)
        if probe.returncode != 0:
            raise Full(f'commit {ref[:12]} is not in this checkout (shallow clone?)')
    raw = sh('git', 'diff', '--name-status', '--no-renames', '-z', base, head, cwd=str(root))
    parts = raw.split('\0')
    return [(parts[i], parts[i + 1]) for i in range(0, len(parts) - 1, 2)]


@dataclass
class Result:
    mode: str                                   # 'SET' | 'NONE'
    reason: str = ''
    packages: list[str] = field(default_factory=list)
    changed: dict[str, str] = field(default_factory=dict)   # package -> a changed file in it
    read: dict[str, str] = field(default_factory=dict)      # package -> a changed file its tests read


def select(root: Path, base: str, head: str, tags: str, deps_file: Path) -> Result:
    files = changed_files(root, base, head)
    paths = [f for _, f in files]
    if not paths:
        return Result('NONE', 'the diff is empty')

    globs = scope_globs(deps_file)

    def in_scope(path: str) -> bool:
        return any(matches(path, g) for g in globs)

    for f in paths:
        for glob, why in FULL_GLOBS.items():
            if matches(f, glob):
                raise Full(f'{why} ({f})')
        if SHARED_HELPER_DIR.search(f) and in_scope(f):
            raise Full(f'a shared test helper or fixture changed ({f})')

    relevant = [(s, f) for s, f in files if in_scope(f)]
    if not relevant:
        shown = ', '.join(paths[:3]) + (' ...' if len(paths) > 3 else '')
        return Result('NONE', f'no changed file can reach a Go test ({len(paths)} files: {shown})')

    pkgs = load_packages(root, tags)
    by_dir = {p.dir: p for p in pkgs.values()}
    deps = load_deps(deps_file)

    nontest_seeds: dict[str, str] = {}
    test_seeds: dict[str, str] = {}
    declared: dict[str, str] = {}
    unresolved: list[str] = []
    saw_go_change = False

    for status, f in relevant:
        is_go = f.endswith('.go')
        saw_go_change = saw_go_change or is_go

        # A declared file read adds its package; it never explains the file itself.
        read_by_tests = False
        for pdir, pglobs in deps:
            if any(matches(f, g) for g in pglobs):
                read_by_tests = True
                # A package this build configuration does not compile (a tagged-out
                # integration suite, say) has no test to run here. That the directory
                # exists at all is checked by test_go_affected_packages.py.
                if pdir in by_dir:
                    declared[by_dir[pdir].path] = f

        owner, kind = None, ''
        for p in pkgs.values():
            if f in p.nontest:
                owner, kind = p, 'nontest'
            elif f in p.tests:
                owner, kind = p, 'test'
            elif f in p.ignored:
                owner, kind = p, 'ignored'
            if owner:
                break
        if owner:
            if kind == 'nontest':
                nontest_seeds[owner.path] = f
            elif kind == 'test':
                test_seeds.setdefault(owner.path, f)
            continue

        directory = str(PurePosixPath(f).parent)
        if status == 'D' and directory in by_dir:
            # Gone from the tree, so in no file list: judge it by its name and by the
            # package that still lives in its directory.
            if f.endswith('_test.go') or TESTDATA_DIR.search(f):
                test_seeds.setdefault(by_dir[directory].path, f)
            else:
                nontest_seeds[by_dir[directory].path] = f
            continue
        if status == 'D' and is_go:
            continue          # the whole package is gone; its importers changed in this diff too

        if is_go:
            # In no listed package: excluded by build constraints in this configuration
            # (cannot affect this run) or invisible to go list (cannot be judged).
            if f in excluded_by_constraints(root, tags, directory):
                continue
            unresolved.append(f)
            continue

        # Not Go: owned by the nearest ancestor directory that is a package.
        anc = PurePosixPath(directory)
        while str(anc) not in ('.', '') and str(anc) not in by_dir:
            anc = anc.parent
        owner = by_dir.get(str(anc))
        if owner is not None and owner.dir != '.':
            if f.endswith('.md'):
                continue
            if TESTDATA_DIR.search(f):
                test_seeds.setdefault(owner.path, f)
            else:
                nontest_seeds[owner.path] = f   # an unknown file in a package: assume it is an input
            continue
        if read_by_tests or f.endswith('.md'):
            continue
        unresolved.append(f)

    if unresolved:
        raise Full(f'{len(unresolved)} changed file(s) belong to no package ({", ".join(unresolved[:3])})')

    importers: dict[str, set[str]] = {}
    for p in pkgs.values():
        for dep in p.imports:
            importers.setdefault(dep, set()).add(p.path)
    closure = set(nontest_seeds)                                     # (1) + (2)
    queue = list(closure)
    while queue:
        for importer in importers.get(queue.pop(), ()):
            if importer not in closure:
                closure.add(importer)
                queue.append(importer)
    selected = closure | {p.path for p in pkgs.values() if p.test_imports & closure}   # (3)
    selected |= set(test_seeds) | set(declared)                                         # (4) + (5)

    if saw_go_change and not selected:
        raise Full('changed Go files resolved to no package in this build configuration')
    if not selected:
        return Result('NONE', 'only files that no Go test reads changed')
    if len(selected) == len(pkgs):
        raise Full(f'the selection reaches all {len(pkgs)} packages')
    changed = {**test_seeds, **nontest_seeds}
    read = {k: v for k, v in declared.items() if k not in changed}
    return Result('SET', packages=sorted(selected), changed=changed, read=read)


def under(path: str, prefixes: list[str]) -> bool:
    for pre in prefixes:
        base = pre[:-4] if pre.endswith('/...') else pre
        if path == base or path.startswith(base + '/'):
            return True
    return False


def run(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description='Select Go packages to test for a diff.')
    ap.add_argument('--base', default=os.environ.get('CI_MERGE_REQUEST_DIFF_BASE_SHA', ''))
    ap.add_argument('--head', default='HEAD')
    ap.add_argument('--tags', default='', help='build tags of the job being narrowed')
    ap.add_argument('--deps', type=Path, default=DEPS_FILE)
    ap.add_argument('--root', type=Path, default=Path.cwd())
    ap.add_argument('--only', nargs='*', default=[],
                    help='keep only packages under these repo-relative paths (`dir/...` allowed)')
    ap.add_argument('--empty-means-all', action='store_true',
                    help='for a job that cannot run nothing (it must leave a coverage profile)')
    ap.add_argument('--print-scope-globs', action='store_true')
    args = ap.parse_args(argv)

    if args.print_scope_globs:
        print('\n'.join(scope_globs(args.deps)))
        return 0

    root = args.root.resolve()
    try:
        res = select(root, args.base, args.head, args.tags, args.deps)
        if res.mode == 'NONE' and args.empty_means_all:
            raise Full(f'{res.reason}, and this job must run something')
        module = sh('go', 'list', '-m', cwd=str(root)).strip()
    except Full as why:
        print(f'selection: ALL because {why}', file=sys.stderr)
        print('ALL')
        return 0
    except BaseException as exc:                       # noqa: BLE001 - fail safe, never fail open
        print(f'selection: ALL because the selector failed ({type(exc).__name__}: {exc})',
              file=sys.stderr)
        print('ALL')
        return 0

    if res.mode == 'NONE':
        print(f'selection: none because {res.reason}', file=sys.stderr)
        return 0

    def short(p: str) -> str:
        return p[len(module) + 1:] if p.startswith(module + '/') else '.'

    chosen = res.packages
    if args.only:
        chosen = [p for p in chosen if under(short(p), args.only)]
        if not chosen:
            print(f'selection: none because no package under {" ".join(args.only)} is affected '
                  f'(the diff affects {len(res.packages)} packages elsewhere)', file=sys.stderr)
            return 0

    def listed(items: dict[str, str]) -> str:
        shown = [f'{short(k)} ({v})' for k, v in items.items()]
        return ', '.join(shown[:3]) + (f' and {len(shown) - 3} more' if len(shown) > 3 else '')

    why = f'changed {listed(res.changed)}' if res.changed else 'no changed package'
    if res.read:
        why += f'; their tests read the changed files: {", ".join(short(k) for k in res.read)}'
    scope = f' under {" ".join(args.only)}' if args.only else ''
    print(f'selection: {len(chosen)} packages{scope} because {why}', file=sys.stderr)
    for p in chosen:
        print(p)
    return 0


if __name__ == '__main__':
    sys.exit(run())
