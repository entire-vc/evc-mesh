#!/usr/bin/env python3
"""Give the bench corpus an AGE, so age-dependent ranking is measurable at all.

    python fixture_ages.py --selftest        # offline; no Mesh, no database

The hole this closes
--------------------
`remember` has no `created_at` field (`rememberRequest`,
`internal/handler/memory_handler.go`), so every fixture the bench writes is born
"now" and the whole haystack shares one age to within the ingest window —
`STORE_CONCURRENCY = 1`, so 42-54 sequential stores, seconds apart.

Recall multiplies each score by `exp(-Δt·ln2/half_life)`. At equal ages that is a
common factor: it scales every score and reorders nothing. Measured on the live
corpus (#4772db75):

    perturbation from decay across a 60 s ingest window : 0.0069 %
                                             300 s      : 0.0344 %
    smallest gap between adjacent RRF scores            : 0.0534 %
    median gap                                          : 2.280  %

So the age signal sat one to two orders of magnitude BELOW the granularity of
the ranking it is supposed to move. Every age-dependent mechanism —
`apply_recency_decay`, `half_life_days`, `recency_weight`, and any temporal
profile built on them — was invisible to this harness in both directions: a
change that fixed one and a change that broke one produced the same number.

Two columns, not one
--------------------
The card said "decay is computed from `created_at`". That is true of two of the
three mechanisms and false of the third, so backdating only `created_at` would
have fixed the harness's blindness by two thirds and looked complete:

    apply_recency_decay   memory_service.go     `now - m.CreatedAt`
    decayed_relevance     memory_repo.go (SQL)  `NOW() - created_at`
    recency_weight        memory_repo.go        `now - rows[i].UpdatedAt`   <-- !

`mesh_client_stdio._search` already forwards `BENCH_RECENCY_WEIGHT`, so that
third mechanism is reachable from the bench today and would have stayed blind.
Both columns are therefore set to the same instant. There is no meaning to a
fixture whose two timestamps disagree — it was never edited.

Why the anchor is `question_date` and not the literal dataset date
------------------------------------------------------------------
LongMemEval dates are 2023; the bench runs in 2026. Writing them literally puts
the whole corpus ~1100 days back, which at a 30-day half-life is a common factor
of ~1e-11 — arithmetically different from today's common factor of ~1.0 and
behaviourally identical to it, because a common factor still reorders nothing.
It would also drive every score into float noise, which is a new failure mode
bought for no signal.

The ages the dataset actually asserts are the ones the QUESTION sees: it is
asked at `question_date`, about sessions that happened before it. So the whole
timeline is shifted by `now - question_date`, preserving every relative age
exactly. Measured over all 24 questions / 1147 sessions: min 0.00 d, median
7.3 d, max 183.1 d, and zero sessions dated after their own question — the
shift needs no special case in practice, and clamps rather than inverting when
one appears (a future `created_at` would make `exp(-λ·Δt)` a BOOST; the FTS
blend clamps it, the hybrid path does not).

Resulting decay spread: 2^(11/30) = 1.29 for a typical category, up to 2^(178/30)
= 60 for temporal-reasoning — against the 0.0534 % that ranking can resolve.

Safety: this writes SQL to a database, so it refuses a non-local DSN
--------------------------------------------------------------------
Backdating is only possible because the branch arm owns an ephemeral postgres.
The prod arm measures `mesh.entire.host` and must never be touched this way —
so the guard is not "the prod arm does not pass a DSN", which is a property of a
YAML file someone can edit, but `assert_local_dsn`, which refuses any host that
is not loopback. A copy-pasted prod DSN fails closed here rather than rewriting
timestamps in the fleet's live memory.
"""

from __future__ import annotations

import argparse
import os
import re
import shutil
import statistics
import subprocess
import sys
from datetime import datetime, timezone

# `ingest-now` is the historical regime: every fixture aged "now". It stays the
# DEFAULT so that nothing — the prod arm, a local run, an old committed baseline
# — changes behaviour by merely upgrading this file. Backdating is opt-in.
AGE_MODE_NOW = "ingest-now"
AGE_MODE_ANCHORED = "question-anchored"
AGE_MODES = (AGE_MODE_NOW, AGE_MODE_ANCHORED)

ENV_AGE_MODE = "BENCH_FIXTURE_AGE_MODE"
ENV_BACKDATE_DSN = "BENCH_BACKDATE_DSN"
ENV_APPLY_DECAY = "BENCH_APPLY_RECENCY_DECAY"

