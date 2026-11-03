package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Wire contract board-product-done.authenticated.v1 over real HTTP handlers and
// real Postgres: the exact sequence the fleet consumer runs (discovery, read
// the acceptance record, transition with acceptance_ref only, read the receipt
// back) and the checks it applies to every response. Same harness and skip
// rule as the other receipt e2e files.

const wireScope = "product-done-canary-v1"
const wireProducer = "board-services.product-done.v1"

func (r *receiptE2E) enableWire() {
	r.t.Helper()
	code, out := r.do(http.MethodPut, "/projects/"+r.projID.String()+"/transition-receipt-policy", r.producer, map[string]any{
		"enabled": true, "enforce_done": true, "scope_id": wireScope,
		"allowed_producers":  []map[string]any{{"producer_id": wireProducer, "actor_id": r.producer}},
		"allowed_acceptors":  []uuid.UUID{r.acceptor},
		"allowed_exemptions": []string{"research"},
	})
	require.Equal(r.t, http.StatusOK, code, "%v", out)
	require.Equal(r.t, wireScope, out["scope_id"])
}

// consumerPayload is what the consumer posts: no accepting_actor_id, no
// acceptance_intent_id, the acceptance record's id as acceptance_ref and its
// evidence object verbatim.
func consumerPayload(acc map[string]any, key string) map[string]any {
	return map[string]any{
		"domain": "product", "operation": "product_done", "class": acc["class"],
		"expected_status_id": acc["expected_status_id"], "expected_version": acc["expected_version"],
		"to_status_id": acc["to_status_id"], "producer_id": wireProducer, "producer_session": "sess-1",
		"source_ref": "bob@abc", "source_hash": "deadbeef", "acceptance_ref": acc["id"],
		"reason": "authenticated independent product acceptance", "evidence": acc["evidence"],
		"idempotency_key": key,
	}
}

