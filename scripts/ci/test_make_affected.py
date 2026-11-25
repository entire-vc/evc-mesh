#!/usr/bin/env python3
"""Checks for `make test-affected` / `make test-pkg` (the pre-push package run).

    python3 scripts/ci/test_make_affected.py

Runs the real Makefile recipes with stand-ins only at the process boundary: the
package selector, `docker`, `go` and `goose` are small scripts on PATH that log
how they were called. What is checked:

  - ALL from the selector: the CI line is printed, exit 0, no stack is started and
    no test runs;
  - an empty selection: exit 0, nothing started;
  - selected packages: the throwaway stack comes up, the migrations run, `go test
    -race -count=1` gets exactly those packages and the stack's DSNs, and the stack
    is removed (`down -v`) afterwards;
  - a failing test: make exits non-zero, the failing test's name is in the output,
    and the stack is still removed;
  - a failing `compose up`: non-zero, and the half-started stack is removed;
  - SIGINT (Ctrl+C) or SIGTERM (a limiter timeout) during the tests: the stack is
    still removed, within the 15 s before a follow-up SIGKILL;
  - a second SIGTERM while compose down is running cannot interrupt cleanup;
  - two checkouts with the same directory name (every lane's `slot-2`) get
    different compose projects, and up/down address this checkout's own one;
  - host ports are docker's choice (`up` with port 0) and the tests and the
    migrations get the ports `compose port` reports, never a computed offset
    that another checkout can also draw.

Red control: a recipe without the cleanup trap must fail the removal checks (it is
built here by stripping the trap from a copy of the Makefile).

Requires: make, python3. Stdlib only.
"""
from __future__ import annotations

import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FAILED: list[str] = []

STUB_DOCKER = r'''#!/bin/sh
# Compose's own precedence: the shell environment, then the .env next to the file.
proj="$COMPOSE_PROJECT_NAME"
if [ -z "$proj" ] && [ -f deploy/docker/mesh/.env ]; then
  proj=$(sed -n 's/^COMPOSE_PROJECT_NAME=//p' deploy/docker/mesh/.env)
fi
echo "docker $* project=$proj db=$DB_PORT" >> "$STUB_LOG"
case "$*" in
  *" down -v")
    echo "down-started" >> "$STUB_LOG"
    if [ -n "$STUB_DOWN_DELAY" ]; then sleep "$STUB_DOWN_DELAY"; fi
    echo "down-completed" >> "$STUB_LOG" ;;
  *" port postgres 5432") echo "0.0.0.0:41001" ;;
  *" port redis 6379") echo "0.0.0.0:41002" ;;
  *" port nats 4222") echo "0.0.0.0:41003" ;;
  *" up "*) [ -n "$STUB_UP_FAIL" ] && exit 1 ;;
esac
exit 0
'''

STUB_GO = r'''#!/bin/sh
if [ "$1" = env ]; then echo /nonexistent-gopath; exit 0; fi
echo "go $* DATABASE_URL=$DATABASE_URL TEST_DATABASE_URL=$TEST_DATABASE_URL REDIS_URL=$REDIS_URL NATS_URL=$NATS_URL" >> "$STUB_LOG"
if [ -n "$STUB_GO_SLEEP" ]; then echo "go-test-started" >> "$STUB_LOG"; sleep "$STUB_GO_SLEEP"; fi
if [ -n "$STUB_GO_FAIL" ]; then echo "--- FAIL: TestStubRed (0.00s)"; exit 1; fi
echo "ok  	stub"
'''

STUB_GOOSE = r'''#!/bin/sh
echo "goose $* DBSTRING=$GOOSE_DBSTRING" >> "$STUB_LOG"
'''

STUB_LINT = r'''#!/bin/sh
echo "golangci-lint has version 2.14.0"
'''


def write_exec(path: Path, body: str) -> None:
    path.write_text(body)
    path.chmod(0o755)


