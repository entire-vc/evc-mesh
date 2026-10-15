#!/usr/bin/env python3
"""Bind a successful coverage profile to this pipeline and toolchain.

The test job writes the manifest only after its race/atomic run succeeds. The
coverage job verifies it before considering even a legitimately empty Go diff.

The run is the full suite (`./...`) unless the test job was given a package
selection (scripts/ci/go_affected_packages.py). A selection is recorded in the
manifest and must then cover every package that has a changed non-test Go file:
the diff gate measures only those lines, and a profile without them would pass.
"""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

from diff_coverage import instrumented_blocks, parse_profile

FULL_SUITE = ['./...']
TEST_FLAGS = ['-race', '-coverprofile=coverage.out', '-covermode=atomic']
TEST_ARGS = [*FULL_SUITE, *TEST_FLAGS]
FULL_SUITE_STALL_SECONDS = '420'
# Keep the target-feature selectors in step with `go help environment`.
# GOARCH alone does not bind GOARM64, GO386, GORISCV64, etc.; these can change
# selected build tags and executable code without changing the commit or profile.
GO_ENV_KEYS = (
    'GOOS', 'GOARCH', 'GO386', 'GOAMD64', 'GOARM', 'GOARM64',
    'GOMIPS', 'GOMIPS64', 'GOPPC64', 'GORISCV64', 'GOWASM',
    'CGO_ENABLED', 'GOFLAGS', 'GOEXPERIMENT', 'GOFIPS140',
    'GO_EXTLINK_ENABLED', 'GOTOOLCHAIN', 'GOWORK',
)


def command(*args: str) -> str:
    try:
        return subprocess.run(args, check=True, capture_output=True,
                              text=True).stdout.strip()
    except (OSError, subprocess.CalledProcessError) as exc:
        raise ValueError(f'could not inspect {args[0]} toolchain or checkout') from exc


def contract(packages: list[str] | None = None) -> dict:
    sha = os.environ.get('CI_COMMIT_SHA', '')
    pipeline = os.environ.get('CI_PIPELINE_ID', '')
    if not re.fullmatch(r'[0-9a-f]{40}|[0-9a-f]{64}', sha) or not pipeline.isdecimal():
        raise ValueError('CI_COMMIT_SHA or CI_PIPELINE_ID is absent or invalid')
    if command('git', 'rev-parse', 'HEAD') != sha:
        raise ValueError('checkout HEAD differs from CI_COMMIT_SHA')
    return {
        'schema': 1,
        'commit_sha': sha,
        'pipeline_id': pipeline,
        'go_version': command('go', 'version'),
        'go_env': json.loads(command('go', 'env', '-json', *GO_ENV_KEYS)),
        'test_args': [*(packages or FULL_SUITE), *TEST_FLAGS],
    }


def read_selection(path: Path) -> list[str]:
    """`ALL`, or the import paths go_affected_packages.py chose, one per line."""
    lines = [ln.strip() for ln in path.read_text(encoding='utf-8').splitlines() if ln.strip()]
    if not lines:
        raise ValueError('package selection is empty: the test job must run something')
    if 'ALL' in lines:
        if lines != ['ALL']:
            raise ValueError('package selection mixes ALL with package names')
        return list(FULL_SUITE)
    if any(ln.startswith('-') or ' ' in ln or '\t' in ln for ln in lines):
        raise ValueError('package selection holds something that is not an import path')
    return lines


