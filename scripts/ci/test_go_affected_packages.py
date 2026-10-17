#!/usr/bin/env python3
"""Tests for scripts/ci/go_affected_packages.py, the Go package selector.

Needs git and go. Runs the real selector on a disposable module whose import graph is
known, so every expectation below is a fact about that graph, not about this repository.

Three groups:

  * selection cases on the disposable module (leaf, shared, test-only, embedded, go.mod,
    docs-only, a declared file read, fail-safe cases ...);
  * the red control: the same cases run against deliberately broken copies of the
    selector. A selector that ignores importers, test imports, declared file reads or
    the full-run triggers must FAIL them. A check nobody has seen fail proves nothing,
    so this group is what makes the first one worth believing;
  * checks on this repository: the `rules:changes` list in .gitlab-ci.yml is the
    selector's own, and every test that reads files outside its package is declared in
    go_test_file_deps.txt.
"""
from __future__ import annotations

import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
SELECTOR = HERE / 'go_affected_packages.py'
sys.path.insert(0, str(HERE))
import go_affected_packages as selector  # noqa: E402

MODULE = 'example.test/m'

INTEGRATION_TEST = (
    '//go:build integration\n\npackage integration\n\nimport (\n\t"testing"\n\n\t"%s/internal/service"\n)\n\n'
    'func TestAPI(t *testing.T) { _ = service.Run() }\n' % 'example.test/m')

BASE_FILES = {
    'go.mod': f'module {MODULE}\n\ngo 1.22\n',
    'internal/model/model.go': 'package model\n\nfunc Name() string { return "m" }\n',
    'internal/model/model_test.go': 'package model\n\nimport "testing"\n\nfunc TestName(t *testing.T) {}\n',
    'internal/service/service.go': f'package service\n\nimport "{MODULE}/internal/model"\n\nfunc Run() string {{ return model.Name() }}\n',
    'internal/service/service_test.go': 'package service\n\nimport "testing"\n\nfunc TestRun(t *testing.T) {}\n',
    'internal/service/testdata/case.txt': 'one\n',
    'internal/handler/handler.go': f'package handler\n\nimport "{MODULE}/internal/service"\n\nfunc Serve() string {{ return service.Run() }}\n',
    'cmd/api/main.go': f'package main\n\nimport "{MODULE}/internal/handler"\n\nfunc main() {{ _ = handler.Serve() }}\n',
    'pkg/util/util.go': 'package util\n\nfunc Pad(s string) string { return " " + s }\n',
    # Its TESTS import model; its own code does not.
    'internal/readonly/readonly.go': 'package readonly\n\nfunc Version() int { return 1 }\n',
    'internal/readonly/readonly_test.go': f'package readonly_test\n\nimport (\n\t"testing"\n\n\t"{MODULE}/internal/model"\n)\n\nfunc TestUsesModel(t *testing.T) {{ _ = model.Name() }}\n',
    # An embedded file is part of the package.
    'internal/embedded/embedded.go': 'package embedded\n\nimport _ "embed"\n\n//go:embed data/payload.txt\nvar Payload string\n',
    'internal/embedded/data/payload.txt': 'x\n',
    'internal/consumer/consumer.go': f'package consumer\n\nimport "{MODULE}/internal/embedded"\n\nfunc Size() int {{ return len(embedded.Payload) }}\n',
    # Reads another package's source without importing it (declared in deps below).
    'internal/scanner/scanner.go': 'package scanner\n',
    'internal/scanner/scanner_test.go': 'package scanner\n\nimport (\n\t"os"\n\t"testing"\n)\n\nfunc TestHandlerSource(t *testing.T) {\n\tif _, err := os.ReadFile("../handler/handler.go"); err != nil {\n\t\tt.Fatal(err)\n\t}\n}\n',
    # A shared helper: reaches tests through no import edge worth trusting.
    'internal/testutil/testutil.go': 'package testutil\n\nfunc Helper() {}\n',
    # Only built with -tags integration.
    'tests/integration/api_test.go': INTEGRATION_TEST,
    'docs/readme.md': 'docs\n',
    'web/app.js': 'x\n',
    'web/locales/board.json': '{}\n',
    'migrations/001_init.sql': 'select 1;\n',
    '.gitlab-ci.yml': 'stages: [test]\n',
}
DEPS = ('internal/scanner <- internal/handler/*.go\ntests/integration <- cmd/api/main.go\n'
        'internal/model <- web/locales/*.json\n')

