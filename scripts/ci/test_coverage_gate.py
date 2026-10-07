#!/usr/bin/env python3
"""Exercise the actual shell gate in disposable Go modules.

Git and the affected-set/diff scripts are real. Only external Go process failures
are injected, so these controls exercise orchestration rather than replacing it.
COVERAGE_GATE_SOURCE_ROOT can point at a known-bad snapshot for red controls.
"""
from pathlib import Path
import os
import shutil
import subprocess
import tempfile
import unittest

SOURCE = Path(os.environ.get('COVERAGE_GATE_SOURCE_ROOT', Path(__file__).resolve().parents[2]))
REAL_GO = shutil.which('go')
BASH = '/opt/homebrew/bin/bash' if Path('/opt/homebrew/bin/bash').exists() else shutil.which('bash')

# mock: external executable boundary — inject process exits and missing/malformed
# artifacts from the Go toolchain, retaining real git and our own gate scripts.
GO_WRAPPER = '''#!/usr/bin/env python3
import os, sys, subprocess
from pathlib import Path
args = sys.argv[1:]
fault = os.environ.get('COVERAGE_TEST_FAULT', '')
if args[:1] == ['list']:
    if fault == 'changed_list' and '-f' not in args and args[-1].startswith('./'):
        print('ERROR: injected changed-package resolution failure', file=sys.stderr)
        sys.exit(23)
    if fault == 'graph_list' and args[-1] == './...':
        print('example.test/gate\\t')
        print('ERROR: injected partial graph failure', file=sys.stderr)
        sys.exit(23)
    if fault == 'empty_graph' and args[-1] == './...':
        sys.exit(0)
    if fault == 'empty_changed' and '-f' not in args and args[-1].startswith('./'):
        sys.exit(0)
    if fault == 'metadata' and '{{.Name}}' in args:
        sys.exit(23)
    if fault == 'metadata' and any('{{.Name}}' in arg for arg in args):
        sys.exit(23)
    if fault == 'ignored_metadata' and any('.IgnoredGoFiles' in arg for arg in args):
        sys.exit(23)
if args[:1] == ['test']:
    if fault == 'test_fail':
        print('ERROR: injected test failure before profile creation')
        sys.exit(23)
    if fault == 'missing_profile':
        sys.exit(0)
    if fault == 'second_profile' and args[-1] == 'example.test/gate/other':
        sys.exit(0)
    profile = next((arg.split('=', 1)[1] for arg in args if arg.startswith('-coverprofile=')), None)
    if fault == 'malformed_profile':
        Path(profile).write_text('mode: atomic\\ngarbage\\n')
        sys.exit(0)
    if fault == 'inconclusive':
        Path(profile).write_text('mode: atomic\\nexample.test/gate/main.go:2.1,2.30 1 1\\n')
        sys.exit(0)
    if fault == 'test_fail_profile':
        Path(profile).write_text('mode: atomic\\n')
        sys.exit(23)
if args[:2] == ['tool', 'cover'] and any(arg.startswith('-func=') for arg in args):
    if fault == 'summary_fail':
        print('total: (statements) 100.0%')
        sys.exit(23)
    if fault == 'inconclusive':
        print('total: (statements) 100.0%')
        sys.exit(0)
    if fault == 'summary_empty':
        sys.exit(0)
os.execv(os.environ['COVERAGE_TEST_REAL_GO'], [os.environ['COVERAGE_TEST_REAL_GO'], *args])
'''


