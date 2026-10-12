#!/usr/bin/env python3
"""The full-suite run must give the stall watchdog a window longer than the
slowest Go package takes on a loaded runner (CI test job, main #10371)."""
from pathlib import Path
from unittest import mock
import os
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import coverage_artifact  # noqa: E402


class StallWindow(unittest.TestCase):
    def run_suite(self, env):
        seen = {}

        def fake_run(argv, check, env):
            seen['argv'], seen['env'] = argv, env

        # mock: external process boundary - only the watchdog/go invocation is faked.
        with mock.patch.dict(os.environ, env, clear=True), \
                mock.patch.object(coverage_artifact.subprocess, 'run', fake_run), \
                mock.patch.object(coverage_artifact, 'create'), \
                mock.patch.object(Path, 'unlink'):
            coverage_artifact.run_full_suite(Path('coverage.out'), Path('m.json'))
        return seen

    def test_default_window_exceeds_loaded_runner_package_time(self):
        seen = self.run_suite({})
        self.assertEqual(seen['argv'][:2], ['sh', 'scripts/ci/go-test-stall-watchdog.sh'])
        # 184s was the slowest sibling package observed; the old 180s default killed it.
        # Pin the exact value so a drift of the default is caught, not just a floor.
        self.assertEqual(seen['env']['STALL_SECONDS'], '420')

    def test_explicit_override_is_respected(self):
        self.assertEqual(self.run_suite({'STALL_SECONDS': '9'})['env']['STALL_SECONDS'], '9')


if __name__ == '__main__':
    unittest.main()
