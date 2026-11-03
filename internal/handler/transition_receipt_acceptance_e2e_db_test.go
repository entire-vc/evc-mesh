package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Acceptance intent routes against real Postgres: recording, listing and
// revoking an intent, and the product-done commit that consumes one while
// enforce_done is armed. Same harness and skip rule as the receipt e2e file.

func (r *receiptE2E) enableEnforced() {
	r.t.Helper()
	code, out := r.do(http.MethodPut, "/projects/"+r.projID.String()+"/transition-receipt-policy", r.producer, map[string]any{
		"enabled":            true,
		"enforce_done":       true,
		"allowed_producers":  []map[string]any{{"producer_id": "board", "actor_id": r.producer}},
		"allowed_acceptors":  []uuid.UUID{r.acceptor},
		"allowed_exemptions": []string{"research"},
	})
	require.Equal(r.t, http.StatusOK, code, "%v", out)
}

func (r *receiptE2E) intentBody() map[string]any {
	return map[string]any{
		"domain": "product", "class": "code", "operation": "product_done",
		"to_status_id": r.doneID, "expected_status_id": r.todoID, "expected_version": r.version(),
		"reason": "accepted on exact head", "evidence": map[string]any{"vcs_link_id": r.linkID, "head_sha": receiptHead},
	}
}

func TestTransitionReceiptE2E_EnforcedPolicyValidation(t *testing.T) {
	r := newReceiptE2E(t)
	p := "/projects/" + r.projID.String() + "/transition-receipt-policy"
	code, _ := r.do(http.MethodPut, p, r.producer, map[string]any{"enabled": false, "enforce_done": true})
	require.Equal(t, http.StatusBadRequest, code, "enforce_done needs enabled")
	code, _ = r.do(http.MethodPut, p, r.producer, map[string]any{
		"enabled": true, "allowed_producers": []map[string]any{{"producer_id": "board", "actor_id": r.producer, "role": "admin"}},
	})
	require.Equal(t, http.StatusBadRequest, code, "producer role is empty or lead")
}

func TestTransitionReceiptE2E_AcceptanceIntentLifecycle(t *testing.T) {
	r := newReceiptE2E(t)
	tid := r.taskID.String()
	intents := "/tasks/" + tid + "/acceptance-intents"

	// Control: with the policy off an intent is refused.
	code, out := r.do(http.MethodPost, intents, r.acceptor, r.intentBody())
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "transition_policy_disabled", out["code"])

	r.enableEnforced()

	code, _ = r.do(http.MethodPost, intents, uuid.Nil, r.intentBody())
	require.Equal(t, http.StatusUnauthorized, code)
	code, _ = r.do(http.MethodPost, intents, r.acceptor, `{"unknown_field":1}`)
	require.Equal(t, http.StatusBadRequest, code)
	b := r.intentBody()
	delete(b, "expected_version")
	code, _ = r.do(http.MethodPost, intents, r.acceptor, b)
	require.Equal(t, http.StatusBadRequest, code)

	code, out = r.do(http.MethodPost, intents, uuid.New(), r.intentBody())
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "acceptor_not_authorized", out["code"])

	b = r.intentBody()
	b["expected_version"] = r.version() - 1
	code, _ = r.do(http.MethodPost, intents, r.acceptor, b)
	require.Equal(t, http.StatusConflict, code)

	b = r.intentBody()
	b["to_status_id"] = r.todoID
	code, out = r.do(http.MethodPost, intents, r.acceptor, b)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "target_not_done_status", out["code"])

	code, out = r.do(http.MethodGet, intents, r.acceptor, nil)
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, out["acceptance_intents"], "refusals recorded nothing")

	// The accepting identity records an intent under its own credential.
	code, out = r.do(http.MethodPost, intents, r.acceptor, r.intentBody())
	require.Equal(t, http.StatusCreated, code, "%v", out)
	first := out["id"].(string)
	require.Equal(t, r.acceptor.String(), out["acceptor_actor_id"])
	code, out = r.do(http.MethodGet, intents, r.acceptor, nil)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, out["acceptance_intents"], 1)

	// Revoke: input checks, then ownership, then the real revoke.
	revoke := intents + "/" + first + "/revoke"
	code, _ = r.do(http.MethodPost, intents+"/not-a-uuid/revoke", r.acceptor, map[string]any{"reason": "x"})
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = r.do(http.MethodPost, revoke, r.acceptor, `{"unknown_field":1}`)
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = r.do(http.MethodPost, revoke, uuid.Nil, map[string]any{"reason": "x"})
	require.Equal(t, http.StatusUnauthorized, code)
	code, _ = r.do(http.MethodPost, revoke, r.acceptor, map[string]any{})
	require.Equal(t, http.StatusBadRequest, code, "reason is required")
	code, _ = r.do(http.MethodPost, intents+"/"+uuid.NewString()+"/revoke", r.acceptor, map[string]any{"reason": "x"})
	require.Equal(t, http.StatusNotFound, code)
	code, out = r.do(http.MethodPost, revoke, r.producer, map[string]any{"reason": "x"})
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "acceptor_not_authorized", out["code"])
	code, _ = r.do(http.MethodPost, revoke, r.acceptor, map[string]any{"reason": "changed my mind"})
	require.Equal(t, http.StatusNoContent, code)

	// A revoked intent cannot back the done transition: zero commit.
	path := "/tasks/" + tid + "/transitions/product-done"
	b = r.body("revoked")
	b["acceptance_intent_id"] = first
	code, out = r.do(http.MethodPost, path, r.producer, b)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "acceptance_intent_revoked", out["code"])
	require.False(t, r.statusIsDone())

	// Enforced mode without any intent is refused as well.
	code, out = r.do(http.MethodPost, path, r.producer, r.body("no-intent"))
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "acceptance_intent_required", out["code"])
	require.False(t, r.statusIsDone())

	// A fresh intent commits exactly once and is single-use.
	code, out = r.do(http.MethodPost, intents, r.acceptor, r.intentBody())
	require.Equal(t, http.StatusCreated, code, "%v", out)
	second := out["id"].(string)
	b = r.body("with-intent")
	b["acceptance_intent_id"] = second
	code, out = r.do(http.MethodPost, path, r.producer, b)
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.True(t, r.statusIsDone())
	require.Equal(t, second, out["receipt"].(map[string]any)["acceptance_intent_id"])
}