# ---------------------------------------------------------------------------
# MIRROR of evc-mesh-mcp's recall auto-classifier. NOT the authority.
#
# `evc-mesh-mcp/internal/mcp/tools.go` classifies EVERY recall by its query
# text and then
# does `if pp.ApplyDecay { applyDecay = true }` — a profile can turn decay ON
# over the caller's explicit `false`, and `ProfileTemporal` also forces
# `order_by=decayed_relevance:desc` and a **7-day** half-life instead of 30.
#
# Two consequences this mirror exists to make visible:
#
#   1. Which scored questions the ages can move at all. Measured over
#      `data/lme_s_24.json`: 2 of 24 questions trip `temporalKeywords`
#      (`031748ae`, "...when I just started..."; `9a707b81`, "How many days ago
#      ...") and are therefore age-ranked the moment the corpus has ages. The
#      other 22 run with decay off, so backdating cannot move them. Reading a
#      backdated run without knowing that would attribute any movement to the
#      wrong 22 questions.
#   2. A control that toggles `apply_recency_decay` is only measuring the toggle
#      if its own query does NOT trip the classifier. A temporal-sounding
#      control query gets decay forced on in BOTH arms, both arms agree, and the
#      negative control passes for a reason that has nothing to do with age.
#      `recency_control.py --selftest` asserts its query is clean against this
#      list, which is the only reason that assertion can be trusted.
#
# A mirror can go stale, and stale in the dangerous direction is "we think this
# query is neutral and the server disagrees". It is checked the only way this
# repo can check it: `test_fixture_ages.py` pins the list against the shape
# documented here, and every consumer says out loud that it is a mirror.
# ---------------------------------------------------------------------------
MCP_TEMPORAL_KEYWORDS = (
    "when", "ago", "yesterday", "last week", "recently", "before", "after",
)
_MCP_DATE_RE = re.compile(r"(?<![a-z0-9])20\d\d-")


def trips_temporal_profile(query: str) -> bool:
    """Would evc-mesh-mcp auto-classify this query as `temporal`?

    True means: decay ON regardless of what the caller asked for, half-life 7
    days, ordered by `decayed_relevance`. See the mirror note above.
    """
    lowered = (query or "").lower()
    if any(kw in lowered for kw in MCP_TEMPORAL_KEYWORDS):
        return True
    return bool(_MCP_DATE_RE.search(lowered))


def resolve_apply_decay(explicit: bool | None = None) -> bool | None:
    """Whether to SEND `apply_recency_decay`, and with what value.

    Three states, all meaningful: `None` = do not send the parameter (the server
    then applies its own default, which in evc-mesh-mcp is `false` unless the
    query trips a profile); `True`/`False` = send exactly that. Collapsing None
    into a boolean would silently change the regime of every existing run.
    """
    if explicit is not None:
        return explicit
    raw = os.environ.get(ENV_APPLY_DECAY, "").strip().lower()
    if not raw:
        return None
    if raw in ("1", "true", "yes"):
        return True
    if raw in ("0", "false", "no"):
        return False
    raise ValueError(
        f"{ENV_APPLY_DECAY}={raw!r} is neither true nor false; refusing to guess "
        "which ranking regime was intended"
    )

# "2023/05/10 (Wed) 01:57" — the LongMemEval date shape. The weekday is
# decoration and is not parsed: it is redundant with the date and, being
# redundant, is the part most likely to be wrong in a hand-edited fixture.
_LME_DATE_RE = re.compile(
    r"^\s*(\d{4})/(\d{2})/(\d{2})\s*(?:\([A-Za-z]{3}\)\s*)?(\d{2}):(\d{2})\s*$"
)

# Keys are already constrained to this shape server-side (`remember` 400s
# otherwise, which is how two questions silently died for six days — #dea7a367).
# Re-checked here because these strings are interpolated into SQL: the validator
# is what makes that interpolation safe, so it must live next to the SQL and not
# be inherited on trust from a caller.
_KEY_RE = re.compile(r"^[a-z0-9][a-z0-9-]*[a-z0-9]$")

_LOOPBACK_HOSTS = frozenset({"localhost", "127.0.0.1", "::1", "[::1]", ""})


class BackdateError(RuntimeError):
    """Backdating was requested and did not happen.

    Always raised, never logged-and-continued. A run that asked for aged
    fixtures and silently got "now" is the exact state this module exists to
    end, and it would be indistinguishable from a working one in every artifact.
    """


