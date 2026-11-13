package domain

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The fence check itself: what holds and what is refused,
// with the stable error code of each refusal.
func TestResultFence_Check(t *testing.T) {
	holder, other, token, session := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now()
	live, dead := now.Add(time.Minute), now.Add(-time.Second)
	state := func(h *uuid.UUID, exp *time.Time) ResultFenceState {
		return ResultFenceState{Holder: h, Token: &token, Expires: exp, SessionID: &session, Generation: 5, InputRevision: 2, Now: now}
	}
	rev := func(n int64) *int64 { return &n }
	wrongToken, otherSession := uuid.New(), uuid.New()

	cases := []struct {
		name  string
		fence *ResultFence
		state ResultFenceState
		code  string // "" = holds
	}{
		{"no fence, no check", nil, state(nil, nil), ""},
		{"token on a live lock", &ResultFence{Token: &token}, state(&holder, &live), ""},
		{"session+generation on a live lock", &ResultFence{Holder: holder, SessionID: &session, Generation: 5}, state(&holder, &live), ""},
		{"revision only, matching", &ResultFence{ExpectedInputRevision: rev(2)}, state(nil, nil), ""},
		{"lock and revision both hold", &ResultFence{Token: &token, ExpectedInputRevision: rev(2)}, state(&holder, &live), ""},
		{"expired", &ResultFence{Token: &token}, state(&holder, &dead), CodeCheckoutPreconditionFailed},
		{"released", &ResultFence{Token: &token}, state(nil, nil), CodeCheckoutPreconditionFailed},
		{"another token", &ResultFence{Token: &wrongToken}, state(&holder, &live), CodeCheckoutPreconditionFailed},
		{"another holder", &ResultFence{Holder: holder, SessionID: &session, Generation: 5}, state(&other, &live), CodeCheckoutPreconditionFailed},
		{"older generation", &ResultFence{Holder: holder, SessionID: &session, Generation: 4}, state(&holder, &live), CodeCheckoutPreconditionFailed},
		{"another session", &ResultFence{Holder: holder, SessionID: &otherSession, Generation: 5}, state(&holder, &live), CodeCheckoutPreconditionFailed},
		{"no session named", &ResultFence{Holder: holder, Generation: 5}, state(&holder, &live), CodeCheckoutPreconditionFailed},
		{"revision moved", &ResultFence{ExpectedInputRevision: rev(1)}, state(nil, nil), CodeInputRevisionChanged},
		{"lock holds, revision moved", &ResultFence{Token: &token, ExpectedInputRevision: rev(1)}, state(&holder, &live), CodeInputRevisionChanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fence.Check(tc.state)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("want the fence to hold, got %v", err)
				}
				return
			}
			var ae *apierror.Error
			if !errors.As(err, &ae) {
				t.Fatalf("want *apierror.Error with %s, got %v", tc.code, err)
			}
			if ae.Code != http.StatusConflict || ae.ErrorCode != tc.code {
				t.Fatalf("want 409 %s, got %d %s", tc.code, ae.Code, ae.ErrorCode)
			}
			for _, v := range ae.Context {
				if s, ok := v.(string); ok && s == token.String() {
					t.Fatal("a refusal never reveals the live token")
				}
			}
		})
	}
}

func TestResultFence_NamesLock(t *testing.T) {
	token := uuid.New()
	rev := int64(3)
	var none *ResultFence
	for name, tc := range map[string]struct {
		f    *ResultFence
		want bool
	}{
		"nil":           {none, false},
		"revision only": {&ResultFence{ExpectedInputRevision: &rev}, false},
		"token":         {&ResultFence{Token: &token}, true},
		"generation":    {&ResultFence{Generation: 1}, true},
	} {
		if got := tc.f.NamesLock(); got != tc.want {
			t.Errorf("%s: NamesLock() = %t, want %t", name, got, tc.want)
		}
	}
}

func TestUUIDPtrEqual(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	a2 := a
	if !uuidPtrEqual(nil, nil) || !uuidPtrEqual(&a, &a2) {
		t.Error("equal pointers must compare equal")
	}
	if uuidPtrEqual(&a, nil) || uuidPtrEqual(nil, &a) || uuidPtrEqual(&a, &b) {
		t.Error("different pointers must not compare equal")
	}
}