class TestCoverageGate(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory()
        self.addCleanup(self.scratch.cleanup)
        self.root = Path(self.scratch.name)
        for relative in ('docs/ci-templates/scripts/affected_set_go.sh',
                         'scripts/ci/diff_coverage.py', 'scripts/ci/test_diff_coverage.py',
                         'scripts/ci/merge_coverage_profiles.py'):
            original = SOURCE / relative
            if original.exists():
                target = self.root / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(original, target)
        runner = SOURCE / 'scripts/ci/coverage_gate.sh'
        if runner.exists():
            script = runner.read_text()
        else:
            # Execute the historical inline gate with isolated scratch paths.
            job = (SOURCE / '.gitlab-ci.yml').read_text().split('\ncoverage-gate:\n', 1)[1]
            script = job.split('  script:\n    - |\n', 1)[1].split('\n  artifacts:', 1)[0]
            script = '\n'.join(line[6:] for line in script.splitlines())
            script = script.replace('/tmp/', str(self.root / 'tmp') + '/')
        (self.root / 'runner.sh').write_text(script)
        self.write('go.mod', 'module example.test/gate\n\ngo 1.25\n')
        self.write('a.go', 'package gate\nfunc F() int { return 1 }\n')
        self.write('a_test.go', 'package gate\nimport "testing"\nfunc TestF(t *testing.T) { if F() != 1 { t.Fatal("wrong") } }\n')
        self.git('init', '-q')
        self.git('config', 'user.name', 'Coverage Test')
        self.git('config', 'user.email', 'coverage-test@example.test')
        self.commit('base')
        self.base = self.git('rev-parse', 'HEAD').stdout.strip()
        self.write('bin/go', GO_WRAPPER)
        (self.root / 'bin/go').chmod(0o755)
        (self.root / 'bin/bash').symlink_to(BASH)
        (self.root / 'tmp').mkdir()
        self.env = dict(os.environ, PATH=str(self.root / 'bin') + os.pathsep + os.environ['PATH'],
                        COVERAGE_TEST_REAL_GO=REAL_GO, CI_PROJECT_DIR=str(self.root),
                        CI_MERGE_REQUEST_DIFF_BASE_SHA=self.base, TMPDIR=str(self.root / 'tmp'))

    def write(self, relative, content):
        target = self.root / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content)

    def git(self, *args):
        return subprocess.run(['git', *args], cwd=self.root, check=True, capture_output=True, text=True)

    def commit(self, subject):
        self.git('add', '.')
        self.git('commit', '-qm', subject)

    def good_diff(self):
        self.write('a.go', 'package gate\nfunc F() int { return 2 }\n')
        self.write('a_test.go', 'package gate\nimport "testing"\nfunc TestF(t *testing.T) { if F() != 2 { t.Fatal("wrong") } }\n')
        self.commit('change and cover F')

    def run_gate(self, fault='', base=None):
        return subprocess.run([BASH, 'runner.sh', self.base if base is None else base],
                              cwd=self.root, env=dict(self.env, COVERAGE_TEST_FAULT=fault),
                              capture_output=True, text=True, timeout=120)

    def rejects(self, result, marker=None):
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        if marker:
            self.assertIn(marker, result.stdout + result.stderr)

    def test_known_good_go_diff_passes_real_race_atomic_coverage(self):
        self.good_diff()
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Diff coverage 100.0% >= 80%', result.stdout)

    def test_known_uncovered_diff_fails_threshold(self):
        self.write('a.go', 'package gate\nfunc F() int { return 1 }\nfunc Untested() int { return 99 }\n')
        self.commit('add untested function')
        self.rejects(self.run_gate(), 'threshold')

    def test_affected_set_bad_ref_fails(self):
        self.good_diff()
        self.rejects(self.run_gate(base='missing-ref'))

    def test_affected_set_changed_package_error_fails(self):
        self.good_diff()
        self.rejects(self.run_gate('changed_list'))

    def test_affected_set_partial_dependency_graph_error_fails(self):
        self.good_diff()
        self.rejects(self.run_gate('graph_list'))

    def test_empty_resolved_package_or_graph_is_not_no_diff(self):
        self.good_diff()
        for fault in ('empty_changed', 'empty_graph'):
            with self.subTest(fault=fault):
                self.rejects(self.run_gate(fault))

    def test_empty_diff_base_fails(self):
        self.good_diff()
        self.rejects(self.run_gate(base=''))

    def test_package_metadata_error_fails(self):
        self.good_diff()
        self.rejects(self.run_gate('metadata'))

    def test_ignored_file_metadata_error_fails(self):
        self.good_diff()
        self.rejects(self.run_gate('ignored_metadata'))

    def test_failed_test_without_profile_fails(self):
        self.good_diff()
        self.rejects(self.run_gate('test_fail'))

    def test_failed_test_with_profile_fails(self):
        self.good_diff()
        self.rejects(self.run_gate('test_fail_profile'))

    def test_successful_test_without_profile_fails(self):
        self.good_diff()
        self.rejects(self.run_gate('missing_profile'))

    def test_stale_profile_cannot_replace_missing_current_profile(self):
        self.good_diff()
        self.write('tmp/cover_stale.out', 'mode: atomic\nexample.test/gate/a.go:2.1,2.25 1 1\n')
        self.rejects(self.run_gate('missing_profile'))

    def test_missing_second_package_profile_fails(self):
        self.good_diff()
        self.write('other/o.go', 'package other\nfunc Other() int { return 1 }\n')
        self.commit('add second package')
        self.rejects(self.run_gate('second_profile'))

    def test_malformed_profile_fails(self):
        self.good_diff()
        self.rejects(self.run_gate('malformed_profile'))

    def test_summary_tool_failure_is_not_hidden_by_output(self):
        self.good_diff()
        self.rejects(self.run_gate('summary_fail'))

    def test_empty_summary_fails(self):
        self.good_diff()
        self.rejects(self.run_gate('summary_empty'))

    def test_diff_measurement_inconclusive_fails_job(self):
        self.good_diff()
        result = self.run_gate('inconclusive')
        self.rejects(result, '::error::')
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)

    def test_confirmed_no_go_diff_passes(self):
        self.write('README.md', 'documentation change\n')
        self.commit('docs')
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_confirmed_main_only_diff_passes(self):
        self.write('cmd/main.go', 'package main\nfunc main() { println(1) }\n')
        self.commit('add composition root')
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('main', result.stdout)

    def test_mixed_main_library_diff_measures_library(self):
        self.good_diff()
        self.write('cmd/main.go', 'package main\nfunc main() { println(1) }\n')
        self.commit('add composition root')
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Diff coverage 100.0% >= 80%', result.stdout)

    def test_build_excluded_main_file_does_not_mask_library_coverage(self):
        self.good_diff()
        self.write('cmd/main.go', 'package main\nfunc main() {}\n')
        self.write('cmd/tagged.go', '//go:build coverage_extra\n\npackage main\nfunc Extra() { println(1) }\n')
        self.commit('add build-excluded composition source')
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Diff coverage 100.0% >= 80%', result.stdout)

    def test_build_excluded_library_file_is_only_excluded_when_inactive(self):
        self.good_diff()
        self.write('tagged.go', '//go:build coverage_extra\n\npackage gate\nfunc UntestedExtra() int { return 99 }\n')
        self.commit('add tagged library source')
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Diff coverage 100.0% >= 80%', result.stdout)
        self.env['GOFLAGS'] = '-tags=coverage_extra'
        self.rejects(self.run_gate(), 'threshold')

    def test_race_tagged_library_file_remains_under_threshold(self):
        self.good_diff()
        self.write('race.go', '//go:build race\n\npackage gate\nfunc UntestedRace() int { return 99 }\n')
        self.commit('add race-selected source')
        self.rejects(self.run_gate(), 'threshold')

    def test_no_tests_is_not_a_skip(self):
        self.write('other/o.go', 'package other\nfunc Other() int { return 1 }\n')
        self.commit('add untested package')
        self.rejects(self.run_gate(), 'threshold')

    def test_declaration_only_package_is_confirmed_empty(self):
        self.write('other/o.go', 'package other\n// no executable statements\ntype T struct{}\n')
        self.commit('add declarations')
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('no measurable statements', result.stdout)

    def test_comments_outside_existing_blocks_are_confirmed_empty(self):
        self.write('a.go', 'package gate\nfunc F() int { return 1 }\n// only a comment changed\n')
        self.commit('add a comment')
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('no measurable statements', result.stdout)

    def test_reverse_dependency_closure_still_tests_importers(self):
        self.write('other/o.go', 'package other\nimport "example.test/gate"\nfunc Other() int { return gate.F() }\n')
        self.commit('add importer')
        self.base = self.git('rev-parse', 'HEAD').stdout.strip()
        self.good_diff()
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('example.test/gate/other', result.stdout)

    def test_deleted_package_with_surviving_importer_fails_closed(self):
        self.write('obsolete/old.go', 'package obsolete\nfunc Old() int { return 1 }\n')
        self.write('consumer/use.go',
                   'package consumer\nimport "example.test/gate/obsolete"\n'
                   'func Use() int { return obsolete.Old() }\n')
        self.commit('add imported package')
        self.base = self.git('rev-parse', 'HEAD').stdout.strip()
        self.git('rm', 'obsolete/old.go')
        self.commit('delete imported package')
        self.rejects(self.run_gate(), 'obsolete')

    def test_deleted_unreferenced_package_resolves_graph_before_empty_set(self):
        self.write('obsolete/old.go', 'package obsolete\nfunc Old() int { return 1 }\n')
        self.commit('add unreferenced package')
        self.base = self.git('rev-parse', 'HEAD').stdout.strip()
        self.git('rm', 'obsolete/old.go')
        self.commit('delete unreferenced package')
        self.rejects(self.run_gate('graph_list'))
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('No surviving Go packages', result.stdout)

    def test_rename_only_go_file_remains_resolved(self):
        self.git('mv', 'a.go', 'renamed.go')
        self.commit('rename Go source')
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('example.test/gate/renamed.go', result.stdout)
        self.assertIn('No non-test Go lines changed', result.stdout)


if __name__ == '__main__':
    unittest.main(verbosity=2)