M = {
    'model': f'{MODULE}/internal/model', 'service': f'{MODULE}/internal/service',
    'handler': f'{MODULE}/internal/handler', 'api': f'{MODULE}/cmd/api',
    'util': f'{MODULE}/pkg/util', 'readonly': f'{MODULE}/internal/readonly',
    'embedded': f'{MODULE}/internal/embedded', 'consumer': f'{MODULE}/internal/consumer',
    'scanner': f'{MODULE}/internal/scanner', 'integration': f'{MODULE}/tests/integration',
}


def pkgs(*names: str) -> set[str]:
    return {M[n] for n in names}


# (name, {path: new content, or None to delete}, extra args, expected, log must contain)
# expected: a set of import paths, 'ALL', or 'none'.
CASES = [
    ('a leaf package change selects it, what imports it transitively, and packages whose tests import it',
     {'internal/model/model.go': 'package model\n\nfunc Name() string { return "n" }\n'}, [],
     pkgs('model', 'service', 'handler', 'api', 'readonly'), 'because changed'),
    ('middle package change selects what imports it, not what it imports',
     {'internal/service/service.go': f'package service\n\nimport "{MODULE}/internal/model"\n\nfunc Run() string {{ return model.Name() + "!" }}\n'}, [],
     pkgs('service', 'handler', 'api'), 'because changed'),
    ('a package with no importers selects only itself',
     {'pkg/util/util.go': 'package util\n\nfunc Pad(s string) string { return "  " + s }\n'}, [],
     pkgs('util'), 'because changed'),
    ('a test-only edit selects that package and nothing that imports it',
     {'internal/service/service_test.go': 'package service\n\nimport "testing"\n\nfunc TestRun(t *testing.T) { t.Log("x") }\n'}, [],
     pkgs('service'), 'because changed'),
    ('a testdata edit selects that package only',
     {'internal/service/testdata/case.txt': 'two\n'}, [],
     pkgs('service'), 'because changed'),
    ('an embedded file is part of the package: it and its importers',
     {'internal/embedded/data/payload.txt': 'yy\n'}, [],
     pkgs('embedded', 'consumer'), 'because changed'),
    ('a test that reads another package source without importing it is selected (declared read)',
     {'internal/handler/handler.go': f'package handler\n\nimport "{MODULE}/internal/service"\n\nfunc Serve() string {{ return service.Run() + "?" }}\n'}, [],
     pkgs('handler', 'api', 'scanner'), 'because changed'),
    ('a file outside Go that a test reads (declared) selects that package alone',
     {'web/locales/board.json': '{\"a\": 1}\n'}, [],
     pkgs('model'), 'their tests read the changed files: internal/model'),
    ('docs and web only: nothing runs',
     {'docs/readme.md': 'changed\n', 'web/app.js': 'y\n'}, [],
     'none', 'selection: none because'),
    ('docs only with --empty-means-all (a job that must leave a profile): everything',
     {'docs/readme.md': 'changed\n'}, ['--empty-means-all'],
     'ALL', 'selection: ALL because'),
    ('go.mod changed: everything',
     {'go.mod': f'module {MODULE}\n\ngo 1.23\n'}, [], 'ALL', 'go.mod changed'),
    ('a migration changed: everything',
     {'migrations/002_more.sql': 'select 2;\n'}, [], 'ALL', 'migration'),
    ('a CI file changed: everything',
     {'.gitlab-ci.yml': 'stages: [build, test]\n'}, [], 'ALL', 'CI file'),
    ('a shared test helper changed: everything',
     {'internal/testutil/testutil.go': 'package testutil\n\nfunc Helper() { _ = 1 }\n'}, [], 'ALL', 'shared test helper'),
    ('a Go file excluded by build tags leaves an empty selection: everything, never nothing',
     {'tests/integration/api_test.go': INTEGRATION_TEST + '// edited\n'}, [], 'ALL', 'resolved to no package'),
    ('the same file under its tag selects its package',
     {'tests/integration/api_test.go': INTEGRATION_TEST + '// edited\n'}, ['--tags', 'integration'],
     pkgs('integration'), 'because changed'),
    ('a file in scope that no package owns: everything',
     {'docs/api/orphan.json': '{}\n'}, [], 'ALL', 'belong to no package'),
    ('a dependency that does not resolve: everything',
     {'internal/handler/handler.go': f'package handler\n\nimport _ "{MODULE}/internal/nope"\n'}, [], 'ALL', 'go list reports an error'),
    ('a deleted test file selects its package only',
     {'internal/service/service_test.go': None}, [], pkgs('service'), 'because changed'),
    ('a deleted package: everything',
     {'pkg/util/util.go': None}, [], 'ALL', 'resolved to no package'),
    ('--only keeps the packages under the given paths',
     {'internal/model/model.go': 'package model\n\nfunc Name() string { return "o" }\n'},
     ['--only', 'internal/service'], pkgs('service'), 'under internal/service'),
    ('--only with nothing affected under it: nothing, and it says so',
     {'pkg/util/util.go': 'package util\n\nfunc Pad(s string) string { return "   " + s }\n'},
     ['--only', 'cmd/api', 'tests/integration'], 'none', 'no package under'),
    ('--only does not narrow a full run',
     {'go.mod': f'module {MODULE}\n\ngo 1.24\n'}, ['--only', 'cmd/api'], 'ALL', 'go.mod changed'),
]


