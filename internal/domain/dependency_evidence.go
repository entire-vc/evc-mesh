package domain

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// EvidenceStatus is the verdict on the evidence recorded for a dependency edge.
type EvidenceStatus string

const (
	EvidenceNone       EvidenceStatus = "none"       // nothing recorded
	EvidenceUnverified EvidenceStatus = "unverified" // a bare link (legacy or unbound): kept, never trusted
	EvidencePending    EvidenceStatus = "pending"    // bound, but the blocker does not show the fact yet
	EvidenceInvalid    EvidenceStatus = "invalid"    // contradicted: other HEAD/env/criterion, failed verdict
	EvidenceTrusted    EvidenceStatus = "trusted"    // verified against the blocker's own receipts or attested
	EvidenceRevoked    EvidenceStatus = "revoked"    // was trusted; its subject (HEAD/deploy) moved on
)

// EvidenceClaim is what a caller sends: the receipt it says satisfies the
// criterion. Nothing in it is fetched; it is only compared with Mesh's own
// records of the blocker.
type EvidenceClaim struct {
	URL         string
	Criterion   string
	HeadSHA     string
	Environment string
	// Attest is an explicit, authorized attestation of a manual criterion
	// (access_ready, contract_ready, artifact_published).
	Attest bool
}

// DependencyEvidence is the verified record stored on the edge.
type DependencyEvidence struct {
	URL         string
	Status      EvidenceStatus
	Reason      string
	HeadSHA     string
	Environment string
	LinkID      *uuid.UUID
	ActorID     *uuid.UUID
	MetAt       *time.Time
}

// EffectiveCriterion is the criterion text an evidence claim must name.
func (d TaskDependency) EffectiveCriterion() string {
	if strings.TrimSpace(d.Criterion) != "" {
		return strings.TrimSpace(d.Criterion)
	}
	return string(d.Condition)
}

// VerifyDependencyEvidence decides what a claim is worth for an edge whose
// blocker has the given pr links. It reads only Mesh's records (links), never
// the claim's URL. Authority of the actor is checked by the caller; actorOK
// says it passed.
func VerifyDependencyEvidence(dep TaskDependency, c EvidenceClaim, links []VCSLink, actorOK bool) DependencyEvidence {
	ev := DependencyEvidence{URL: strings.TrimSpace(c.URL), HeadSHA: c.HeadSHA, Environment: c.Environment}
	set := func(st EvidenceStatus, why string) DependencyEvidence { ev.Status, ev.Reason = st, why; return ev }
	if ev.URL != "" {
		if u, err := url.Parse(ev.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return set(EvidenceInvalid, "evidence_url must be an absolute http(s) URL")
		}
	}
	if strings.TrimSpace(c.Criterion) == "" {
		return set(EvidenceUnverified, "no criterion named: kept as a link only")
	}
	if strings.TrimSpace(c.Criterion) != dep.EffectiveCriterion() {
		return set(EvidenceInvalid, "criterion does not match the edge criterion")
	}
	if dep.Condition == DependencyConditionTaskDone || dep.Condition == "" {
		return set(EvidenceInvalid, "task_done edge takes no evidence")
	}
	switch dep.Condition {
	case DependencyConditionAccessReady, DependencyConditionContractReady, DependencyConditionArtifactPublished:
		if !c.Attest {
			return set(EvidenceUnverified, "manual criterion needs an explicit attestation")
		}
		if !actorOK {
			return set(EvidenceInvalid, "actor is not authorized to attest this criterion")
		}
		if ev.URL == "" {
			return set(EvidenceInvalid, "attestation needs an evidence_url reference")
		}
		return set(EvidenceTrusted, "attested")
	}
	// A delivery claim reads the blocker's own links, so the writer must be allowed
	// to see the blocker; access to the dependent task alone is not enough.
	if !actorOK {
		return set(EvidenceInvalid, "actor is not authorized for the blocker task")
	}
	if strings.TrimSpace(c.HeadSHA) == "" {
		return set(EvidenceInvalid, "head_sha is required for "+string(dep.Condition))
	}
	var seen bool
	for i := range links {
		l := links[i]
		if l.LinkType != VCSLinkTypePR || l.Status == VCSLinkStatusClosed {
			continue
		}
		seen = true
		id := l.ID
		// A receipt only counts on the link's current HEAD: a link that moved on
		// keeps its old deploy/acceptance metadata, which must not release.
		if l.HeadSHA() != c.HeadSHA {
			continue
		}
		switch dep.Condition {
		case DependencyConditionShipped:
			if l.ShipSHA() == c.HeadSHA {
				ev.LinkID = &id
				return set(EvidenceTrusted, "independent SHIP recorded for this HEAD")
			}
		case DependencyConditionDeployed:
			d := l.Deployed()
			if d == nil || d.SHA != c.HeadSHA {
				continue
			}
			if strings.TrimSpace(c.Environment) == "" {
				return set(EvidenceInvalid, "environment is required for deployed")
			}
			if d.Environment != c.Environment {
				continue
			}
			ev.LinkID = &id
			return set(EvidenceTrusted, "deploy recorded for this SHA and environment")
		case DependencyConditionAccepted:
			a := l.Acceptance()
			if a == nil || a.SHA != c.HeadSHA {
				continue
			}
			if a.Result == DeliveryStatePassed {
				ev.LinkID = &id
				return set(EvidenceTrusted, "acceptance passed for this HEAD")
			}
			return set(EvidenceInvalid, "acceptance verdict for this HEAD is "+a.Result)
		}
	}
	if !seen {
		return set(EvidencePending, "the blocker has no open pr link yet")
	}
	// A link exists but shows no matching receipt: if any link carries a
	// different HEAD/SHA it is stale, otherwise nothing is recorded yet.
	for i := range links {
		l := links[i]
		if l.LinkType != VCSLinkTypePR || l.Status == VCSLinkStatusClosed {
			continue
		}
		if (l.HeadSHA() != "" && l.HeadSHA() != c.HeadSHA) ||
			(dep.Condition == DependencyConditionDeployed && l.Deployed() != nil) {
			return set(EvidenceInvalid, "recorded receipt is for another HEAD or environment")
		}
	}
	return set(EvidencePending, "the blocker does not yet record this receipt")
}

// ErrStaleDependencyGeneration: the edge's condition/criterion changed after the
// caller read it, so evidence recorded for the old generation is refused.
var ErrStaleDependencyGeneration = errors.New("dependency generation changed: evidence is stale")

// DependencyConditionUpdate is one write of an edge's condition, criterion and
// (optionally) verified evidence. Evidence nil keeps whatever is stored, unless
// condition/criterion changed, which clears it.
type DependencyConditionUpdate struct {
	Condition          DependencyCondition
	Criterion          string
	Evidence           *DependencyEvidence
	ExpectedGeneration *int64
}
