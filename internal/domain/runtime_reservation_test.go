package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func validAcquireInput() RuntimeAcquireInput {
	return RuntimeAcquireInput{
		IdempotencyKey:            "idem-1",
		ProfileID:                 "preferred",
		TaskID:                    uuid.New(),
		CheckoutGeneration:        1,
		WorkerRef:                 "worker-b",
		ExpectedCatalogRevision:   1,
		ExpectedCatalogDigest:     "digest",
		ExpectedAdmissionRevision: 1,
		ExpectedProfileRevision:   "rev-1",
		ExpectedPoolSetDigest:     "pools",
		TTLSeconds:                60,
	}
}

func TestRuntimeAcquireInputValid(t *testing.T) {
	require.True(t, validAcquireInput().Valid())
	nilID := uuid.Nil
	someID := uuid.New()

	ok := validAcquireInput()
	ok.CheckoutRequestID = &someID
	require.True(t, ok.Valid(), "a non-nil checkout request id is accepted")
	for _, ttl := range []int{10, 900} {
		in := validAcquireInput()
		in.TTLSeconds = ttl
		require.True(t, in.Valid(), "ttl %d is inside the inclusive bounds", ttl)
	}

	cases := map[string]func(*RuntimeAcquireInput){
		"empty idempotency key":     func(in *RuntimeAcquireInput) { in.IdempotencyKey = "" },
		"invalid profile ref":       func(in *RuntimeAcquireInput) { in.ProfileID = "-bad" },
		"invalid worker ref":        func(in *RuntimeAcquireInput) { in.WorkerRef = "bad ref" },
		"nil task":                  func(in *RuntimeAcquireInput) { in.TaskID = uuid.Nil },
		"zero checkout generation":  func(in *RuntimeAcquireInput) { in.CheckoutGeneration = 0 },
		"nil checkout request id":   func(in *RuntimeAcquireInput) { in.CheckoutRequestID = &nilID },
		"zero catalog revision":     func(in *RuntimeAcquireInput) { in.ExpectedCatalogRevision = 0 },
		"empty catalog digest":      func(in *RuntimeAcquireInput) { in.ExpectedCatalogDigest = "" },
		"zero admission revision":   func(in *RuntimeAcquireInput) { in.ExpectedAdmissionRevision = 0 },
		"empty profile revision":    func(in *RuntimeAcquireInput) { in.ExpectedProfileRevision = "" },
		"empty pool set digest":     func(in *RuntimeAcquireInput) { in.ExpectedPoolSetDigest = "" },
		"ttl below minimum":         func(in *RuntimeAcquireInput) { in.TTLSeconds = 9 },
		"ttl above maximum":         func(in *RuntimeAcquireInput) { in.TTLSeconds = 901 },
		"negative checkout gen":     func(in *RuntimeAcquireInput) { in.CheckoutGeneration = -1 },
		"negative catalog revision": func(in *RuntimeAcquireInput) { in.ExpectedCatalogRevision = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validAcquireInput()
			mutate(&in)
			require.False(t, in.Valid())
		})
	}
}

func TestRuntimeConsumeInputValid(t *testing.T) {
	require.True(t, RuntimeConsumeInput{Fence: 1, CheckoutGeneration: 1, RunLeaseSeconds: 60}.Valid())
	require.True(t, RuntimeConsumeInput{Fence: 1, CheckoutGeneration: 1, RunLeaseSeconds: 86400}.Valid())
	for name, in := range map[string]RuntimeConsumeInput{
		"zero fence":           {Fence: 0, CheckoutGeneration: 1, RunLeaseSeconds: 60},
		"zero generation":      {Fence: 1, CheckoutGeneration: 0, RunLeaseSeconds: 60},
		"lease below minimum":  {Fence: 1, CheckoutGeneration: 1, RunLeaseSeconds: 59},
		"lease above maximum":  {Fence: 1, CheckoutGeneration: 1, RunLeaseSeconds: 86401},
		"empty (all defaults)": {},
	} {
		require.False(t, in.Valid(), name)
	}
}

func TestRuntimeReleaseInputValid(t *testing.T) {
	nilID := uuid.Nil
	someID := uuid.New()
	require.True(t, RuntimeReleaseInput{Fence: 1, CheckoutGeneration: 1}.Valid())
	require.True(t, RuntimeReleaseInput{Fence: 1, CheckoutGeneration: 1, CheckoutRequestID: &someID,
		Proof: RuntimeReleaseProof{Kind: RuntimeProofNoChild, EvidenceRef: "evidence/1"}}.Valid())
	for name, in := range map[string]RuntimeReleaseInput{
		"zero fence":           {Fence: 0, CheckoutGeneration: 1},
		"zero generation":      {Fence: 1, CheckoutGeneration: 0},
		"nil request id":       {Fence: 1, CheckoutGeneration: 1, CheckoutRequestID: &nilID},
		"invalid evidence ref": {Fence: 1, CheckoutGeneration: 1, Proof: RuntimeReleaseProof{EvidenceRef: " bad"}},
	} {
		require.False(t, in.Valid(), name)
	}
}