def git(cwd: Path, *args: str) -> str:
    return subprocess.run(['git', *args], cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()


def write(root: Path, files: dict) -> None:
    for rel, content in files.items():
        path = root / rel
        if content is None:
            path.unlink()
            continue
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding='utf-8')


class Fixture:
    """One git repository, reused: every case branches from `base` and is committed."""

    def __init__(self) -> None:
        self.dir = Path(tempfile.mkdtemp(prefix='go-affected-'))
        git(self.dir, 'init', '-q')
        git(self.dir, 'config', 'user.name', 'ci')
        git(self.dir, 'config', 'user.email', 'ci@example.test')
        write(self.dir, BASE_FILES)
        (self.dir / 'deps.txt').write_text(DEPS, encoding='utf-8')
        git(self.dir, 'add', '-A')
        git(self.dir, 'commit', '-q', '-m', 'base')
        self.base = git(self.dir, 'rev-parse', 'HEAD')

    def close(self) -> None:
        shutil.rmtree(self.dir, ignore_errors=True)

    def run(self, script: Path, changes: dict, args: list[str], base: str | None = None) -> tuple[str, str]:
        git(self.dir, 'checkout', '-q', '-B', 'case', self.base)
        write(self.dir, changes)
        git(self.dir, 'add', '-A')
        git(self.dir, 'commit', '-q', '--allow-empty', '-m', 'case')
        env = {k: v for k, v in os.environ.items() if k != 'CI_MERGE_REQUEST_DIFF_BASE_SHA'}
        done = subprocess.run(
            [sys.executable, str(script), '--base', self.base if base is None else base,
             '--root', str(self.dir), '--deps', str(self.dir / 'deps.txt'), *args],
            capture_output=True, text=True, env=env, timeout=120)
        assert done.returncode == 0, f'the selector must always exit 0: {done.stderr}'
        return done.stdout, done.stderr


def verdict(stdout: str, stderr: str) -> set[str] | str:
    lines = [ln for ln in stdout.splitlines() if ln]
    if lines == ['ALL']:
        return 'ALL'
    return set(lines) if lines else 'none'


def check_cases(fx: Fixture, script: Path, only: str | None = None) -> list[str]:
    """Every case that the given selector gets wrong, as readable lines."""
    failures = []
    for name, changes, args, expected, in_log in CASES:
        if only is not None and name != only:
            continue
        stdout, stderr = fx.run(script, changes, args)
        got = verdict(stdout, stderr)
        log = [ln for ln in stderr.splitlines() if ln.startswith('selection:')]
        if got != expected:
            failures.append(f'{name}: expected {sorted(expected) if isinstance(expected, set) else expected}, '
                            f'got {sorted(got) if isinstance(got, set) else got} ({stderr.strip()})')
        elif len(log) != 1 or in_log not in log[0]:
            failures.append(f'{name}: the log must carry one `selection:` line with {in_log!r}, got {stderr.strip()!r}')
    return failures


def mutant(workdir: Path, old: str, new: str, label: str) -> Path:
    text = SELECTOR.read_text(encoding='utf-8')
    # If the code the control breaks is renamed, the control must say so, not pass.
    assert old in text, f'red control {label!r} no longer matches the selector source: {old!r}'
    path = workdir / f'mutant_{re.sub(r"[^a-z]+", "_", label)}.py'
    path.write_text(text.replace(old, new), encoding='utf-8')
    return path