func TestTransitionReceiptE2E_WireContractRoundTrip(t *testing.T) {
	r := newReceiptE2E(t)
	tid := r.taskID.String()
	caps := "/projects/" + r.projID.String() + "/product-done-capabilities"
	accPath := "/tasks/" + tid + "/product-done-acceptance"
	path := "/tasks/" + tid + "/transitions/product-done"

	// Discovery before any policy: reachable, not ready (and not a 404).
	code, out := r.do(http.MethodGet, caps, r.producer, nil)
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Equal(t, domain.WireContractProductDoneV1, out["wire_contract"])
	require.Equal(t, false, out["enforcement_ready"])
	code, _ = r.do(http.MethodGet, caps, uuid.Nil, nil)
	require.Equal(t, http.StatusUnauthorized, code)

	r.enableWire()

	code, out = r.do(http.MethodGet, caps, r.producer, nil)
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Equal(t, wireScope, out["scope_id"])
	require.Equal(t, r.projID.String(), out["project_id"])
	require.Equal(t, wireProducer, out["producer_id"])
	require.Equal(t, r.producer.String(), out["credential_actor_id"])
	require.Contains(t, out, "credential_client_id")
	require.Nil(t, out["credential_client_id"])
	require.Equal(t, true, out["authenticated_acceptance"])
	require.Equal(t, true, out["immutable_receipts"])
	require.Equal(t, true, out["enforcement_ready"])
	require.Equal(t, domain.PayloadDigestAlgorithmV1, out["payload_digest_algorithm"])
	require.EqualValues(t, 1, out["policy_version"], "first policy row")
	// Control: a credential the policy does not bind to a producer is never ready.
	code, out = r.do(http.MethodGet, caps, r.acceptor, nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, out["enforcement_ready"])
	require.Equal(t, "", out["producer_id"])

	// No acceptance yet: nothing to read; an unbound credential cannot read it.
	code, out = r.do(http.MethodGet, accPath, r.producer, nil)
	require.Equal(t, http.StatusNotFound, code, "%v", out)
	code, out = r.do(http.MethodGet, accPath, r.acceptor, nil)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "producer_not_authorized", out["code"])

	// The reviewer records the acceptance under its own credential.
	code, out = r.do(http.MethodPost, "/tasks/"+tid+"/acceptance-intents", r.acceptor, r.intentBody())
	require.Equal(t, http.StatusCreated, code, "%v", out)
	intentID := out["id"].(string)

	code, out = r.do(http.MethodGet, accPath, r.producer, nil)
	require.Equal(t, http.StatusOK, code, "%v", out)
	acc := out["acceptance"].(map[string]any)
	require.Equal(t, intentID, acc["id"])
	require.Equal(t, r.acceptor.String(), acc["accepting_actor_id"], "the acceptor is the authenticated reviewer, not a request claim")
	require.Equal(t, r.producer.String(), acc["credential_actor_id"])
	require.Equal(t, wireProducer, acc["producer_id"])
	require.Equal(t, wireScope, acc["scope_id"])
	require.Equal(t, true, acc["authenticated"])
	require.Equal(t, true, acc["immutable"])
	require.Equal(t, false, acc["revoked"])
	require.Equal(t, map[string]any{"vcs_link_id": r.linkID.String(), "head_sha": receiptHead}, acc["evidence"])
	rest := map[string]any{}
	for k, v := range acc {
		if k != "snapshot_hash" {
			rest[k] = v
		}
	}
	want, err := domain.CanonicalHash(rest)
	require.NoError(t, err)
	require.Equal(t, want, acc["snapshot_hash"])
	evh, err := domain.CanonicalHash(acc["evidence"])
	require.NoError(t, err)
	require.Equal(t, evh, acc["evidence_hash"])
	// Another credential reads the other-credential stamp: a different snapshot.
	code, _ = r.do(http.MethodGet, accPath, uuid.Nil, nil)
	require.Equal(t, http.StatusUnauthorized, code)

	// Negative controls: each is refused with zero commit.
	b := consumerPayload(acc, "forged-ref")
	b["acceptance_ref"] = uuid.NewString()
	code, out = r.do(http.MethodPost, path, r.producer, b)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "acceptance_intent_not_found", out["code"])
	b = consumerPayload(acc, "forged-actor")
	b["accepting_actor_id"] = r.producer // the credential itself, not the intent's acceptor
	code, out = r.do(http.MethodPost, path, r.producer, b)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "acceptor_not_authorized", out["code"])
	b = consumerPayload(acc, "other-evidence")
	b["evidence"] = map[string]any{"vcs_link_id": r.linkID.String(), "head_sha": "dddddddddddddddddddddddddddddddddddddddd"}
	code, _ = r.do(http.MethodPost, path, r.producer, b)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	b = consumerPayload(acc, "mismatch-pair")
	b["acceptance_intent_id"] = uuid.NewString()
	code, out = r.do(http.MethodPost, path, r.producer, b)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "acceptance_ref_mismatch", out["code"])
	b = consumerPayload(acc, "stale-cas")
	b["expected_version"] = r.version() - 1
	code, _ = r.do(http.MethodPost, path, r.producer, b)
	require.Equal(t, http.StatusConflict, code)
	b = consumerPayload(acc, "unbound-producer")
	code, out = r.do(http.MethodPost, path, r.acceptor, b)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "producer_not_authorized", out["code"])
	require.False(t, r.statusIsDone())

	// The consumer transition: acceptance_ref only.
	pay := consumerPayload(acc, "wire-1")
	code, out = r.do(http.MethodPost, path, r.producer, pay)
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.True(t, r.statusIsDone())
	rc := out["receipt"].(map[string]any)

	wantDigest, err := domain.CanonicalHash(withoutKey(pay, "idempotency_key"))
	require.NoError(t, err)
	// Every field the consumer's receipt check compares.
	require.Equal(t, tid, rc["task_id"])
	require.Equal(t, r.projID.String(), rc["project_id"])
	require.Equal(t, pay["expected_status_id"], rc["from_status_id"])
	require.EqualValues(t, pay["expected_version"], rc["from_version"])
	require.EqualValues(t, pay["expected_version"].(float64)+1, rc["to_version"])
	require.Equal(t, r.producer.String(), rc["credential_actor_id"])
	require.Contains(t, rc, "credential_client_id")
	require.Nil(t, rc["credential_client_id"])
	require.Equal(t, r.acceptor.String(), rc["accepting_actor_id"])
	require.Equal(t, acc["snapshot_hash"], rc["acceptance_hash"])
	require.Equal(t, acc["evidence_hash"], rc["evidence_hash"])
	require.Equal(t, wireScope, rc["scope_id"])
	require.EqualValues(t, 1, rc["policy_version"])
	require.Equal(t, domain.PayloadDigestAlgorithmV1, rc["payload_digest_algorithm"])
	require.Equal(t, wantDigest, rc["payload_digest_v1"])
	require.Equal(t, acc["evidence"], rc["evidence"])
	require.Equal(t, "claimed", rc["source_trust"])
	for _, k := range []string{"domain", "class", "operation", "to_status_id", "producer_id", "producer_session", "source_ref", "source_hash", "reason", "acceptance_ref", "idempotency_key"} {
		require.Equal(t, pay[k], rc[k], k)
	}
	require.NotEmpty(t, rc["payload_digest"], "foundation digest is kept next to the v1 digest")
	require.NotNil(t, rc["evidence_snapshot"], "the stored vcs snapshot stays readable")

	// Readback returns the identical receipt.
	code, out = r.do(http.MethodGet, "/tasks/"+tid+"/transition-receipts", r.producer, nil)
	require.Equal(t, http.StatusOK, code)
	list := out["receipts"].([]any)
	require.Len(t, list, 1)
	require.Equal(t, rc, list[0])

	// Same key + same payload: the same receipt, no second event.
	code, out = r.do(http.MethodPost, path, r.producer, pay)
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Equal(t, true, out["replayed"])
	require.Equal(t, rc, out["receipt"])
	// Same key, different payload: terminal conflict.
	other := consumerPayload(acc, "wire-1")
	other["reason"] = "different"
	code, out = r.do(http.MethodPost, path, r.producer, other)
	require.Equal(t, http.StatusConflict, code, "%v", out)
	var receipts, audits int
	require.NoError(t, r.db.Get(&receipts, `SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, r.taskID))
	require.Equal(t, 1, receipts)
	require.NoError(t, r.db.Get(&audits, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved'`, r.taskID))
	require.Equal(t, 1, audits)

	// The intent is single-use and the acceptance record is gone for this state.
	code, _ = r.do(http.MethodGet, accPath, r.producer, nil)
	require.Equal(t, http.StatusNotFound, code)
}

