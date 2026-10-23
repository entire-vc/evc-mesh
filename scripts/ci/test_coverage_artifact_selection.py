#!/usr/bin/env python3
"""A coverage profile from a package SELECTION (not `./...`) is only as good as its list.

The diff-coverage gate looks changed lines up in the profile. A profile that is valid,
hash-matched and complete for the packages it names, but does not name a package the
diff changed, would let those lines through unmeasured. These tests run
scripts/ci/coverage_artifact.py in a disposable module and require that:

  * a selection run records its package list in the manifest and verifies;
  * a selection that omits a package with a changed Go file is refused (the red control);
  * a selection can only be verified against a diff base;
  * `ALL` is still exactly the old `./...` manifest.

Needs git and go (with cgo, for -race).
"""
from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
# affected_set_go.sh needs bash 4 (mapfile, associative arrays): macOS ships 3.2.
BASH = '/opt/homebrew/bin/bash' if Path('/opt/homebrew/bin/bash').exists() else shutil.which('bash')
MODULE = 'example.test/cov'
FILES = {
    'go.mod': f'module {MODULE}\n\ngo 1.22\n',
    'a/a.go': 'package a\n\nfunc Double(n int) int { return n * 2 }\n',
    'a/a_test.go': 'package a\n\nimport "testing"\n\nfunc TestDouble(t *testing.T) {\n\tif Double(2) != 4 {\n\t\tt.Fatal("x")\n\t}\n}\n',
    'b/b.go': f'package b\n\nimport "{MODULE}/a"\n\nfunc Quad(n int) int {{ return a.Double(a.Double(n)) }}\n',
    'b/b_test.go': 'package b\n\nimport "testing"\n\nfunc TestQuad(t *testing.T) {\n\tif Quad(1) != 4 {\n\t\tt.Fatal("x")\n\t}\n}\n',
    'c/c.go': 'package c\n\nfunc One() int { return 1 }\n',
    'c/c_test.go': 'package c\n\nimport "testing"\n\nfunc TestOne(t *testing.T) {\n\tif One() != 1 {\n\t\tt.Fatal("x")\n\t}\n}\n',
}