def profile_data(path: Path, packages: list[str] | None = None) -> bytes:
    data = path.read_bytes()
    if not data.startswith(b'mode: atomic\n'):
        raise ValueError('test profile is not atomic')
    blocks = parse_profile(str(path), '')
    if not any(blocks.values()):
        raise ValueError('test profile has no measured blocks')
    # A valid, hash-matched subset of the profile would otherwise pass a
    # docs-only diff: diff_coverage has no changed blocks to inspect there.
    # Resolve the active package files with the same race/build-tag selection
    # as the full suite, then require every executable block in its profile.
    listed = command('go', 'list', '-race', '-json', *(packages or FULL_SUITE))
    decoder = json.JSONDecoder()
    offset = 0
    missing = []
    packages = 0
    while offset < len(listed):
        while offset < len(listed) and listed[offset].isspace():
            offset += 1
        if offset == len(listed):
            break
        package, offset = decoder.raw_decode(listed, offset)
        packages += 1
        import_path = package['ImportPath']
        directory = Path(package['Dir'])
        for name in package.get('GoFiles', []) + package.get('CgoFiles', []):
            file_path = f'{import_path}/{name}'
            actual = {block[:5] for block in blocks.get(file_path, [])}
            for block in instrumented_blocks(str(directory / name), str(directory)):
                if block[4] > 0 and block not in actual:
                    missing.append(f'{file_path}:{block[0]}-{block[2]}')
                    if len(missing) >= 10:
                        break
            if len(missing) >= 10:
                break
        if len(missing) >= 10:
            break
    if packages == 0:
        raise ValueError('go list returned no packages for full-suite profile')
    if missing:
        raise ValueError(f'missing full-suite coverage blocks: {missing}')
    return data


def create(profile: Path, manifest: Path, packages: list[str] | None = None, reason: str = '') -> None:
    expected = contract(packages)
    if reason:
        expected['selection_reason'] = reason
    data = profile_data(profile, packages)
    expected.update(profile_sha256=hashlib.sha256(data).hexdigest(),
                    profile_size=len(data))
    manifest.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(mode='w', encoding='utf-8',
                                     dir=manifest.parent, prefix='.coverage-manifest-',
                                     delete=False) as stream:
        temp_name = stream.name
        json.dump(expected, stream, sort_keys=True)
        stream.write('\n')
    os.replace(temp_name, manifest)


def run_full_suite(profile: Path, manifest: Path, packages: list[str] | None = None,
                   reason: str = '') -> None:
    # The invocation and manifest contract share TEST_FLAGS: a CI edit cannot
    # silently remove -race while still claiming it. `packages` is None for the
    # whole suite, or the import paths of a diff-based selection.
    args = [*(packages or FULL_SUITE), *TEST_FLAGS]
    if profile != Path('coverage.out'):
        raise ValueError('full suite must write coverage.out')
    manifest.unlink(missing_ok=True)
    # A successful runner that fails to emit a profile must not certify bytes
    # left behind by an earlier checkout or invocation.
    profile.unlink(missing_ok=True)
    # `go test ./...` prints a package's result only when that package finishes,
    # so the watchdog's quiet window is "longest package, started alongside its
    # siblings". internal/repository/postgres and internal/handler run ~85s each
    # on an idle runner but took 140-184s on a loaded one (main #10371, !1166
    # #10368: stack showed a live DB call in [IO wait] under a minute, not a
    # wedge), and the default 180s window killed healthy runs. A real wedge is
    # minutes of nothing until the job timeout, so 420s still catches it.
    env = dict(os.environ)
    env.setdefault('STALL_SECONDS', FULL_SUITE_STALL_SECONDS)
    subprocess.run(['sh', 'scripts/ci/go-test-stall-watchdog.sh', *args],
                   check=True, env=env)
    create(profile, manifest, packages, reason)


def changed_measured_packages(base: str, listed_packages: list[dict]) -> dict[str, str]:
    """Packages that own a changed non-test Go file, with one such file each.

    These are the packages whose lines the diff gate will look up in the profile.
    A file excluded by build constraints has no blocks and is not measured.
    """
    diff = command('git', 'diff', '--name-only', '--no-renames', base, 'HEAD', '--', '*.go')
    owner: dict[str, str] = {}
    for package in listed_packages:
        directory = Path(package['Dir'])
        try:
            relative = directory.relative_to(Path.cwd())
        except ValueError:
            continue
        for name in package.get('GoFiles', []) + package.get('CgoFiles', []):
            owner[(relative / name).as_posix()] = package['ImportPath']
    return {owner[f]: f for f in diff.splitlines() if f in owner}


