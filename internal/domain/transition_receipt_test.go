package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func validCodeRequest() ProductDoneRequest {
	v, link := int64(3), uuid.New()
	return ProductDoneRequest{
		Domain: TransitionDomainProduct, Class: TransitionClassCode, Operation: TransitionOpProductDone,
		ToStatusID: uuid.New(), ExpectedStatusID: uuid.New(), ExpectedVersion: &v, IdempotencyKey: "k",
		ProducerID: "board", AcceptingActorID: uuid.New(), Reason: "accepted",
		Evidence: ProductDoneEvidence{VCSLinkID: &link, HeadSHA: "abc"},
	}
}

func TestProductDoneRequestValidate(t *testing.T) {
	require.NoError(t, func() error { r := validCodeRequest(); return r.Validate() }())

	cases := map[string]func(r *ProductDoneRequest){
		"other domain":        func(r *ProductDoneRequest) { r.Domain = "decision_request" },
		"no expected version": func(r *ProductDoneRequest) { r.ExpectedVersion = nil },
		"no expected status":  func(r *ProductDoneRequest) { r.ExpectedStatusID = uuid.Nil },
		"no idempotency key":  func(r *ProductDoneRequest) { r.IdempotencyKey = " " },
		"no producer":         func(r *ProductDoneRequest) { r.ProducerID = "" },
		"no acceptor":         func(r *ProductDoneRequest) { r.AcceptingActorID = uuid.Nil },
		"no reason":           func(r *ProductDoneRequest) { r.Reason = "" },
		"code without link":   func(r *ProductDoneRequest) { r.Evidence.VCSLinkID = nil },
		"code without head":   func(r *ProductDoneRequest) { r.Evidence.HeadSHA = "" },
		"code with exemption": func(r *ProductDoneRequest) { r.Evidence.ExemptionType = TransitionClassResearch },
		"research no artifact": func(r *ProductDoneRequest) {
			*r = validCodeRequest()
			r.Class = TransitionClassResearch
			r.Evidence = ProductDoneEvidence{ExemptionType: TransitionClassResearch}
		},
		"research wrong type": func(r *ProductDoneRequest) {
			*r = validCodeRequest()
			r.Class = TransitionClassResearch
			r.Evidence = ProductDoneEvidence{ExemptionType: TransitionClassTrivial, ArtifactRef: "doc"}
		},
	}
	for name, mut := range cases {
		r := validCodeRequest()
		mut(&r)
		require.Error(t, r.Validate(), name)
	}
}

func TestProductDoneDigest(t *testing.T) {
	a := validCodeRequest()
	b := a
	b.IdempotencyKey = "another-key"
	require.Equal(t, a.Digest(), b.Digest(), "the key is the scope, not part of the payload")
	b.Reason = "different"
	require.NotEqual(t, a.Digest(), b.Digest())
}

func TestPolicyLookups(t *testing.T) {
	actor, acceptor := uuid.New(), uuid.New()
	p := TransitionReceiptPolicy{
		AllowedProducers:  []AllowedProducer{{ProducerID: "board", ActorID: actor}},
		AllowedAcceptors:  []uuid.UUID{acceptor},
		AllowedExemptions: []string{TransitionClassResearch},
	}
	require.True(t, p.ProducerAllowed("board", actor))
	require.False(t, p.ProducerAllowed("board", uuid.New()), "producer is bound to its actor")
	require.False(t, p.ProducerAllowed("other", actor))
	require.True(t, p.AcceptorAllowed(acceptor))
	require.False(t, p.AcceptorAllowed(actor))
	require.True(t, p.ExemptionAllowed(TransitionClassResearch))
	require.False(t, p.ExemptionAllowed(TransitionClassTrivial))
	require.False(t, (&TransitionReceiptPolicy{}).ExemptionAllowed(TransitionClassResearch), "zero policy allows nothing")
}

func TestTransitionRefusalError(t *testing.T) {
	require.Equal(t, "idempotency_key_payload_mismatch: idempotency_key was already used with a different payload", ErrReceiptKeyConflict.Error())
	r := validCodeRequest()
	r.Class = "bogus"
	require.Error(t, r.Validate())
	r.Class = TransitionClassConfig
	require.NoError(t, r.Validate())
	r.Class = TransitionClassVisible
	require.NoError(t, r.Validate())
}

func TestPolicyRoleAndExecutorLookups(t *testing.T) {
	lead, exec, other := uuid.New(), uuid.New(), uuid.New()
	p := &TransitionReceiptPolicy{AllowedProducers: []AllowedProducer{
		{ProducerID: "board", ActorID: exec},
		{ProducerID: "lead", ActorID: lead, Role: ProducerRoleLead},
	}}
	require.Equal(t, ProducerRoleLead, p.ProducerRole(lead))
	require.Empty(t, p.ProducerRole(exec), "an automatic executor has no lead role")
	require.Empty(t, p.ProducerRole(other))
	require.True(t, p.IsExecutor(exec))
	require.True(t, p.IsExecutor(lead))
	require.False(t, p.IsExecutor(other))
}

func TestAcceptanceIntentRequestValidate(t *testing.T) {
	good := func() AcceptanceIntentRequest {
		d := validCodeRequest()
		return AcceptanceIntentRequest{
			Domain: d.Domain, Class: d.Class, Operation: d.Operation, ToStatusID: d.ToStatusID,
			ExpectedStatusID: d.ExpectedStatusID, ExpectedVersion: d.ExpectedVersion, Reason: d.Reason, Evidence: d.Evidence,
		}
	}
	r := good()
	require.NoError(t, r.Validate())

	cases := map[string]func(r *AcceptanceIntentRequest){
		"no expected version": func(r *AcceptanceIntentRequest) { r.ExpectedVersion = nil },
		"no reason":           func(r *AcceptanceIntentRequest) { r.Reason = "" },
		"other domain":        func(r *AcceptanceIntentRequest) { r.Domain = "decision_request" },
		"code without head":   func(r *AcceptanceIntentRequest) { r.Evidence.HeadSHA = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := good()
			mutate(&r)
			require.Error(t, r.Validate())
		})
	}
}