# What each broken selector gets wrong, so a control cannot pass by failing for another reason.
MUTANTS = [
    ('ignores importers (reverse dependencies)',
     'for importer in importers.get(queue.pop(), ()):', 'for importer in (queue.pop() and ()):',
     'a leaf package change selects it, what imports it transitively, and packages whose tests import it'),
    ('ignores the imports of tests',
     "selected = closure | {p.path for p in pkgs.values() if p.test_imports & closure}",
     'selected = closure',
     'a leaf package change selects it, what imports it transitively, and packages whose tests import it'),
    ('ignores declared file reads',
     'selected |= set(test_seeds) | set(declared)', 'selected |= set(test_seeds)',
     'a test that reads another package source without importing it is selected (declared read)'),
    ('treats a test-only edit as a code change (over-selects)',
     "elif kind == 'test':\n                test_seeds.setdefault(owner.path, f)",
     "elif kind == 'test':\n                nontest_seeds[owner.path] = f",
     'a test-only edit selects that package and nothing that imports it'),
    ('has no full-run triggers',
     'raise Full(f\'{why} ({f})\')', 'pass',
     'go.mod changed: everything'),
    ('skips an empty selection instead of running everything',
     "if saw_go_change and not selected:\n        raise Full('changed Go files resolved to no package in this build configuration')",
     'if False:\n        pass',
     'a Go file excluded by build tags leaves an empty selection: everything, never nothing'),
]


# File-system reads in Go tests, by IMPORT PATH (a package imported under another name, or
# dot-imported, is the same package). Value: function -> index of its PATH argument, or None
# when merely calling it means the test finds the repository by its own means.
FS_READS = {
    'os': {'ReadFile': 0, 'ReadDir': 0, 'Open': 0, 'OpenFile': 0, 'Stat': 0, 'Lstat': 0,
           'DirFS': 0, 'Getwd': None},
    'io/ioutil': {'ReadFile': 0, 'ReadDir': 0},
    'path/filepath': {'Walk': 0, 'WalkDir': 0, 'Glob': 0},
    'go/parser': {'ParseFile': 1, 'ParseDir': 1},
    'io/fs': {'WalkDir': 1, 'ReadFile': 1, 'ReadDir': 1, 'Stat': 1, 'Glob': 1, 'Sub': 1},
    'github.com/pressly/goose': {'CollectMigrations': 0},
    'runtime': {'Caller': None},
}
IMPORT_SPEC = re.compile(r'^\s*(?:(\w+|\.)\s+)?"([^"]+)"', re.M)
CONST_LITERAL = re.compile(r'\bconst\s+(\w+)\s*=\s*("(?:[^"\\\n]|\\.)*"|`[^`]*`)')
LITERAL = re.compile(r'"(?:[^"\\\n]|\\.)*"|`[^`]*`')


def strip_comments(source: str) -> str:
    return '\n'.join(re.sub(r'\s//.*$', '', ln) for ln in source.splitlines()
                      if not ln.strip().startswith('//'))


def call_arguments(code: str, open_paren: int) -> list[str]:
    """Top-level arguments of the call whose `(` is at `open_paren`."""
    depth, args, start, i, quote = 0, [], open_paren + 1, open_paren, ''
    while i < len(code):
        c = code[i]
        if quote:
            if c == '\\' and quote == '"':
                i += 1
            elif c == quote:
                quote = ''
        elif c in '"`\'':
            quote = c
        elif c in '([{':
            depth += 1
        elif c in ')]}':
            depth -= 1
            if depth == 0:
                args.append(code[start:i].strip())
                return args
        elif c == ',' and depth == 1:
            args.append(code[start:i].strip())
            start = i + 1
        i += 1
    return args


def stays_in_package(arg: str, consts: dict[str, str] | None = None) -> bool:
    """True only for a literal path (or a Join of literals) that cannot leave the directory.

    A name counts as a literal when the same file declares `const name = "literal"`.
    """
    consts = consts or {}
    join = re.fullmatch(r'(?:filepath|path)\.Join\((.*)\)', arg, re.S)
    parts = [p.strip() for p in re.split(r',(?=(?:[^"`]*["`][^"`]*["`])*[^"`]*$)', join.group(1))] if join else [arg]
    for part in parts:
        part = consts.get(part, part)
        if not LITERAL.fullmatch(part):
            return False
        text = part[1:-1]
        if '..' in text or text.startswith('/'):
            return False
    return True