class Env:
    def __init__(self, makefile: Path):
        self.tmp = Path(tempfile.mkdtemp(prefix='make-affected-'))
        self.bin = self.tmp / 'bin'
        self.bin.mkdir()
        self.log = self.tmp / 'calls.log'
        self.log.touch()
        self.selection = self.tmp / 'selection.txt'
        write_exec(self.bin / 'docker', STUB_DOCKER)
        write_exec(self.bin / 'go', STUB_GO)
        write_exec(self.bin / 'goose', STUB_GOOSE)
        write_exec(self.bin / 'golangci-lint', STUB_LINT)
        write_exec(self.bin / 'selector', f'#!/bin/sh\ncat "{self.selection}"\n'
                   'echo "selection: stub" >&2\n')
        # The checkout the recipes run in: a copy with only what they touch, so the
        # .env and marker files land in the temp dir, never in the real tree.
        self.repo = self.tmp / 'repo'
        (self.repo / 'deploy/docker/mesh').mkdir(parents=True)
        (self.repo / 'scripts/ci').mkdir(parents=True)
        shutil.copy(makefile, self.repo / 'Makefile')
        (self.repo / 'deploy/docker/mesh/docker-compose.yml').write_text('services: {}\n')
        # The reaper is best-effort (`|| true`); a no-op here keeps the log about us.
        (self.repo / 'scripts/ci/reap_stale_ci_containers.py').write_text('')

    def args(self, target: str) -> list[str]:
        return ['make', '--no-print-directory', '-C', str(self.repo), target,
                'AFFECTED_BASE=HEAD',
                f'GO_AFFECTED_SELECTOR={self.bin / "selector"}',
                f'GOLANGCI_LINT={self.bin / "golangci-lint"}',
                f'GOOSE={self.bin / "goose"}']

    def env(self, **extra: str) -> dict[str, str]:
        env = {k: v for k, v in os.environ.items() if not k.startswith(('MAKE', 'MFLAGS'))}
        env.update(PATH=f'{self.bin}{os.pathsep}{env.get("PATH", "")}', STUB_LOG=str(self.log))
        env.update(extra)
        return env

    def run(self, target: str, selection: str, **extra: str) -> subprocess.CompletedProcess:
        self.selection.write_text(selection)
        self.log.write_text('')
        return subprocess.run(self.args(target), env=self.env(**extra), text=True,
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=120)

    def calls(self) -> list[str]:
        return self.log.read_text().splitlines()


def check(name: str, ok: bool, detail: str = '') -> None:
    print(f'{"ok  " if ok else "FAIL"} {name}' + (f'\n     {detail}' if not ok and detail else ''))
    if not ok:
        FAILED.append(name)


def downs(calls: list[str]) -> int:
    return sum(1 for c in calls if re.match(r'docker compose .* down -v ', c))


def ups(calls: list[str]) -> int:
    return sum(1 for c in calls if re.match(r'docker compose .* up ', c))