// Legacy shape, enforcement off: a caller-supplied accepting actor still commits
// and the receipt keeps its legacy evidence shape (policy-off positive control).
func TestTransitionReceiptE2E_WireLegacyPathUnchanged(t *testing.T) {
	r := newReceiptE2E(t)
	r.enablePolicy()
	code, out := r.do(http.MethodPost, "/tasks/"+r.taskID.String()+"/transitions/product-done", r.producer, r.body("legacy-1"))
	require.Equal(t, http.StatusOK, code, "%v", out)
	rc := out["receipt"].(map[string]any)
	require.Equal(t, "vcs", rc["evidence_kind"])
	require.Contains(t, rc["evidence"], "ship_sha", "legacy: evidence is the stored snapshot")
	require.NotContains(t, rc, "evidence_snapshot")
	for _, k := range []string{"scope_id", "acceptance_hash", "payload_digest_v1", "payload_digest_algorithm", "credential_client_id", "evidence_snapshot"} {
		require.NotContains(t, rc, k, "legacy receipt keeps its prior JSON shape")
	}
}

func withoutKey(m map[string]any, k string) map[string]any {
	out := map[string]any{}
	for key, v := range m {
		if key != k {
			out[key] = v
		}
	}
	return out
}

func TestTransitionReceiptE2E_WireRefusalPaths(t *testing.T) {
	r := newReceiptE2E(t)
	tid := r.taskID.String()
	accPath := "/tasks/" + tid + "/product-done-acceptance"
	polPath := "/projects/" + r.projID.String() + "/transition-receipt-policy"

	// Malformed ids and bodies.
	code, _ := r.do(http.MethodGet, "/projects/not-a-uuid/product-done-capabilities", r.producer, nil)
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = r.do(http.MethodGet, "/tasks/"+uuid.NewString()+"/product-done-acceptance", r.producer, nil)
	require.Equal(t, http.StatusNotFound, code)
	code, _ = r.do(http.MethodPost, "/tasks/"+tid+"/transitions/product-done", r.producer, "{not json")
	require.Equal(t, http.StatusBadRequest, code)
	code, out := r.do(http.MethodPost, "/tasks/"+tid+"/transitions/product-done", r.producer, `{"bogus_field":1}`)
	require.Equal(t, http.StatusBadRequest, code, "%v", out)

	// Policy scope_id validation.
	code, _ = r.do(http.MethodPut, polPath, r.producer, map[string]any{"enabled": true, "scope_id": " padded "})
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = r.do(http.MethodPut, polPath, r.producer, map[string]any{"enabled": true, "scope_id": strings.Repeat("x", 121)})
	require.Equal(t, http.StatusBadRequest, code)

	// Acceptance read: no actor, policy disabled, unbound caller, no intent.
	code, _ = r.do(http.MethodGet, accPath, uuid.Nil, nil)
	require.Equal(t, http.StatusUnauthorized, code)
	code, out = r.do(http.MethodGet, accPath, r.producer, nil)
	require.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)
	require.Equal(t, "transition_policy_disabled", out["code"])

	r.enableWire()
	code, out = r.do(http.MethodGet, accPath, r.acceptor, nil)
	require.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)
	require.Equal(t, "producer_not_authorized", out["code"])
	code, _ = r.do(http.MethodGet, accPath, r.producer, nil)
	require.Equal(t, http.StatusNotFound, code, "no intent recorded yet")
}
