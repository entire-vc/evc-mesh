package domain

import (
	"testing"

	"github.com/google/uuid"
)

// Task 1.17a's other new line lives in ArmHumanGateInput.Validate (human_gate_arming.go),
// not in the predicate file: copy_tier is required exactly when Reason looks like a
// copy-approval question, and that check needs Reason, which lives on this struct, not
// on GateArmPredicate. Same reason as the tests in human_gate_predicate_copy_tier_test.go
// for touching this from package domain directly: coverage-gate has no -coverpkg, so a
// service-level caller of ArmHumanGateInput.Validate is invisible to internal/domain's
// own coverage profile.
func validArmingInputWithReason(reason, copyTier string) ArmHumanGateInput {
	p := copyTierValidPredicate()
	p.CopyTier = copyTier
	return ArmHumanGateInput{
		TaskID:             uuid.New(),
		Author:             uuid.New(),
		AuthorType:         ActorTypeAgent,
		Reason:             reason,
		RecommendedDefault: "жду ответа до дедлайна",
		Source:             ArmHumanGateSourceAPI,
		Predicate:          &p,
	}
}

func TestArmHumanGateInput_Validate_RequiresCopyTierOnCopyShapedReason(t *testing.T) {
	in := validArmingInputWithReason("апрув видимого текста: подписи пунктов меню и диалога", "")

	err := in.Validate()

	if err == nil {
		t.Fatal("expected an error for a copy-shaped reason with copy_tier omitted, got nil")
	}
	vErr, ok := err.(*ArmHumanGateValidationError)
	if !ok {
		t.Fatalf("expected *ArmHumanGateValidationError, got %T: %v", err, err)
	}
	if vErr.Field != "predicate.copy_tier" {
		t.Errorf("Field = %q, want predicate.copy_tier", vErr.Field)
	}
}

func TestArmHumanGateInput_Validate_CopyTierSet_PassesOnCopyShapedReason(t *testing.T) {
	for _, tier := range []string{CopyTierA, CopyTierB} {
		in := validArmingInputWithReason("апрув видимого текста: подписи пунктов меню и диалога", tier)

		if err := in.Validate(); err != nil {
			t.Errorf("Validate() with copy_tier=%q on a copy-shaped reason = %v, want nil", tier, err)
		}
	}
}

func TestArmHumanGateInput_Validate_OrdinaryReason_CopyTierStillOptional(t *testing.T) {
	in := validArmingInputWithReason("мёржим сейчас или ждём зелёного CI?", "")

	if err := in.Validate(); err != nil {
		t.Errorf("Validate() on an ordinary reason with copy_tier omitted = %v, want nil", err)
	}
}

// The marker source is exempt from the whole predicate, copy_tier included — Validate
// only runs the copy_tier check inside the `Source == ArmHumanGateSourceAPI` branch.
func TestArmHumanGateInput_Validate_MarkerSource_CopyTierNotRequired(t *testing.T) {
	in := ArmHumanGateInput{
		TaskID:     uuid.New(),
		Author:     uuid.New(),
		AuthorType: ActorTypeAgent,
		Reason:     "апрув видимого текста: подписи пунктов меню и диалога",
		Source:     ArmHumanGateSourceMarker,
	}

	if err := in.Validate(); err != nil {
		t.Errorf("Validate() on a marker-sourced copy-shaped reason = %v, want nil", err)
	}
}