def sh(cwd: Path, *args: str, env: dict | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(args, cwd=cwd, capture_output=True, text=True, env=env)


class SelectionManifest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.repo = Path(tempfile.mkdtemp(prefix='cov-selection-'))
        for rel, content in FILES.items():
            (cls.repo / rel).parent.mkdir(parents=True, exist_ok=True)
            (cls.repo / rel).write_text(content, encoding='utf-8')
        scripts = cls.repo / 'scripts' / 'ci'
        scripts.mkdir(parents=True)
        for name in ('coverage_artifact.py', 'diff_coverage.py', 'go-test-stall-watchdog.sh',
                     'coverage_gate.sh', 'merge_coverage_profiles.py'):
            shutil.copy(HERE / name, scripts / name)
        (cls.repo / 'docs/ci-templates/scripts').mkdir(parents=True)
        shutil.copy(HERE.parents[1] / 'docs/ci-templates/scripts/affected_set_go.sh',
                    cls.repo / 'docs/ci-templates/scripts/affected_set_go.sh')
        for cmd in (['init', '-q'], ['config', 'user.name', 'ci'], ['config', 'user.email', 'ci@example.test'],
                    ['add', '-A'], ['commit', '-q', '-m', 'base']):
            subprocess.run(['git', *cmd], cwd=cls.repo, check=True, capture_output=True)
        cls.base = sh(cls.repo, 'git', 'rev-parse', 'HEAD').stdout.strip()
        # The MR changes a/a.go: a and its importer b are affected, c is not.
        (cls.repo / 'a/a.go').write_text('package a\n\nfunc Double(n int) int { return n + n }\n', encoding='utf-8')
        subprocess.run(['git', 'commit', '-qam', 'change a'], cwd=cls.repo, check=True, capture_output=True)
        cls.head = sh(cls.repo, 'git', 'rev-parse', 'HEAD').stdout.strip()
        cls.env = {**os.environ, 'CI_COMMIT_SHA': cls.head, 'CI_PIPELINE_ID': '7', 'PYTHONDONTWRITEBYTECODE': '1'}

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.repo, ignore_errors=True)

    def artifact(self, *args: str) -> subprocess.CompletedProcess:
        return sh(self.repo, sys.executable, 'scripts/ci/coverage_artifact.py', *args, env=self.env)

    def run_selection(self, lines: list[str]) -> subprocess.CompletedProcess:
        (self.repo / 'sel.txt').write_text('\n'.join(lines) + '\n', encoding='utf-8')
        return self.artifact('run', 'coverage.out', 'manifest.json', 'sel.txt')

    def manifest(self) -> dict:
        return json.loads((self.repo / 'manifest.json').read_text(encoding='utf-8'))

    def test_a_selection_run_is_recorded_and_verifies_against_the_diff(self):
        ran = self.run_selection([f'{MODULE}/a', f'{MODULE}/b'])
        self.assertEqual(ran.returncode, 0, ran.stdout + ran.stderr)
        self.assertEqual(self.manifest()['test_args'][:2], [f'{MODULE}/a', f'{MODULE}/b'])
        self.assertIn('-race', self.manifest()['test_args'])
        self.assertNotIn(f'{MODULE}/c', (self.repo / 'coverage.out').read_text(encoding='utf-8'))
        ok = self.artifact('verify', 'coverage.out', 'manifest.json', self.base)
        self.assertEqual(ok.returncode, 0, ok.stdout + ok.stderr)

    def test_red_control_a_selection_that_omits_a_changed_package_is_refused(self):
        # Valid, complete for what it names, and wrong: the diff changed a/a.go, not c.
        ran = self.run_selection([f'{MODULE}/c'])
        self.assertEqual(ran.returncode, 0, ran.stdout + ran.stderr)
        refused = self.artifact('verify', 'coverage.out', 'manifest.json', self.base)
        self.assertNotEqual(refused.returncode, 0, 'verify accepted a profile that lacks the changed package')
        self.assertIn('omits packages with changed Go files', refused.stderr)
        self.assertIn(f'{MODULE}/a', refused.stderr)

    def test_a_selection_cannot_be_verified_without_a_diff_base(self):
        self.assertEqual(self.run_selection([f'{MODULE}/a', f'{MODULE}/b']).returncode, 0)
        refused = self.artifact('verify', 'coverage.out', 'manifest.json')
        self.assertNotEqual(refused.returncode, 0)
        self.assertIn('diff base', refused.stderr)

    def test_a_selection_naming_a_package_the_module_lacks_is_refused(self):
        self.assertEqual(self.run_selection([f'{MODULE}/a', f'{MODULE}/b']).returncode, 0)
        manifest = self.manifest()
        manifest['test_args'][0] = f'{MODULE}/nope'
        (self.repo / 'manifest.json').write_text(json.dumps(manifest), encoding='utf-8')
        refused = self.artifact('verify', 'coverage.out', 'manifest.json', self.base)
        self.assertNotEqual(refused.returncode, 0)

    def gate(self) -> subprocess.CompletedProcess:
        return sh(self.repo, BASH, 'scripts/ci/coverage_gate.sh', self.base,
                  '--reuse', 'manifest.json', 'coverage.out', env=self.env)

    def test_the_diff_gate_accepts_a_selection_profile_that_covers_the_change(self):
        self.assertEqual(self.run_selection([f'{MODULE}/a', f'{MODULE}/b']).returncode, 0)
        done = self.gate()
        self.assertEqual(done.returncode, 0, done.stdout + done.stderr)

    def test_the_diff_gate_refuses_a_selection_profile_that_misses_the_change(self):
        # Without the verify step a profile naming only `c` has no block of a/a.go to measure.
        self.assertEqual(self.run_selection([f'{MODULE}/c']).returncode, 0)
        done = self.gate()
        self.assertNotEqual(done.returncode, 0, done.stdout + done.stderr)
        self.assertIn('omits packages with changed Go files', done.stdout + done.stderr)

    def test_describe_prints_the_selection_and_its_reason_from_the_manifest(self):
        reason = 'selection: 2 packages because changed a (a/a.go)'
        (self.repo / 'sel.txt.reason').write_text(reason + '\n', encoding='utf-8')
        try:
            self.assertEqual(self.run_selection([f'{MODULE}/a', f'{MODULE}/b']).returncode, 0)
            described = self.artifact('describe', 'manifest.json')
            self.assertEqual(described.returncode, 0, described.stderr)
            self.assertIn(reason, described.stdout)
            self.assertIn(f'selected packages 2: {MODULE}/a {MODULE}/b', described.stdout)
            # The extra field must not disturb verification.
            self.assertEqual(self.artifact('verify', 'coverage.out', 'manifest.json', self.base).returncode, 0)
            self.assertEqual(self.run_selection(['ALL']).returncode, 0)
            full = self.artifact('describe', 'manifest.json')
            self.assertIn('selected packages the whole suite (./...)', full.stdout)
        finally:
            (self.repo / 'sel.txt.reason').unlink()

    def test_describe_without_a_recorded_reason_says_so(self):
        self.assertEqual(self.run_selection([f'{MODULE}/a']).returncode, 0)
        self.assertIn('no reason recorded', self.artifact('describe', 'manifest.json').stdout)

    def test_all_is_still_the_whole_suite_manifest(self):
        ran = self.run_selection(['ALL'])
        self.assertEqual(ran.returncode, 0, ran.stdout + ran.stderr)
        self.assertEqual(self.manifest()['test_args'][0], './...')
        for base in (None, self.base):
            with self.subTest(base=base):
                ok = self.artifact('verify', 'coverage.out', 'manifest.json', *([base] if base else []))
                self.assertEqual(ok.returncode, 0, ok.stdout + ok.stderr)
        self.assertIn(f'{MODULE}/c', (self.repo / 'coverage.out').read_text(encoding='utf-8'))

    def test_an_empty_or_mixed_selection_is_not_a_run(self):
        for lines in ([], ['ALL', f'{MODULE}/a'], ['-race']):
            with self.subTest(lines=lines):
                (self.repo / 'sel.txt').write_text('\n'.join(lines), encoding='utf-8')
                refused = self.artifact('run', 'coverage.out', 'manifest.json', 'sel.txt')
                self.assertNotEqual(refused.returncode, 0)
                self.assertIn('package selection', refused.stderr)


