#!/usr/bin/env python3
"""Exercise make lint-affected with real gates and disposable local Git remotes."""
from __future__ import annotations

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class LintAffectedGates(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory(prefix='lint-affected-gates-')
        cls.repo = Path(cls.tmp.name) / 'repo'
        cls.repo.mkdir()
        # A complete fixture keeps the existing ratchets real, including their
        # repository-wide scans. No network, real DB or service is used here.
        archive = subprocess.check_output(['git', 'archive', 'HEAD'], cwd=ROOT)
        subprocess.run(['tar', '-xf', '-', '-C', str(cls.repo)], input=archive, check=True)
        for rel in ('Makefile', 'scripts/ci/lint_affected_gates.py'):
            shutil.copyfile(ROOT / rel, cls.repo / rel)
        cls.git('init', '-b', 'main')
        cls.git('config', 'user.name', 'Gate test')
        cls.git('config', 'user.email', 'gate-test@example.invalid')
        cls.git('add', '.')
        cls.git('commit', '-qm', 'test fixture')
        cls.remote = Path(cls.tmp.name) / 'origin.git'
        cls.git('clone', '--bare', str(cls.repo), str(cls.remote))
        cls.git('remote', 'add', 'origin', str(cls.remote))
        cls.git('fetch', 'origin')
        cls.base = cls.git('rev-parse', 'HEAD').strip()
        # mock: external process boundary — lint and package selection are
        # tested elsewhere; these stubs isolate the newly wired fast gates.
        cls.bin = Path(cls.tmp.name) / 'bin'
        cls.bin.mkdir()
        for name, body in {'lint': 'echo "golangci-lint has version 2.14.0"',
                           'goose': 'exit 0', 'selector': 'exit 0'}.items():
            p = cls.bin / name
            p.write_text('#!/bin/sh\n' + body + '\n')
            p.chmod(0o755)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    @classmethod
    def git(cls, *args):
        return subprocess.check_output(['git', *args], cwd=cls.repo, text=True,
                                       stderr=subprocess.STDOUT)

    def lint(self, explicit_base=True):
        env = {k: v for k, v in os.environ.items() if not k.startswith(('MAKE', 'MFLAGS'))}
        result = subprocess.run(
            ['make', '--no-print-directory', 'lint-affected',
             *([f'AFFECTED_BASE={self.base}'] if explicit_base else []),
             f'GOLANGCI_LINT={self.bin / "lint"}', f'GOOSE={self.bin / "goose"}',
             f'GO_AFFECTED_SELECTOR={self.bin / "selector"}'], cwd=self.repo,
            env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=120)
        print(f'lint-affected exit={result.returncode}\n{result.stdout}', flush=True)
        return result

    def test_clean_and_unrelated_diff(self):
        self.assertEqual(self.lint().returncode, 0)
        p = self.repo / 'unrelated.txt'
        try:
            p.write_text('docs only\n')
            result = self.lint()
            self.assertEqual(result.returncode, 0)
            self.assertNotIn('── Pre-push gate:', result.stdout)
        finally:
            p.unlink()

    def test_migration_red_then_newer_green(self):
        versions = [int(p.name.split('_')[0]) for p in (self.repo / 'migrations').glob('*.sql')]
        maximum = max(versions)
        version = next(i for i in range(maximum - 1, 0, -1) if i not in versions)
        bad = self.repo / f'migrations/{version}_prepush_probe.sql'
        good = self.repo / f'migrations/{maximum + 1}_prepush_probe.sql'
        try:
            bad.write_text('-- +goose Up\nSELECT 1;\n')
            self.git('add', str(bad.relative_to(self.repo)))
            result = self.lint()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('ERROR: New migration(s) have out-of-order version number(s):', result.stdout)
            self.git('restore', '--staged', str(bad.relative_to(self.repo)))
            bad.unlink()
            good.write_text('-- +goose Up\nSELECT 1;\n')
            self.git('add', str(good.relative_to(self.repo)))
            self.assertEqual(self.lint().returncode, 0)
        finally:
            self.git('restore', '--staged', 'migrations')
            bad.unlink(missing_ok=True)
            good.unlink(missing_ok=True)

    def test_fetch_failure_is_not_green(self):
        p = self.repo / 'migrations/999999999999_prepush_probe.sql'
        try:
            p.write_text('-- +goose Up\nSELECT 1;\n')
            self.git('add', str(p.relative_to(self.repo)))
            self.git('remote', 'set-url', 'origin', str(self.remote) + '-unavailable')
            result = self.lint()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('requires a fresh origin/main; fetch failed', result.stdout)
        finally:
            self.git('restore', '--staged', 'migrations')
            p.unlink()
            self.git('remote', 'set-url', 'origin', str(self.remote))

    def test_missing_main_is_not_green(self):
        self.git('update-ref', '-d', 'refs/remotes/origin/main')
        p = self.repo / 'migrations/999999999999_prepush_probe.sql'
        try:
            result = self.lint(explicit_base=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('origin/main or diff base unavailable', result.stdout)
            p.write_text('-- +goose Up\nSELECT 1;\n')
            self.git('add', str(p.relative_to(self.repo)))
            # An explicit base permits selection, but the gate still refreshes
            # origin/main before comparing (no migration script SKIP success).
            result = self.lint()
            self.assertEqual(result.returncode, 0, result.stdout)
            self.assertNotIn('SKIP:', result.stdout)
        finally:
            self.git('restore', '--staged', 'migrations')
            p.unlink()
            self.git('fetch', 'origin')

    def test_memory_red_then_registered_green(self):
        p = self.repo / 'internal/domain/memory_prepush_probe.go'
        workflow = self.repo / '.github/workflows/memory-bench.yml'
        original = workflow.read_text()
        try:
            p.write_text('package domain\n')
            result = self.lint()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('NOT in MEMORY_PATHS', result.stdout)
            workflow.write_text(original.replace('  MEMORY_PATHS: |\n',
                                '  MEMORY_PATHS: |\n    internal/domain/memory_prepush_probe.go\n'))
            self.assertEqual(self.lint().returncode, 0)
        finally:
            p.unlink()
            workflow.write_text(original)

    def test_file_reader_red_then_declared_green(self):
        package = self.repo / 'internal/prepushprobe'
        package.mkdir()
        deps = self.repo / 'scripts/ci/go_test_file_deps.txt'
        original = deps.read_text()
        try:
            (package / 'probe_test.go').write_text(
                'package prepushprobe\nimport "os"\nfunc read(path string) { _, _ = os.ReadFile(path) }\n')
            result = self.lint()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('stage new Go tests with git add', result.stdout)
            self.git('add', 'internal/prepushprobe/probe_test.go')
            result = self.lint()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('path is not a literal inside their package', result.stdout)
            # An existing scope glob keeps the scope-sync ratchet satisfied.
            deps.write_text(original + '\ninternal/prepushprobe <- docs/api/agent-runtime.example.json\n')
            self.assertEqual(self.lint().returncode, 0)
        finally:
            self.git('restore', '--staged', 'internal/prepushprobe')
            shutil.rmtree(package)
            deps.write_text(original)

    def test_z_fetch_refreshes_maximum(self):
        """A migration valid against yesterday's ref must fail against fresh main."""
        maximum = max(int(p.name.split('_')[0]) for p in (self.repo / 'migrations').glob('*.sql'))
        remote_checkout = Path(self.tmp.name) / 'remote-main'
        self.git('clone', str(self.remote), str(remote_checkout))
        def remote_git(*args):
            subprocess.run(['git', *args], cwd=remote_checkout, check=True,
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        remote_git('config', 'user.name', 'Gate test')
        remote_git('config', 'user.email', 'gate-test@example.invalid')
        (remote_checkout / f'migrations/{maximum + 2}_remote_probe.sql').write_text('-- +goose Up\nSELECT 1;\n')
        remote_git('add', 'migrations')
        remote_git('commit', '-qm', 'advance main')
        remote_git('push', 'origin', 'main')
        p = self.repo / f'migrations/{maximum + 1}_prepush_probe.sql'
        try:
            p.write_text('-- +goose Up\nSELECT 1;\n')
            self.git('add', str(p.relative_to(self.repo)))
            result = self.lint()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(f'Max version on origin/main: {maximum + 2}', result.stdout)
            self.assertIn('out-of-order version number(s)', result.stdout)
        finally:
            self.git('restore', '--staged', 'migrations')
            p.unlink()


if __name__ == '__main__':
    unittest.main()
