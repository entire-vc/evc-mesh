#!/usr/bin/env python3
"""Bind a successful full-suite coverage profile to this pipeline and toolchain.

The test job writes the manifest only after its race/atomic suite succeeds. The
coverage job verifies it before considering even a legitimately empty Go diff.
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

TEST_ARGS = ['./...', '-race', '-coverprofile=coverage.out', '-covermode=atomic']
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


def contract() -> dict:
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
        'test_args': TEST_ARGS,
    }


def profile_data(path: Path) -> bytes:
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
    listed = command('go', 'list', '-race', '-json', './...')
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


def create(profile: Path, manifest: Path) -> None:
    expected = contract()
    data = profile_data(profile)
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


def run_full_suite(profile: Path, manifest: Path) -> None:
    # The invocation and manifest contract share TEST_ARGS: a CI edit cannot
    # silently remove -race or change the package set while still claiming it.
    if profile != Path('coverage.out'):
        raise ValueError('full suite must write coverage.out')
    manifest.unlink(missing_ok=True)
    # A successful runner that fails to emit a profile must not certify bytes
    # left behind by an earlier checkout or invocation.
    profile.unlink(missing_ok=True)
    subprocess.run(['sh', 'scripts/ci/go-test-stall-watchdog.sh', *TEST_ARGS],
                   check=True)
    create(profile, manifest)


def verify(profile: Path, manifest: Path) -> None:
    expected = contract()
    recorded = json.loads(manifest.read_text(encoding='utf-8'))
    if not isinstance(recorded, dict):
        raise ValueError('coverage manifest is not an object')
    for field, value in expected.items():
        if recorded.get(field) != value:
            raise ValueError(f'coverage manifest {field} differs from this job')
    data = profile_data(profile)
    if recorded.get('profile_size') != len(data):
        raise ValueError('coverage profile size differs from successful test artifact')
    if recorded.get('profile_sha256') != hashlib.sha256(data).hexdigest():
        raise ValueError('coverage profile hash differs from successful test artifact')


if __name__ == '__main__':
    try:
        if len(sys.argv) != 4 or sys.argv[1] not in ('run', 'create', 'verify'):
            raise ValueError('usage: coverage_artifact.py run|create|verify PROFILE MANIFEST')
        profile_path, manifest_path = map(Path, sys.argv[2:])
        if sys.argv[1] == 'run':
            run_full_suite(profile_path, manifest_path)
        elif sys.argv[1] == 'create':
            create(profile_path, manifest_path)
        else:
            verify(profile_path, manifest_path)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        sys.exit(f'ERROR: invalid coverage artifact: {exc}')