func TestRuntimeReleaseInputProofAccepted(t *testing.T) {
	proof := func(stopped bool, kind, ref string) RuntimeReleaseInput {
		return RuntimeReleaseInput{Fence: 1, CheckoutGeneration: 1, Stopped: stopped, Proof: RuntimeReleaseProof{Kind: kind, EvidenceRef: ref}}
	}
	cases := []struct {
		name  string
		in    RuntimeReleaseInput
		state string
		want  bool
	}{
		{"not stopped never frees", proof(false, RuntimeProofNoChild, ""), RuntimeReservationReserved, false},
		{"no_child without evidence", proof(true, RuntimeProofNoChild, ""), RuntimeReservationConsumed, true},
		{"no_child with valid evidence", proof(true, RuntimeProofNoChild, "ev-1"), RuntimeReservationConsumed, true},
		{"no_child with invalid evidence", proof(true, RuntimeProofNoChild, "bad ref"), RuntimeReservationConsumed, false},
		{"birth proof needs evidence", proof(true, RuntimeProofStoppedBirthProof, ""), RuntimeReservationConsumed, false},
		{"birth proof with evidence", proof(true, RuntimeProofStoppedBirthProof, "pid-1/birth"), RuntimeReservationConsumed, true},
		{"not_started before consume", proof(true, RuntimeProofNotStarted, ""), RuntimeReservationReserved, true},
		{"not_started after consume", proof(true, RuntimeProofNotStarted, ""), RuntimeReservationConsumed, false},
		{"unknown proof kind", proof(true, "trust-me", "ev"), RuntimeReservationReserved, false},
		{"empty proof kind", proof(true, "", ""), RuntimeReservationReserved, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.in.ProofAccepted(tc.state))
		})
	}
}

func TestRuntimeProfileRevisionAndCanonicalPool(t *testing.T) {
	require.Equal(t, "rev-42", RuntimeProfileRevision(42))
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	require.Equal(t, owner.String()+":anthropic:org-1",
		RuntimeCanonicalPoolID(owner, RuntimePool{Provider: "anthropic", ResourceRef: "org-1"}))
}

func reservationCatalog() *RuntimeCatalog {
	return &RuntimeCatalog{
		Pools: map[string]RuntimePool{
			"org":    {Provider: "anthropic", ResourceRef: "org-1", Aliases: []string{"org-alias"}},
			"weekly": {Provider: "anthropic", ResourceRef: "weekly-1"},
		},
		Accounts: map[string]RuntimeAccount{
			"acct": {Provider: "anthropic", QuotaPoolsByMode: map[string][]string{
				"subscription": {"weekly", "org", "org-alias"},
				"api":          {"missing-pool"},
			}},
			"empty": {Provider: "anthropic", QuotaPoolsByMode: map[string][]string{}},
		},
		Profiles: map[string]RuntimeProfile{
			"preferred":     {AccountRef: "acct", ExecutionMode: "subscription"},
			"unknown-pool":  {AccountRef: "acct", ExecutionMode: "api"},
			"no-account":    {AccountRef: "gone", ExecutionMode: "subscription"},
			"no-pools-mode": {AccountRef: "empty", ExecutionMode: "subscription"},
			"unmapped-mode": {AccountRef: "acct", ExecutionMode: "batch"},
		},
	}
}

func TestRuntimeProfilePools(t *testing.T) {
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	c := reservationCatalog()

	pools, ok := c.RuntimeProfilePools(owner, "preferred")
	require.True(t, ok)
	// The alias resolves to the same physical pool and is deduplicated; output is sorted.
	require.Equal(t, []string{
		owner.String() + ":anthropic:org-1",
		owner.String() + ":anthropic:weekly-1",
	}, pools)

	for _, ref := range []string{"absent", "unknown-pool", "no-account", "no-pools-mode", "unmapped-mode"} {
		got, found := c.RuntimeProfilePools(owner, ref)
		require.False(t, found, ref)
		require.Nil(t, got, ref)
	}
}

func TestRuntimePoolSetDigest(t *testing.T) {
	in := []string{"b", "a"}
	sum := sha256.Sum256([]byte("a\nb"))
	require.Equal(t, hex.EncodeToString(sum[:]), RuntimePoolSetDigest(in))
	require.Equal(t, RuntimePoolSetDigest([]string{"a", "b"}), RuntimePoolSetDigest(in), "order-insensitive")
	require.Equal(t, []string{"b", "a"}, in, "input is not mutated")
	require.NotEqual(t, RuntimePoolSetDigest([]string{"a"}), RuntimePoolSetDigest(in))
}

func TestRuntimeAgentCapacityDerive(t *testing.T) {
	cases := []struct {
		name   string
		in     RuntimeAgentCapacity
		ready  int
		reason string
		occ    int
		unk    int
		eff    int
	}{
		{"cap unset", RuntimeAgentCapacity{Configured: 0, Writers: 1}, 0, RuntimeCapacityCapUnset, 1, 0, 0},
		{"negative cap is unset", RuntimeAgentCapacity{Configured: -3}, 0, RuntimeCapacityCapUnset, 0, 0, 0},
		{"at capacity counts unknown as occupied", RuntimeAgentCapacity{Configured: 3, Reserved: 1, Reconcile: 1, StaleWriters: 1}, 0, RuntimeCapacityAtCapacity, 3, 2, 3},
		{"over capacity", RuntimeAgentCapacity{Configured: 1, Running: 2}, 0, RuntimeCapacityAtCapacity, 2, 0, 1},
		{"available", RuntimeAgentCapacity{Configured: 4, Reserved: 1, Running: 1}, 2, RuntimeCapacityAvailable, 2, 0, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.in
			c.Derive()
			require.Equal(t, tc.ready, c.Ready)
			require.Equal(t, tc.reason, c.Reason)
			require.Equal(t, tc.occ, c.Occupied)
			require.Equal(t, tc.unk, c.Unknown)
			require.Equal(t, tc.eff, c.Effective)
		})
	}
}