class ToolchainPin(unittest.TestCase):
    """The manifest records `go version`; `verify` compares it. Both jobs must run one image."""

    CI = HERE.parents[1] / '.gitlab-ci.yml'

    @staticmethod
    def job_image(text: str, job: str) -> str | None:
        block = re.search(rf'^{re.escape(job)}:\n(.*?)(?=^\S)', text, re.M | re.S)
        image = re.search(r'^  image: (\S+)', block.group(1), re.M) if block else None
        return image.group(1) if image else None

    def test_test_and_coverage_gate_share_a_digest_pinned_image(self):
        text = self.CI.read_text(encoding='utf-8')
        test, gate = self.job_image(text, 'test'), self.job_image(text, 'coverage-gate')
        self.assertIsNotNone(test)
        self.assertEqual(gate, test)
        self.assertRegex(test, r'@sha256:[0-9a-f]{64}$')

    def test_a_floating_gate_image_is_detected(self):
        text = self.CI.read_text(encoding='utf-8')
        gate = self.job_image(text, 'coverage-gate')
        broken = text.replace(f'  image: {gate}', '  image: golang:1.26-alpine', 1)
        self.assertNotEqual(self.job_image(broken, 'coverage-gate'), self.job_image(broken, 'test'))


if __name__ == '__main__':
    unittest.main()