def resolve_age_mode(explicit: str | None = None) -> str:
    """Which age regime this process runs under: explicit arg, else env, else now.

    An unrecognised value is a hard error rather than a fallback to the default.
    A typo in `BENCH_FIXTURE_AGE_MODE` must not quietly restore the blindness —
    that failure would present as "the fix did not work", six months after
    anyone remembers there is a spelling to get right.
    """
    raw = (explicit if explicit is not None else os.environ.get(ENV_AGE_MODE, "")).strip()
    if not raw:
        return AGE_MODE_NOW
    if raw not in AGE_MODES:
        raise ValueError(
            f"unknown fixture age mode {raw!r}; expected one of {', '.join(AGE_MODES)}"
        )
    return raw


def parse_lme_timestamp(raw: str) -> datetime:
    """`"2023/05/10 (Wed) 01:57"` -> aware UTC datetime."""
    m = _LME_DATE_RE.match(raw or "")
    if not m:
        raise ValueError(f"unparseable LongMemEval timestamp: {raw!r}")
    year, month, day, hour, minute = (int(g) for g in m.groups())
    return datetime(year, month, day, hour, minute, tzinfo=timezone.utc)


def target_timestamps(
    dates: list[str], question_date: str, now: datetime
) -> tuple[list[datetime], int]:
    """Session dates -> the instants to stamp, shifted so `question_date` == now.

    Returns `(timestamps, clamped)`. `clamped` counts sessions dated AFTER their
    own question, which would otherwise land in the future: `exp(-λ·Δt)` with a
    negative Δt is greater than 1, so the hybrid path would BOOST them (only the
    FTS blend clamps). Zero in the shipped dataset; carried because a future
    dataset that lands one should get a bounded fixture and a number in the log,
    not a silent amplifier.
    """
    anchor = parse_lme_timestamp(question_date)
    shift = now - anchor
    out: list[datetime] = []
    clamped = 0
    for raw in dates:
        ts = parse_lme_timestamp(raw) + shift
        if ts > now:
            ts = now
            clamped += 1
        out.append(ts)
    return out, clamped


def age_summary(timestamps: list[datetime], now: datetime) -> dict[str, float]:
    """min / median / max age in days, plus n. Empty input -> zeros, not a crash."""
    if not timestamps:
        return {"n": 0, "min_days": 0.0, "median_days": 0.0, "max_days": 0.0}
    ages = sorted((now - ts).total_seconds() / 86400.0 for ts in timestamps)
    return {
        "n": len(ages),
        "min_days": ages[0],
        "median_days": statistics.median(ages),
        "max_days": ages[-1],
    }


def format_age_summary(summary: dict[str, float]) -> str:
    return (
        f"n={int(summary['n'])} "
        f"min={summary['min_days']:.2f}d "
        f"median={summary['median_days']:.2f}d "
        f"max={summary['max_days']:.2f}d"
    )


def assert_local_dsn(dsn: str) -> None:
    """Refuse to rewrite timestamps anywhere but a loopback database.

    Parsed rather than pattern-matched: `postgres://mesh:mesh@127.0.0.1:5432/x`
    and `postgres://u:p@mesh.entire.host/x?host=127.0.0.1` differ in exactly the
    place a substring check would get wrong.
    """
    from urllib.parse import urlsplit

    parts = urlsplit(dsn)
    if parts.scheme not in ("postgres", "postgresql"):
        raise BackdateError(
            f"{ENV_BACKDATE_DSN} must be a postgres:// URL, got scheme {parts.scheme!r}"
        )
    host = (parts.hostname or "").lower()
    if host not in _LOOPBACK_HOSTS:
        raise BackdateError(
            f"refusing to backdate against host {host!r}: fixture backdating is "
            "only ever correct against the branch arm's ephemeral postgres. The "
            "prod arm measures the live workspace, where rewriting created_at "
            "would corrupt real memories' ranking."
        )


