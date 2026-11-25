#!/usr/bin/env python3
"""Fast pre-push CI gates, selected by committed, staged and working-tree paths."""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]


def git(*args: str) -> str:
    return subprocess.check_output(['git', *args], cwd=ROOT, text=True, timeout=30)


def run(*args: str, timeout: int = 120, **env: str) -> None:
    print('── Pre-push gate:', ' '.join(args), flush=True)
    subprocess.run(args, cwd=ROOT, env={**os.environ, **env}, check=True, timeout=timeout)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base', default='')
    args = parser.parse_args()
    try:
        base = args.base or git('merge-base', 'origin/main', 'HEAD').strip()
        paths = set(git('diff', '--name-only', '--no-renames', '-z', base).split('\0'))
        untracked = set(git('ls-files', '--others', '--exclude-standard', '-z').split('\0'))
        paths.update(untracked)
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired, OSError):
        print('ERROR: cannot select pre-push gates: origin/main or diff base unavailable; '
              'run git fetch origin main. Gates did not pass.', file=sys.stderr)
        return 2

    try:
        if any(p.startswith('migrations/') for p in paths):
            if any(p.startswith('migrations/') for p in untracked):
                raise ValueError('stage new migrations with git add before running the migration gate')
            try:
                # Refresh the remote-tracking ref explicitly, even with a narrow fetch config.
                run('git', 'fetch', 'origin', 'main:refs/remotes/origin/main', timeout=30)
                git('rev-parse', '--verify', 'origin/main^{commit}')
            except (subprocess.CalledProcessError, subprocess.TimeoutExpired, OSError):
                print('ERROR: migration gate requires a fresh origin/main; fetch failed '
                      '(network/remote unavailable). Gates did not pass.', file=sys.stderr)
                return 2
            run('bash', 'scripts/check-migration-version.sh', 'migrations', MIGRATION_BASE_REF='main')

        workflow = '.github/workflows/memory-bench.yml'
        text = (ROOT / workflow).read_text()
        block = re.search(r'^  MEMORY_PATHS: \|\n((?:    .*\n)+)', text, re.M)
        if not block:
            raise ValueError('MEMORY_PATHS block missing from ' + workflow)
        prefixes = [line.strip() for line in block[1].splitlines() if line.strip()]
        if (any(p and any(p.startswith(prefix) for prefix in prefixes) for p in paths)
                or any(p.endswith('.go') and 'memor' in Path(p).name.lower() for p in paths)
                or paths.intersection({workflow, '.gitlab-ci.yml', '.gitlab/ci/memory.yml',
                                       '.gitlab/ci/data/memory-scope.yml',
                                       'scripts/memory-bench/test_gate_blindness.py'})):
            run(sys.executable, 'scripts/memory-bench/test_gate_blindness.py', 'TestMemoryPathCoverage')

        if (any(p.endswith('_test.go') for p in paths)
                or paths.intersection({'scripts/ci/go_test_file_deps.txt',
                                       'scripts/ci/go_affected_packages.py',
                                       'scripts/ci/test_go_affected_packages.py'})):
            if any(p.endswith('_test.go') for p in untracked):
                raise ValueError('stage new Go tests with git add before checking file dependencies')
            run(sys.executable, 'scripts/ci/test_go_affected_packages.py')
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired, OSError, ValueError) as exc:
        print(f'ERROR: pre-push gate failed: {exc}', file=sys.stderr)
        return 1
    print('── Selected pre-push gates OK', flush=True)
    return 0


if __name__ == '__main__':
    sys.exit(main())