def describe(manifest: Path) -> str:
    """The selection the test job ran, for the log of the job that consumes the manifest."""
    recorded = json.loads(manifest.read_text(encoding='utf-8'))
    packages = recorded['test_args'][:-len(TEST_FLAGS)]
    reason = recorded.get('selection_reason') or 'selection: (no reason recorded)'
    listing = 'the whole suite (./...)' if packages == FULL_SUITE else f'{len(packages)}: ' + ' '.join(packages)
    return f'{reason}\nselected packages {listing}'


def verify(profile: Path, manifest: Path, base: str | None = None) -> None:
    recorded = json.loads(manifest.read_text(encoding='utf-8'))
    if not isinstance(recorded, dict):
        raise ValueError('coverage manifest is not an object')
    args = recorded.get('test_args')
    if not isinstance(args, list) or args[-len(TEST_FLAGS):] != TEST_FLAGS:
        raise ValueError('coverage manifest test_args differs from this job')
    packages = args[:-len(TEST_FLAGS)]
    if not packages or not all(isinstance(p, str) and p and not p.startswith('-') for p in packages):
        raise ValueError('coverage manifest records no package list')
    subset = packages != FULL_SUITE
    expected = contract(packages if subset else None)
    for field, value in expected.items():
        if recorded.get(field) != value:
            raise ValueError(f'coverage manifest {field} differs from this job')
    data = profile_data(profile, packages if subset else None)
    if recorded.get('profile_size') != len(data):
        raise ValueError('coverage profile size differs from successful test artifact')
    if recorded.get('profile_sha256') != hashlib.sha256(data).hexdigest():
        raise ValueError('coverage profile hash differs from successful test artifact')
    if subset:
        # The profile is complete for the packages it names; whether those are the
        # right ones is decided here, independently of the test job's own selection.
        if not base:
            raise ValueError('a package selection can only be verified against a diff base')
        listed = list(split_json(command('go', 'list', '-race', '-json', *FULL_SUITE)))
        known = {p['ImportPath'] for p in listed}
        unknown = [p for p in packages if p not in known]
        if unknown:
            raise ValueError(f'selection names packages this module does not have: {unknown[:3]}')
        uncovered = {pkg: file for pkg, file in changed_measured_packages(base, listed).items()
                     if pkg not in packages}
        if uncovered:
            raise ValueError('selection omits packages with changed Go files: '
                             + ', '.join(f'{pkg} ({file})' for pkg, file in sorted(uncovered.items())[:3]))


def split_json(text: str):
    """The concatenated JSON objects `go list -json` prints."""
    decoder, offset = json.JSONDecoder(), 0
    while offset < len(text):
        while offset < len(text) and text[offset].isspace():
            offset += 1
        if offset == len(text):
            break
        obj, end = decoder.raw_decode(text, offset)
        yield obj
        offset = end


if __name__ == '__main__':
    try:
        usage = ('usage: coverage_artifact.py run PROFILE MANIFEST [SELECTION_FILE] | '
                 'create PROFILE MANIFEST | verify PROFILE MANIFEST [DIFF_BASE] | describe MANIFEST')
        if sys.argv[1:2] == ['describe'] and len(sys.argv) == 3:
            print(describe(Path(sys.argv[2])))
            sys.exit(0)
        if len(sys.argv) not in (4, 5) or sys.argv[1] not in ('run', 'create', 'verify'):
            raise ValueError(usage)
        profile_path, manifest_path = map(Path, sys.argv[2:4])
        extra = sys.argv[4] if len(sys.argv) == 5 else None
        if sys.argv[1] == 'run':
            selection = read_selection(Path(extra)) if extra else None
            reason_file = Path(extra + '.reason') if extra else None
            reason = (reason_file.read_text(encoding='utf-8').strip()
                      if reason_file and reason_file.exists() else '')
            run_full_suite(profile_path, manifest_path,
                           None if selection == FULL_SUITE else selection, reason)
        elif extra:
            if sys.argv[1] == 'create':
                raise ValueError(usage)
            verify(profile_path, manifest_path, extra)
        elif sys.argv[1] == 'create':
            create(profile_path, manifest_path)
        else:
            verify(profile_path, manifest_path)
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as exc:
        sys.exit(f'ERROR: invalid coverage artifact: {exc}')
