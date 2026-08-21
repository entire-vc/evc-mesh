"""Normalise the LongMemEval-S corpus so it survives the memory write-path gate.

Since `1f803fd` (PR #660, task #f78232c4) `remember` refuses content that
carries an invisible/direction-altering character or an instruction-override
phrase. The bench corpus contains both — measured against
`data/lme_s_24.json`: 6 sessions carry `U+200B`, 3 carry a literal
"ignore all previous"-shaped phrase, out of 1150 sessions total.

The policy decision (task #82e42882) was to normalise here rather than to grant
the bench agent an exemption on the write path. The reasoning, in short: an
exemption would add a fourth way into `memories` that bypasses the gate, whose
only check is "is this the right agent" — and the bench agent's key lives in
CI. It would also put the adversarial content into the database for real, since
the harness writes at `STORE_SCOPE = "workspace"`, the same space the fleet's
`recall` reads. Normalising costs 9 sessions out of 1150 and puts nothing.

Two properties this module is built around:

1. **It never silently touches a session that carries an answer.** The metric is
   recall@k against `answer_session_ids`; rewriting a distractor cannot move it,
   rewriting a gold session can. `assert_only_distractors_touched` raises rather
   than letting that happen quietly.

2. **Drift against the Go rules fails loudly, not silently.** These rules mirror
   `internal/service/memory_sanitizer.go`. If Go widens a rule and this module
   lags, the write is *refused* and the bench dies visibly — the copies cannot
   drift into a quiet hole. `test_corpus_sanitize.py` additionally pins the rule
   set so the divergence is caught before a run rather than during one.
"""

from __future__ import annotations

import re
import unicodedata
from typing import Iterable, NamedTuple

# Mirrors isAllowedInvisible() in internal/service/memory_sanitizer.go: ZWJ and
# the two variation selectors that carry legitimate emoji presentation.
_ALLOWED_INVISIBLE = frozenset("‍︎️")

# Mirrors instructionOverrideRegex. The verb/target lists are deliberately the
# narrow ones from the Go side — widening them there produced false positives on
# ordinary prose, and widening them here would rewrite corpus text that the gate
# would have accepted.
_OVERRIDE_RE = re.compile(
    r"(?is)\b(ignore|disregard|override|forget)\b.{0,50}?\b(previous|prior|system|developer)\b"
)

# What replaces an override phrase. Visible on purpose: a reader of a stored
# bench fixture should be able to tell that the text was altered and why,
# instead of wondering whether the corpus really reads like that.
_OVERRIDE_PLACEHOLDER = "[injection phrase removed: bench write-path policy]"


def _is_disallowed_invisible(ch: str) -> bool:
    """Mirror of isDisallowedInvisible() in memory_sanitizer.go."""
    if ch in _ALLOWED_INVISIBLE:
        return False
    o = ord(ch)
    if ch in ("­", "᠎"):  # soft hyphen, Mongolian vowel separator
        return True
    if 0xFE00 <= o <= 0xFE0F:  # variation selectors 1-16
        return True
    if 0xE0100 <= o <= 0xE01EF:  # variation selectors supplement
        return True
    # Cf covers bidi controls, zero-width space/non-joiner and the Tag block.
    return unicodedata.category(ch) == "Cf"


class Normalised(NamedTuple):
    text: str
    #: Rule labels that fired, using the sanitizer's own names so a bench log
    #: line and a refusal message can be read against each other.
    labels: tuple[str, ...]

    @property
    def changed(self) -> bool:
        return bool(self.labels)


def normalise(text: str) -> Normalised:
    """Return `text` in a form the write-path gate accepts.

    Invisibles are dropped: a zero-width character has no rendered form, so
    removing it changes neither what a human reads nor what the retriever
    indexes — the cost is exactly zero.

    An override phrase is replaced by a visible marker rather than deleted, and
    the surrounding session is kept rather than dropped. Dropping the session
    would shrink the haystack, which makes the question *easier* and inflates
    the score — the opposite of the honest direction.
    """
    labels: list[str] = []

    if any(_is_disallowed_invisible(c) for c in text):
        text = "".join(c for c in text if not _is_disallowed_invisible(c))
        labels.append("invisible-character")

    if _OVERRIDE_RE.search(text):
        text = _OVERRIDE_RE.sub(_OVERRIDE_PLACEHOLDER, text)
        labels.append("instruction-override")

    return Normalised(text, tuple(labels))


def session_text(session: Iterable[dict]) -> str:
    """Join a haystack session the way run_ci.format_session_text does.

    Only used for *inspecting* a corpus (the pre-flight audit below). The live
    write path normalises the already-formatted string, so the two cannot
    disagree about what was scanned.
    """
    return "\n".join(
        f"{t.get('role', '')}: {t.get('content', '')}" for t in session
    )


class Touched(NamedTuple):
    question_id: str
    session_index: int
    session_id: str
    labels: tuple[str, ...]
    is_gold: bool


def audit(dataset: list[dict]) -> list[Touched]:
    """Report every session normalisation would rewrite, gold ones included."""
    out: list[Touched] = []
    for q in dataset:
        gold = set(q.get("answer_session_ids") or [])
        ids = q.get("haystack_session_ids") or []
        for i, sess in enumerate(q.get("haystack_sessions") or []):
            res = normalise(session_text(sess))
            if not res.changed:
                continue
            sid = ids[i] if i < len(ids) else ""
            out.append(
                Touched(
                    question_id=q.get("question_id", ""),
                    session_index=i,
                    session_id=sid,
                    labels=res.labels,
                    is_gold=sid in gold,
                )
            )
    return out


class GoldSessionRewritten(RuntimeError):
    """Normalisation would alter a session that carries an answer."""


def assert_only_distractors_touched(dataset: list[dict]) -> list[Touched]:
    """Fail loudly if normalisation reaches a gold session.

    Today the answer is 0 of 9 — every affected session is a distractor, so the
    rewrite provably cannot move recall@k. That is a property of the *current*
    corpus, not a guarantee of the method: refresh the corpus and it can change.
    A gold rewrite would bias the measurement while every arm still reported a
    number, which is the failure mode this whole card exists to remove. So it
    raises here, before a paid run, instead of being discovered in one.
    """
    touched = audit(dataset)
    gold = [t for t in touched if t.is_gold]
    if gold:
        detail = "; ".join(
            f"{t.question_id} session[{t.session_index}] ({', '.join(t.labels)})"
            for t in gold
        )
        raise GoldSessionRewritten(
            "corpus normalisation would rewrite %d answer-bearing session(s): %s. "
            "Re-check the write-path policy before running: rewriting a gold "
            "session changes what the benchmark measures."
            % (len(gold), detail)
        )
    return touched