def imported_packages(code: str) -> dict[str, str]:
    """Local name -> import path of the packages in FS_READS ('.' is a dot-import)."""
    specs = [m for blk in re.findall(r'\bimport\s*\(([^)]*)\)', code) for m in IMPORT_SPEC.findall(blk)]
    specs += re.findall(r'^\s*import\s+(?:(\w+|\.)\s+)?"([^"]+)"', code, re.M)
    out = {}
    for alias, path in specs:
        path = re.sub(r'/v\d+$', '', path)
        if path in FS_READS and alias != '_':
            out[alias or path.rsplit('/', 1)[-1]] = path
    return out


def outside_reads(source: str) -> list[str]:
    """Reads whose path is not a literal inside the package, one description each.

    Fail closed: a path that is a variable, an environment value, a computed root or a
    `..` is treated as leaving the package, because the selector cannot tell where it goes.
    Calls are matched by what the name was imported as, so `import x "os"` and a dot-import
    are caught like `os.`.
    """
    code = strip_comments(source)
    consts = dict(CONST_LITERAL.findall(code))
    found = []
    for local, path in imported_packages(code).items():
        funcs = FS_READS[path]
        names = '|'.join(funcs)
        call = (rf'(?<![\w.])({names})\(' if local == '.'
                else rf'(?<![\w.]){re.escape(local)}\.({names})\(')
        for m in re.finditer(call, code):
            index = funcs[m.group(1)]
            args = call_arguments(code, m.end() - 1)
            if index is None:
                found.append(f'a repository-root lookup ({path}.{m.group(1)})')
            elif len(args) <= index or not stays_in_package(args[index], consts):
                found.append(f'{path}.{m.group(1)}({args[index] if len(args) > index else "?"})')
    return found


def undeclared_readers(root: Path, declared: set[str]) -> dict[str, list[str]]:
    tracked = subprocess.run(['git', 'ls-files', '*_test.go'], cwd=root, capture_output=True,
                             text=True, check=True).stdout.split()
    undeclared: dict[str, list[str]] = {}
    for rel in tracked:
        if rel.startswith('web/'):
            continue
        reads = outside_reads((root / rel).read_text(encoding='utf-8'))
        pdir = str(Path(rel).parent)
        if reads and pdir not in declared:
            undeclared.setdefault(pdir, []).append(f'{Path(rel).name}: {reads[0]}')
    return undeclared