def build_backdate_sql(stamps: dict[str, datetime]) -> str:
    """One statement: update every key, and RETURN how many rows it touched.

    The count is the whole point of doing it in SQL rather than in a shell loop.
    A backdate that matched nothing — wrong workspace, keys swept between ingest
    and update, a rename — must be a failure and not a green step, and only the
    server can say how many rows changed.
    """
    if not stamps:
        raise BackdateError("no fixtures to backdate")
    rows = []
    for key, ts in sorted(stamps.items()):
        if not _KEY_RE.match(key):
            raise BackdateError(
                f"refusing to interpolate a non-slug key into SQL: {key!r}"
            )
        rows.append(f"('{key}', TIMESTAMPTZ '{ts.astimezone(timezone.utc).isoformat()}')")
    values = ",\n         ".join(rows)
    return (
        "WITH v(key, ts) AS (\n"
        f"  VALUES {values}\n"
        "),\n"
        "u AS (\n"
        "  UPDATE memories m\n"
        "     SET created_at = v.ts, updated_at = v.ts\n"
        "    FROM v\n"
        "   WHERE m.key = v.key\n"
        "  RETURNING 1\n"
        ")\n"
        "SELECT count(*) FROM u;"
    )


def backdate(dsn: str, stamps: dict[str, datetime], *, timeout: float = 60.0) -> int:
    """Apply the backdate. Returns rows updated; raises unless it is exact.

    "Exact" and not "at least": fewer rows than keys means part of the haystack
    kept its ingest age, which is a corpus with two age regimes in it and no way
    to tell from the score which rows carried which. More rows than keys means
    the key namespace is not what this code thinks it is, and the update reached
    fixtures belonging to something else.

    On a mismatch the UPDATE has already committed for the rows that DID match —
    it is one statement, so it is atomic, not conditional. That is deliberate and
    harmless: raising aborts the question, and `_sweep` deletes its fixtures in
    the `finally` regardless. Rolling back would leave the same corpus in the
    same unusable state, one transaction later.
    """
    assert_local_dsn(dsn)
    psql = shutil.which("psql")
    if not psql:
        raise BackdateError(
            "psql is not on PATH, so the fixtures cannot be aged. Failing rather "
            "than measuring an un-aged corpus under an age-mode that claims "
            "otherwise (ubuntu-latest ships postgresql-client)."
        )
    sql = build_backdate_sql(stamps)
    proc = subprocess.run(  # noqa: S603 — fixed argv, SQL on stdin, keys slug-validated
        [psql, dsn, "-v", "ON_ERROR_STOP=1", "--no-psqlrc", "-qtA", "-f", "-"],
        input=sql,
        capture_output=True,
        text=True,
        timeout=timeout,
    )
    if proc.returncode != 0:
        raise BackdateError(
            f"psql exited {proc.returncode}: {(proc.stderr or proc.stdout).strip()[:400]}"
        )
    out = (proc.stdout or "").strip().splitlines()
    try:
        updated = int(out[-1].strip())
    except (IndexError, ValueError) as exc:
        raise BackdateError(
            f"could not read the updated-row count from psql output: {proc.stdout!r}"
        ) from exc
    if updated != len(stamps):
        raise BackdateError(
            f"backdate touched {updated} rows for {len(stamps)} fixtures — the "
            "corpus would carry two different age regimes at once, which no "
            "score can be attributed to. Refusing."
        )
    return updated


# ---------------------------------------------------------------------------
# Selftest: offline, stdlib only. Runs on every PR in the required job.
# ---------------------------------------------------------------------------


