package domain

import (
	"strings"
	"testing"
)

// RequiresCopyTier is the fail-open recognizer that decides when GateArmPredicate's
// optional CopyTier stops being optional (task 1.17a). It is deliberately incomplete —
// see its doc comment — so what this test pins is the SHAPE of that incompleteness: it
// must catch the real case that motivated the card, and it must not fire on an unrelated
// ask.
func TestRequiresCopyTier(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		want   bool
	}{
		{
			// #6a0c7ae3, Garfield's own comment on task 1.17a, verbatim: the live gate
			// that sat open for five days before a human closed it by hand as a false
			// gate. This is the negative control that matters most — the regex was
			// written by someone reading THIS sentence, not the other way round, so if
			// it fails to match, the regex is wrong and needs fixing, not the test.
			name:   "the actual #6a0c7ae3 reason",
			reason: "апрув видимого текста: подписи пунктов меню и диалога",
			want:   true,
		},
		{
			name:   "апрув текста without видимого",
			reason: "нужен апрув текста в письме рассылки",
			want:   true,
		},
		{
			name:   "копирайт bare mention",
			reason: "копирайт для лендинга готов, нужен ок",
			want:   true,
		},
		{
			// The regex requires the two words ADJACENT ("видим<suffix> текст"), not
			// merely both present in the sentence — this is the shape the source
			// pattern actually specifies (a literal single \s+ between the stems).
			name:   "видим+текст adjacent two-word form",
			reason: "проверь, пожалуйста, видимый текст перед публикацией",
			want:   true,
		},
		{
			name:   "формулировка stem",
			reason: "какая формулировка правильная для дисклеймера?",
			want:   true,
		},
		{
			name:   "подписи пунктов меню",
			reason: "подписи пунктов меню — так нормально?",
			want:   true,
		},
		{
			name:   "подпись кнопки",
			reason: "подпись кнопки в диалоге — ок так?",
			want:   true,
		},
		{
			name:   "текст письма",
			reason: "текст письма для рассылки готов, шлём?",
			want:   true,
		},
		{
			name:   "текст модалки",
			reason: "текст модалки подтверждения — нормально?",
			want:   true,
		},
		{
			name:   "словарь stem",
			reason: "обновил словарь локализации, ок?",
			want:   true,
		},
		{
			name:   "FAQ literal",
			reason: "добавил вопрос в FAQ, надо апрувить?",
			want:   true,
		},
		{
			name:   "english wording",
			reason: "wording on the pricing page — good to ship?",
			want:   true,
		},
		{
			name:   "english copy approval",
			reason: "need copy approval on the new landing hero",
			want:   true,
		},
		{
			// Negative control: an ordinary technical ask must NOT match. Failing this
			// makes the fail-open guarantee vacuous — a regex that matches everything
			// "fails open" in name only.
			name:   "unrelated technical ask",
			reason: "мёржим сейчас или ждём зелёного CI?",
			want:   false,
		},
		{
			name:   "unrelated credential ask",
			reason: "нужен доступ к prod DB для миграции",
			want:   false,
		},
		{
			name:   "unrelated money ask",
			reason: "включаем шлюз Точки прямо сейчас или ждём?",
			want:   false,
		},
		{
			name:   "empty reason",
			reason: "",
			want:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequiresCopyTier(tc.reason); got != tc.want {
				t.Errorf("RequiresCopyTier(%q) = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}
}

// copyTierValidPredicate is a GateArmPredicate that satisfies GateArmPredicate.Validate
// on its own (every reason non-empty) — the base fixture every test below starts from
// and overrides just the field it means to exercise. Kept in this package (not shared
// with internal/service's own basePredicate helper) because coverage-gate instruments
// per-package: a service-level test that calls into domain.Decide/Validate/
// RefusalMessage is invisible to `go test ./internal/domain`'s own coverage profile
// (no -coverpkg in the CI script), so these functions need their OWN domain-package
// callers or they read as 0% no matter how well they're exercised from service.
func copyTierValidPredicate() GateArmPredicate {
	return GateArmPredicate{
		CredentialExists: true, CredentialReason: "already hold the key",
		Reversible: false, ReversibleReason: "not applicable here",
		BlockedByOtherTask: false, BlockedReason: "not applicable here",
		CustomerVisibleNow: false, CustomerReason: "not applicable here",
	}
}

// TestGateArmPredicate_Decide_CopyTier covers Decide()'s new copy_tier branch AND the
// three pre-existing branches it now runs alongside — table-driven so the ordering
// invariant (tier B wins over everything, tier A exempts nothing) is pinned at the
// pure-function level, not just through the service's ArmHumanGate.
func TestGateArmPredicate_Decide_CopyTier(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    GateArmPredicate
		want GatePredicateOutcome
	}{
		{
			name: "tier B alone refuses",
			p:    func() GateArmPredicate { p := copyTierValidPredicate(); p.CopyTier = CopyTierB; return p }(),
			want: GatePredicateRefusedCopyTierB,
		},
		{
			name: "tier B wins over blocked_by_other_task",
			p: func() GateArmPredicate {
				p := copyTierValidPredicate()
				p.CopyTier = CopyTierB
				p.BlockedByOtherTask = true
				return p
			}(),
			want: GatePredicateRefusedCopyTierB,
		},
		{
			name: "tier B wins over an otherwise self-serve answer",
			p: func() GateArmPredicate {
				p := copyTierValidPredicate()
				p.CopyTier = CopyTierB
				p.Reversible = true
				return p
			}(),
			want: GatePredicateRefusedCopyTierB,
		},
		{
			name: "tier A falls through to allowed",
			p:    func() GateArmPredicate { p := copyTierValidPredicate(); p.CopyTier = CopyTierA; return p }(),
			want: GatePredicateAllowed,
		},
		{
			name: "tier A does not exempt blocked_by_other_task",
			p: func() GateArmPredicate {
				p := copyTierValidPredicate()
				p.CopyTier = CopyTierA
				p.BlockedByOtherTask = true
				return p
			}(),
			want: GatePredicateRefusedUseDependency,
		},
		{
			name: "tier A does not exempt a self-serve answer",
			p: func() GateArmPredicate {
				p := copyTierValidPredicate()
				p.CopyTier = CopyTierA
				p.Reversible = true
				return p
			}(),
			want: GatePredicateRefusedSelfServe,
		},
		{
			name: "unset tier: ordinary allowed, unchanged",
			p:    copyTierValidPredicate(),
			want: GatePredicateAllowed,
		},
		{
			name: "unset tier: ordinary blocked, unchanged",
			p: func() GateArmPredicate {
				p := copyTierValidPredicate()
				p.BlockedByOtherTask = true
				return p
			}(),
			want: GatePredicateRefusedUseDependency,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Decide(); got != tc.want {
				t.Errorf("Decide() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGateArmPredicate_Validate_CopyTier covers Validate()'s new copy_tier branch: an
// out-of-enum value is refused naming the field, while "", "A", and "B" all pass this
// predicate-level check (B is refused later, by Decide — not here).
func TestGateArmPredicate_Validate_CopyTier(t *testing.T) {
	t.Run("invalid value refused", func(t *testing.T) {
		p := copyTierValidPredicate()
		p.CopyTier = "C"

		err := p.Validate()

		if err == nil {
			t.Fatal("expected an error for copy_tier=\"C\", got nil")
		}
		vErr, ok := err.(*ArmHumanGateValidationError)
		if !ok {
			t.Fatalf("expected *ArmHumanGateValidationError, got %T: %v", err, err)
		}
		if vErr.Field != "predicate.copy_tier" {
			t.Errorf("Field = %q, want predicate.copy_tier", vErr.Field)
		}
	})

	for _, tier := range []string{"", CopyTierA, CopyTierB} {
		t.Run("accepts "+tier, func(t *testing.T) {
			p := copyTierValidPredicate()
			p.CopyTier = tier
			if err := p.Validate(); err != nil {
				t.Errorf("Validate() with copy_tier=%q = %v, want nil", tier, err)
			}
		})
	}

	t.Run("still rejects a missing reason regardless of copy_tier", func(t *testing.T) {
		p := copyTierValidPredicate()
		p.CopyTier = CopyTierA
		p.CredentialReason = ""

		err := p.Validate()

		if err == nil {
			t.Fatal("expected an error for a blank credential_reason, got nil")
		}
	})
}

// TestGatePredicateOutcome_RefusalMessage_CopyTierB covers RefusalMessage()'s new case
// alongside its two pre-existing ones (same reason as above: this function reads 0%
// from the domain package's own coverage profile until something in package domain
// calls it directly).
func TestGatePredicateOutcome_RefusalMessage_CopyTierB(t *testing.T) {
	msg := GatePredicateRefusedCopyTierB.RefusalMessage()
	for _, want := range []string{"тир B", "copy:b", "§1r.A"} {
		if !strings.Contains(msg, want) {
			t.Errorf("RefusalMessage() = %q, want it to contain %q", msg, want)
		}
	}
}

func TestGatePredicateOutcome_RefusalMessage_PreExistingOutcomes(t *testing.T) {
	if got := GatePredicateRefusedSelfServe.RefusalMessage(); !strings.Contains(got, "Reversibility is the license to act") {
		t.Errorf("RefusedSelfServe.RefusalMessage() = %q, missing expected phrase", got)
	}
	if got := GatePredicateRefusedUseDependency.RefusalMessage(); !strings.Contains(got, "add_dependency") {
		t.Errorf("RefusedUseDependency.RefusalMessage() = %q, missing expected phrase", got)
	}
	if got := GatePredicateAllowed.RefusalMessage(); got != "" {
		t.Errorf("Allowed.RefusalMessage() = %q, want empty", got)
	}
}