def cases(makefile: Path, prefix: str = '') -> None:
    e = Env(makefile)
    try:
        r = e.run('test-affected', 'ALL\n')
        check(f'{prefix}ALL: exit 0', r.returncode == 0, r.stdout)
        check(f'{prefix}ALL: says the full suite runs in CI',
              'ALL — the full suite runs in CI' in r.stdout, r.stdout)
        check(f'{prefix}ALL: no stack, no go test', e.calls() == [], '\n'.join(e.calls()))

        r = e.run('test-affected', '')
        check(f'{prefix}empty: exit 0', r.returncode == 0, r.stdout)
        check(f'{prefix}empty: nothing started', e.calls() == [], '\n'.join(e.calls()))

        r = e.run('test-affected', 'example.com/m/internal/a\nexample.com/m/internal/b\n')
        calls = e.calls()
        gotest = [c for c in calls if c.startswith('go test')]
        check(f'{prefix}packages: exit 0', r.returncode == 0, r.stdout)
        check(f'{prefix}packages: stack up once', ups(calls) == 1, '\n'.join(calls))
        check(f'{prefix}packages: migrations before tests',
              any(c.startswith('goose -dir migrations up') for c in calls)
              and calls.index(next(c for c in calls if c.startswith('goose'))) <
              calls.index(gotest[0]) if gotest else False, '\n'.join(calls))
        check(f'{prefix}packages: go test -race -count=1 on exactly the selection',
              len(gotest) == 1 and ' -race -count=1 ' in gotest[0]
              and 'example.com/m/internal/a example.com/m/internal/b DATABASE_URL=' in gotest[0],
              '\n'.join(gotest))
        check(f'{prefix}packages: DSNs use the ports docker assigned',
              bool(gotest) and 'DATABASE_URL=postgres://mesh:mesh@localhost:41001/mesh' in gotest[0]
              and 'TEST_DATABASE_URL=postgres://mesh:mesh@localhost:41001/mesh' in gotest[0]
              and 'REDIS_URL=redis://localhost:41002' in gotest[0]
              and 'NATS_URL=nats://localhost:41003' in gotest[0], '\n'.join(gotest))
        check(f'{prefix}packages: migrations on the docker-assigned port',
              any(c.startswith('goose') and 'localhost:41001/' in c for c in calls), '\n'.join(calls))
        check(f'{prefix}packages: up leaves the host ports to docker (port 0)',
              any(re.match(r'docker compose .* up .* db=0$', c) for c in calls), '\n'.join(calls))
        check(f'{prefix}packages: stack removed', downs(calls) == 1, '\n'.join(calls))
        projects = {m.group(1) for c in calls for m in [re.search(r' project=(\S+) db=', c)] if m}
        check(f'{prefix}packages: up and down address one project of this checkout',
              len(projects) == 1 and re.fullmatch(r'mesh-ci-repo-[0-9a-f]{6}', next(iter(projects))) is not None,
              repr(projects))
        e.project = next(iter(projects), '')

        r = e.run('test-affected', 'example.com/m/internal/a\n', STUB_GO_FAIL='1')
        check(f'{prefix}red test: exit non-zero', r.returncode != 0, r.stdout)
        check(f'{prefix}red test: names the test', 'TestStubRed' in r.stdout, r.stdout)
        check(f'{prefix}red test: stack removed', downs(e.calls()) == 1, '\n'.join(e.calls()))

        r = e.run('test-pkg', '', STUB_UP_FAIL='1')
        check(f'{prefix}test-pkg without PKG: refused', r.returncode != 0 and 'usage' in r.stdout, r.stdout)

        e.selection.write_text('')
        e.log.write_text('')
        r = subprocess.run(e.args('test-pkg') + ['PKG=./x'], env=e.env(STUB_UP_FAIL='1'), text=True,
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=120)
        calls = e.calls()
        check(f'{prefix}compose up fails: exit non-zero', r.returncode != 0, r.stdout)
        check(f'{prefix}compose up fails: no go test', not any(c.startswith('go test') for c in calls),
              '\n'.join(calls))
        check(f'{prefix}compose up fails: stack removed', downs(calls) == 1, '\n'.join(calls))

        # Ctrl+C: the terminal sends SIGINT to the whole foreground process group.
        # SIGTERM: a run limiter's timeout or a killed session terminates the group
        # and follows with SIGKILL after a grace period (15 s on our hosts), so the
        # removal has to start at once, not after the tests would have finished.
        for name, sig in (('Ctrl+C', signal.SIGINT), ('SIGTERM', signal.SIGTERM),
                          ('SIGTERM during cleanup', signal.SIGTERM)):
            e.selection.write_text('example.com/m/internal/a\n')
            e.log.write_text('')
            repeated = name == 'SIGTERM during cleanup'
            p = subprocess.Popen(e.args('test-affected'),
                                 env=e.env(STUB_GO_SLEEP='60', STUB_DOWN_DELAY='0.5' if repeated else ''), text=True,
                                 stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                 start_new_session=True)
            deadline = time.time() + 60
            while 'go-test-started' not in e.log.read_text() and time.time() < deadline:
                time.sleep(0.1)
            started = 'go-test-started' in e.log.read_text()
            sent = time.time()
            os.killpg(p.pid, sig)
            second_sent = False
            if repeated:
                # GNU make also forwards termination to its recipe: a second
                # signal can arrive while the EXIT trap is running compose down.
                deadline = time.monotonic() + 2
                while 'down-started' not in e.log.read_text() and time.monotonic() < deadline:
                    time.sleep(0.01)
                if 'down-started' in e.log.read_text():
                    try:
                        os.killpg(p.pid, sig)
                        second_sent = True
                    except ProcessLookupError:
                        pass
            try:
                out, _ = p.communicate(timeout=60)
            except subprocess.TimeoutExpired:
                os.killpg(p.pid, signal.SIGKILL)
                out, _ = p.communicate()
            took = time.time() - sent
            calls = e.calls()
            check(f'{prefix}{name}: tests had started', started, '\n'.join(calls))
            check(f'{prefix}{name}: exit non-zero', p.returncode != 0, out)
            if repeated:
                check(f'{prefix}{name}: second signal sent', second_sent, out)
            check(f'{prefix}{name}: stack removed', downs(calls) == 1 and 'down-completed' in calls,
                  '\n'.join(calls) + '\n' + out)
            check(f'{prefix}{name}: done within the 15 s grace ({took:.1f} s)', took < 15, out)
        other = Env(makefile)
        try:
            other.run('test-affected', 'example.com/m/internal/a\n')
            theirs = {m.group(1) for c in other.calls() for m in [re.search(r' project=(\S+) db=', c)] if m}
        finally:
            shutil.rmtree(other.tmp, ignore_errors=True)
        check(f'{prefix}same directory name, other checkout: other project',
              len(theirs) == 1 and e.project not in theirs, f'{e.project!r} vs {theirs!r}')
    finally:
        shutil.rmtree(e.tmp, ignore_errors=True)


def main() -> int:
    makefile = ROOT / 'Makefile'
    cases(makefile)
    ok = not FAILED

    # Red control: the same recipe without its cleanup trap must fail the removal checks.
    text = makefile.read_text()
    broken, n = re.subn(r"@trap '[^']*\$\(TEST_PKG_COMPOSE\) down -v;[^']*' EXIT; \\\n",
                        '@', text, count=1)
    if n != 1:
        print('FAIL red control: could not find the test-pkg cleanup trap to strip')
        return 1
    with tempfile.TemporaryDirectory() as d:
        bm = Path(d) / 'Makefile'
        bm.write_text(broken)
        FAILED.clear()
        print('-- red control (cleanup trap removed; removal checks must fail) --')
        cases(bm, prefix='[red] ')
        red_caught = any('stack removed' in f for f in FAILED)
    print('red control:', 'caught' if red_caught else 'NOT CAUGHT')
    return 0 if ok and red_caught else 1


if __name__ == '__main__':
    sys.exit(main())
