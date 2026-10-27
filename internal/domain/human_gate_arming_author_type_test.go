package domain

import (
	"errors"
	"testing"
)

// An extension actor (Keep) arms the gate on a card it opened, so it is a valid
// gate author; an unknown actor type is still refused.
func TestArmHumanGateInput_Validate_AuthorType(t *testing.T) {
	for _, tc := range []struct {
		authorType ActorType
		wantField  string
	}{
		{ActorTypeExtension, ""},
		{ActorTypeAgent, ""},
		{ActorType("robot"), "gate_author_type"},
	} {
		in := validArmingInputWithReason("нужен ответ владельца", "")
		in.AuthorType = tc.authorType

		err := in.Validate()

		var vErr *ArmHumanGateValidationError
		switch {
		case tc.wantField == "" && err != nil:
			t.Errorf("%s: expected no error, got %v", tc.authorType, err)
		case tc.wantField != "" && (!errors.As(err, &vErr) || vErr.Field != tc.wantField):
			t.Errorf("%s: expected %s error, got %v", tc.authorType, tc.wantField, err)
		}
	}
}