class SelectionCases(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fx = Fixture()
        cls.tmp = Path(tempfile.mkdtemp(prefix='go-affected-mutants-'))

    @classmethod
    def tearDownClass(cls):
        cls.fx.close()
        shutil.rmtree(cls.tmp, ignore_errors=True)

    def test_every_case_passes_on_the_real_selector(self):
        failures = check_cases(self.fx, SELECTOR)
        self.assertEqual(failures, [], '\n' + '\n'.join(failures))

    def test_a_missing_or_unreachable_base_is_a_full_run(self):
        for base, why in (('', 'no diff base'), ('0' * 40, 'no diff base'),
                          ('deadbeef' * 5, 'is not in this checkout')):
            with self.subTest(base=base):
                stdout, stderr = self.fx.run(SELECTOR, {'pkg/util/util.go': 'package util\n'}, [], base=base)
                self.assertEqual(verdict(stdout, stderr), 'ALL')
                self.assertIn(why, stderr)

    def test_a_selection_that_reaches_every_package_is_reported_as_all(self):
        # Two packages, one imports the other: changing the imported one reaches both.
        root = Path(tempfile.mkdtemp(prefix='go-affected-all-'))
        try:
            git(root, 'init', '-q')
            git(root, 'config', 'user.name', 'ci')
            git(root, 'config', 'user.email', 'ci@example.test')
            write(root, {
                'go.mod': f'module {MODULE}\n\ngo 1.22\n',
                'internal/a/a.go': 'package a\n\nfunc A() int { return 1 }\n',
                'internal/b/b.go': f'package b\n\nimport "{MODULE}/internal/a"\n\nfunc B() int {{ return a.A() }}\n',
            })
            (root / 'deps.txt').write_text('', encoding='utf-8')
            git(root, 'add', '-A')
            git(root, 'commit', '-q', '-m', 'base')
            base = git(root, 'rev-parse', 'HEAD')
            write(root, {'internal/a/a.go': 'package a\n\nfunc A() int { return 2 }\n'})
            git(root, 'commit', '-qam', 'change')
            done = subprocess.run([sys.executable, str(SELECTOR), '--base', base, '--root', str(root),
                                   '--deps', str(root / 'deps.txt')], capture_output=True, text=True)
            self.assertEqual(done.stdout.split(), ['ALL'])
            self.assertIn('reaches all 2 packages', done.stderr)
        finally:
            shutil.rmtree(root, ignore_errors=True)

    def test_red_control_each_broken_selector_fails_the_cases(self):
        """Run the cases against selectors with one behaviour removed: each must be caught."""
        for label, old, new, caught_by in MUTANTS:
            with self.subTest(broken=label):
                self.assertTrue(any(c[0] == caught_by for c in CASES), f'no such case: {caught_by!r}')
                broken = mutant(self.tmp, old, new, label)
                # Only the case written for this fault: the whole matrix would take minutes.
                failures = check_cases(self.fx, broken, only=caught_by)
                self.assertTrue(failures, f'a selector that {label} passed the case written for it '
                                          f'({caught_by!r}): the case cannot see this fault')
                # And the real one passes that same case, so the failure is the fault's.
                self.assertEqual(check_cases(self.fx, SELECTOR, only=caught_by), [])


class RepositoryChecks(unittest.TestCase):
    def test_ci_rules_changes_list_is_the_selectors_own(self):
        ci = (REPO / '.gitlab-ci.yml').read_text(encoding='utf-8')
        begin, end = ci.index('# BEGIN go-test-scope'), ci.index('# END go-test-scope')
        in_ci = re.findall(r'^\s*-\s+"([^"]+)"\s*$', ci[begin:end], re.M)
        self.assertEqual(in_ci, selector.scope_globs(),
                         'rules:changes in .gitlab-ci.yml (.go-test-scope) differs from the selector: '
                         'regenerate it with `python3 scripts/ci/go_affected_packages.py --print-scope-globs`')

    def test_ci_jobs_use_the_scope_and_the_selection(self):
        ci = (REPO / '.gitlab-ci.yml').read_text(encoding='utf-8')

        def job(name: str) -> str:
            m = re.search(rf'^{re.escape(name)}:\n((?:[ \t].*\n|\n)+)', ci, re.M)
            self.assertIsNotNone(m, f'job {name} not found')
            return m.group(1)

        self.assertIn('rules: !reference [.go-test-scope, rules]', job('test'))
        # The two post-review jobs carry the same path list, but manual on a merge request
        # (see `post-review` in .gitlab-ci.yml), so they take it by anchor, not by rule set.
        for name in ('repo-integration', 'tenancy-rbac-gate'):
            self.assertIn('paths: *go-test-scope-paths', job(name), name)
            self.assertIn('stage: post-review', job(name), name)
        self.assertIn('paths: *go-test-scope-paths', job('coverage-gate'))
        self.assertIn('go_selection.sh --empty-means-all', job('test'))
        self.assertIn('coverage_artifact.py run coverage.out coverage-manifest.json go-selection.txt', job('test'))
        # The three jobs that follow the selection must make a full clone: the diff base is not in a shallow one.
        for name in ('test', 'repo-integration', 'tenancy-rbac-gate'):
            self.assertIn('GIT_DEPTH: "0"', job(name), name)

    def test_every_declared_package_exists(self):
        for pdir, globs in selector.load_deps(selector.DEPS_FILE):
            with self.subTest(package=pdir):
                self.assertTrue(list((REPO / pdir).glob('*.go')), f'{pdir} has no Go files')
                self.assertTrue(globs)

    def test_every_test_that_reads_outside_its_package_is_declared(self):
        """The ratchet: a new source-scanning test cannot slip past the selector unnoticed."""
        declared = {pdir for pdir, _ in selector.load_deps(selector.DEPS_FILE)}
        undeclared = undeclared_readers(REPO, declared)
        self.assertEqual(
            undeclared, {},
            'these tests read a file whose path is not a literal inside their package, so the selector '
            'cannot see what they depend on. Add a line to scripts/ci/go_test_file_deps.txt for each '
            'package, naming the files:\n'
            + '\n'.join(f'  {d}: {"; ".join(f)}' for d, f in undeclared.items()))


class RatchetDetection(unittest.TestCase):
    """The ratchet itself, on tests written to slip past a `..`-only check."""

    ENV_ROOT = ('package x\n\nimport (\n\t"os"\n\t"path/filepath"\n\t"testing"\n)\n\n'
                'func TestDoc(t *testing.T) {\n'
                '\t_, _ = os.ReadFile(filepath.Join(os.Getenv("REPO_ROOT"), "docs/x.json"))\n}\n')

    def repo_with(self, source: str) -> Path:
        root = Path(tempfile.mkdtemp(prefix='go-ratchet-'))
        self.addCleanup(shutil.rmtree, root, ignore_errors=True)
        git(root, 'init', '-q')
        (root / 'internal/x').mkdir(parents=True)
        (root / 'internal/x/x_test.go').write_text(source, encoding='utf-8')
        git(root, 'add', '-A')
        return root

    def test_red_an_env_based_root_without_a_declaration_fails(self):
        undeclared = undeclared_readers(self.repo_with(self.ENV_ROOT), declared=set())
        self.assertEqual(list(undeclared), ['internal/x'])

    def test_green_the_same_read_declared_passes(self):
        self.assertEqual(undeclared_readers(self.repo_with(self.ENV_ROOT), declared={'internal/x'}), {})

    HEAD = ('package x\n\nimport (\n\t"io/fs"\n\t"go/parser"\n\t"os"\n\t"path/filepath"\n)\n\n')

    def test_red_a_read_through_an_import_alias_is_caught(self):
        source = ('package x\n\nimport xos "os"\n\nfunc T(path string) {\n\t_, _ = xos.ReadFile(path)\n}\n')
        self.assertTrue(outside_reads(source))
        undeclared = undeclared_readers(self.repo_with(source), declared=set())
        self.assertEqual(list(undeclared), ['internal/x'])
        self.assertEqual(undeclared_readers(self.repo_with(source), declared={'internal/x'}), {})

    def test_red_a_dot_import_makes_every_bare_call_a_read(self):
        source = 'package x\n\nimport . "os"\n\nfunc T(path string) {\n\t_, _ = ReadFile(path)\n}\n'
        self.assertTrue(outside_reads(source))
        grouped = 'package x\n\nimport (\n\t. "path/filepath"\n)\n\nfunc T(d string) {\n\t_, _ = Glob(d)\n}\n'
        self.assertTrue(outside_reads(grouped))

    def test_an_aliased_literal_read_inside_the_package_is_not_flagged(self):
        source = 'package x\n\nimport xos "os"\n\nfunc T() {\n\t_, _ = xos.ReadFile("testdata/a")\n}\n'
        self.assertEqual(outside_reads(source), [])

    def test_a_package_that_is_not_imported_is_not_a_read(self):
        # A local type with a method named like a read is not the os package.
        self.assertEqual(outside_reads('package x\n\nfunc T(r R, p string) {\n\t_ = r.ReadFile(p)\n}\n'), [])

    def test_other_ways_out_of_the_package_are_caught(self):
        for label, body in {
            'variable path': 'p := compute()\n\t_, _ = os.ReadFile(p)',
            'abs path': '_, _ = os.ReadFile("/etc/x")',
            'dotdot join': '_, _ = os.ReadDir(filepath.Join("..", "handler"))',
            'dirfs on a computed root': '_ = os.DirFS(root())',
            'walk with a variable': 'filepath.WalkDir(dir, nil)',
            'parse with a variable': 'parser.ParseFile(nil, name, nil, 0)',
            'fs read with a variable': 'fs.ReadFile(f, name)',
            'repo root by Getwd': 'wd, _ := os.Getwd()\n\t_ = wd',
        }.items():
            with self.subTest(label):
                self.assertTrue(outside_reads(f'{self.HEAD}func T() {{\n\t{body}\n}}\n'), label)

    def test_literal_reads_inside_the_package_are_not_flagged(self):
        body = ('_, _ = os.ReadFile("testdata/a.json")\n\t_, _ = filepath.Glob("*.go")\n\t'
                '_, _ = os.ReadFile(filepath.Join("schema", "fixtures", "a.json"))\n\t'
                '_, _ = os.ReadDir(".")')
        self.assertEqual(outside_reads(f'{self.HEAD}func T() {{\n\t{body}\n}}\n'), [])


if __name__ == '__main__':
    unittest.main()