def _selftest() -> int:
    failures: list[str] = []

    def check(cond: bool, msg: str) -> None:
        if not cond:
            failures.append(msg)

    now = datetime(2026, 8, 8, 12, 0, tzinfo=timezone.utc)

    # Parsing, including the shape without a weekday.
    check(
        parse_lme_timestamp("2023/05/10 (Wed) 01:57")
        == datetime(2023, 5, 10, 1, 57, tzinfo=timezone.utc),
        "LME date with weekday did not parse to the expected instant",
    )
    for bad in ("", "2023-05-10 01:57", "not a date", "2023/05/10"):
        try:
            parse_lme_timestamp(bad)
            failures.append(f"parsed a malformed timestamp {bad!r} instead of raising")
        except ValueError:
            pass

    # Anchoring: the newest session sits at `now`, and relative spacing survives.
    dates = ["2023/05/10 (Wed) 01:57", "2023/06/09 (Fri) 01:57", "2023/06/17 (Sat) 04:02"]
    ts, clamped = target_timestamps(dates, "2023/06/17 (Sat) 04:02", now)
    check(clamped == 0, f"clamped {clamped} of 3 sessions that are all <= question_date")
    check(ts[-1] == now, "the session dated at question_date did not land on `now`")
    check(
        abs((ts[1] - ts[0]).total_seconds() - 30 * 86400) < 1,
        "relative spacing between sessions was not preserved by the shift",
    )
    summary = age_summary(ts, now)
    check(abs(summary["min_days"]) < 1e-9, f"min age should be 0, got {summary['min_days']}")
    check(
        abs(summary["max_days"] - 38.0847) < 0.01,
        f"max age should be ~38.08 d, got {summary['max_days']}",
    )

    # A session dated after its question is clamped, never turned into a boost.
    ts_future, clamped_future = target_timestamps(
        ["2023/07/01 (Sat) 00:00"], "2023/06/17 (Sat) 04:02", now
    )
    check(clamped_future == 1, "a post-question session was not counted as clamped")
    check(ts_future[0] == now, "a post-question session was not clamped to `now`")

    # The mode resolver: silence is the historical regime, a typo is a failure.
    check(resolve_age_mode(None) in AGE_MODES, "resolve_age_mode returned a non-mode")
    check(resolve_age_mode("") == AGE_MODE_NOW, "empty mode did not resolve to ingest-now")
    check(
        resolve_age_mode(AGE_MODE_ANCHORED) == AGE_MODE_ANCHORED,
        "explicit anchored mode did not survive resolution",
    )
    try:
        resolve_age_mode("question_anchored")  # underscore, not hyphen
        failures.append("a misspelled age mode resolved instead of raising")
    except ValueError:
        pass

    # The DSN guard, in both directions — a guard that only ever passes is not one.
    try:
        assert_local_dsn("postgres://mesh:mesh@127.0.0.1:5432/mesh?sslmode=disable")
    except BackdateError as exc:
        failures.append(f"the loopback DSN was refused: {exc}")
    for remote in (
        "postgres://u:p@mesh.entire.host:5432/mesh",
        "postgres://u:p@10.10.10.10/mesh",
        "postgresql://u:p@db.internal/mesh",
    ):
        try:
            assert_local_dsn(remote)
            failures.append(f"a remote DSN was accepted: {remote}")
        except BackdateError:
            pass

    # SQL construction: both timestamps move, and a non-slug key is refused
    # rather than interpolated.
    sql = build_backdate_sql({"bench-abc-s0": now})
    check("created_at = v.ts" in sql, "generated SQL does not set created_at")
    check("updated_at = v.ts" in sql, "generated SQL does not set updated_at")
    check("SELECT count(*) FROM u" in sql, "generated SQL does not return a row count")
    for bad_key in ("bench'; DROP TABLE memories; --", "Bench-Abc", "bench_abc", ""):
        try:
            build_backdate_sql({bad_key: now})
            failures.append(f"a non-slug key was interpolated into SQL: {bad_key!r}")
        except BackdateError:
            pass

    # The classifier mirror, in both directions. A mirror that says "nothing is
    # temporal" would silently bless a control query the server treats as
    # temporal — the failure that makes the negative control meaningless.
    for temporal in (
        "How many days ago did I attend a baking class?",
        "What did I do recently?",
        "Which event happened before the wedding?",
        "What changed in 2026-07?",
    ):
        check(trips_temporal_profile(temporal), f"missed a temporal query: {temporal!r}")
    for neutral in (
        "What was the user's decision about the apartment lease?",
        "Which dog breed did the user get?",
        "How long is my daily commute to work?",
    ):
        check(
            not trips_temporal_profile(neutral),
            f"flagged a neutral query as temporal: {neutral!r}",
        )
    # A bare year must not trip it — the server requires the `20xx-` shape, and a
    # looser mirror here would over-report age-sensitivity and mislead in the
    # direction of false confidence about which questions moved.
    check(
        not trips_temporal_profile("what happened in 2026"),
        "a bare year tripped the temporal mirror; the server needs `20xx-`",
    )

    # The decay resolver's three states.
    check(resolve_apply_decay(None) in (None, True, False), "resolve_apply_decay broke")
    check(resolve_apply_decay(True) is True, "explicit True did not survive")
    check(resolve_apply_decay(False) is False, "explicit False did not survive")

    for f in failures:
        print(f"FAIL: {f}")
    if failures:
        return 1
    print(
        "fixture_ages selftest OK — anchor preserves spacing, future dates clamp, "
        "remote DSNs refused, both timestamp columns written, temporal mirror "
        "fires and refuses in both directions"
    )
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument(
        "--selftest",
        action="store_true",
        help="Offline checks on parsing, anchoring, the DSN guard and the SQL.",
    )
    args = ap.parse_args()
    if not args.selftest:
        ap.error("--selftest is the only mode; this module is otherwise imported")
    return _selftest()


if __name__ == "__main__":
    sys.exit(main())
