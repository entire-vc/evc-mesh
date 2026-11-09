package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// NextActionSchemaVersion is the compatible version of the NextActionProjection
// wire shape. Additive changes keep the number; a consumer ignores unknown
// fields. The projection is derived on read from existing task_dependencies
// edges and blocker state; it is not a second task-status store.
const NextActionSchemaVersion = 1

// Stages a card's work is split into for gating purposes.
const (
	StagePrep            = "prep"             // code, contract, fixtures: everything before the real episode
	StageFinalAcceptance = "final_acceptance" // acceptance that needs the real install/episode
)

// StageGates returns the stages a `blocks` edge of the given condition holds.
// deployed/accepted/shipped describe the real outcome of the blocker, so they
// gate only the final acceptance of the dependent: preparation can go on.
// task_done and the evidence conditions (access_ready, contract_ready,
// artifact_published) keep the legacy meaning and gate everything.
func StageGates(c DependencyCondition) []string {
	switch c {
	case DependencyConditionDeployed, DependencyConditionAccepted, DependencyConditionShipped:
		return []string{StageFinalAcceptance}
	default:
		return []string{StagePrep, StageFinalAcceptance}
	}
}

// ReviewVerdict is the independent review evidence bound to a blocker.
type ReviewVerdict struct {
	Verdict string // SHIP | DO-NOT-SHIP | BLOCKED | ...
	SHA     string // head the verdict was issued for
}

// NextActionEdge is one `blocks` edge with the facts needed to judge it.
type NextActionEdge struct {
	Edge        TaskDependency
	Blocker     BlockerState
	BlockerHead string         // current HEAD of the blocker's work, "" if none
	BlockerName string         // owner display name, "" if unknown/not visible
	Verdict     *ReviewVerdict // nil: no verdict evidence attached
}

// Requirement is what the dependent still needs from a blocker.
type Requirement struct {
	EdgeID    uuid.UUID           `json:"edge_id"`
	FromTask  uuid.UUID           `json:"from_task"`
	Condition DependencyCondition `json:"condition"`
	Gates     []string            `json:"gates"`
	Released  bool                `json:"released"`
	Reason    string              `json:"reason,omitempty"`
	Owner     string              `json:"owner,omitempty"`
}

// StageState says whether work of one stage may proceed now.
type StageState struct {
	Stage     string        `json:"stage"`
	Ready     bool          `json:"ready"`
	BlockedBy []Requirement `json:"blocked_by,omitempty"`
}

// Produced is what this card hands to its dependents.
type Produced struct {
	EdgeID    uuid.UUID           `json:"edge_id"`
	ToTask    uuid.UUID           `json:"to_task"`
	Condition DependencyCondition `json:"condition"`
}

// NextActionProjection is the versioned per-card answer to "what may be done
// now and what is the real prerequisite of the rest".
type NextActionProjection struct {
	Version    int           `json:"version"`
	TaskID     uuid.UUID     `json:"task_id"`
	Generation string        `json:"generation"`
	Stages     []StageState  `json:"stages"`
	NextAction string        `json:"next_action"` // continue_prep | wait_requirement | final_acceptance | none
	NextActor  string        `json:"next_actor,omitempty"`
	Requires   []Requirement `json:"requires"`
	Produces   []Produced    `json:"produces"`
	// Authority: who may release the remaining requirements. Only an owner
	// through the regular API path; an LLM never sets condition_satisfied and
	// human/security gates are never lifted by this projection.
	Authority string `json:"authority"`
}

// evidenceNegative reports why an edge that the blocker state alone would
// release must not count: a verdict that is not SHIP for the current HEAD is not
// positive evidence (a DONE review with DO-NOT-SHIP or another SHA proves
// nothing about the delivered state).
func evidenceNegative(e NextActionEdge) string {
	if e.Verdict == nil || e.Edge.EvidenceSatisfied() {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(e.Verdict.Verdict), "SHIP") {
		return "review verdict is " + e.Verdict.Verdict + ", not SHIP"
	}
	if e.BlockerHead != "" && e.Verdict.SHA != e.BlockerHead {
		return "review verdict is for another SHA than current HEAD"
	}
	return ""
}

// BuildNextAction projects incoming blocks edges (this card is the dependent)
// and outgoing ones (this card is the blocker) into a NextActionProjection.
func BuildNextAction(taskID uuid.UUID, incoming []NextActionEdge, outgoing []TaskDependency) NextActionProjection {
	p := NextActionProjection{
		Version: NextActionSchemaVersion, TaskID: taskID, NextAction: "none",
		Authority: "owner via regular API/comment path; human and security gates unchanged",
		Requires:  []Requirement{}, Produces: []Produced{},
	}
	prep := StageState{Stage: StagePrep, Ready: true}
	final := StageState{Stage: StageFinalAcceptance, Ready: true}
	for _, e := range incoming {
		d := e.Edge
		if d.DependencyType != DependencyTypeBlocks {
			continue
		}
		r := Requirement{EdgeID: d.ID, FromTask: d.DependsOnTaskID, Condition: d.Condition, Gates: StageGates(d.Condition), Owner: e.BlockerName}
		if r.Condition == "" {
			r.Condition = DependencyConditionTaskDone
		}
		// ConditionSatisfied is the stored full verdict (SQL twin of Released);
		// Released covers callers that only hold BlockerState.
		r.Released = d.ConditionSatisfied || d.Released(e.Blocker)
		if r.Released {
			if why := evidenceNegative(e); why != "" {
				r.Released, r.Reason = false, why
			}
		}
		p.Requires = append(p.Requires, r)
		if r.Released {
			continue
		}
		for _, g := range r.Gates {
			if g == StagePrep {
				prep.Ready, prep.BlockedBy = false, append(prep.BlockedBy, r)
			} else {
				final.Ready, final.BlockedBy = false, append(final.BlockedBy, r)
			}
		}
	}
	for _, d := range outgoing {
		if d.DependencyType == DependencyTypeBlocks {
			c := d.Condition
			if c == "" {
				c = DependencyConditionTaskDone
			}
			p.Produces = append(p.Produces, Produced{EdgeID: d.ID, ToTask: d.TaskID, Condition: c})
		}
	}
	p.Stages = []StageState{prep, final}
	sort.Slice(p.Requires, func(i, j int) bool { return p.Requires[i].EdgeID.String() < p.Requires[j].EdgeID.String() })
	sort.Slice(p.Produces, func(i, j int) bool { return p.Produces[i].EdgeID.String() < p.Produces[j].EdgeID.String() })
	switch {
	case prep.Ready && final.Ready:
		p.NextAction = "final_acceptance"
		if len(p.Requires) == 0 {
			p.NextAction = "continue_prep"
		}
	case prep.Ready:
		p.NextAction = "continue_prep"
	default:
		p.NextAction = "wait_requirement"
		p.NextActor = prep.BlockedBy[0].Owner
	}
	p.Generation = p.generation(incoming, outgoing)
	return p
}

// generation changes whenever anything the answer depends on changes: edge
// set/condition/evidence, blocker state, blocker HEAD, verdict. A consumer that
// already acted on a generation does not act (or wake) again for it.
func (p NextActionProjection) generation(in []NextActionEdge, out []TaskDependency) string {
	var lines []string
	for _, e := range in {
		d := e.Edge
		met := ""
		if d.ConditionMetAt != nil {
			met = d.ConditionMetAt.UTC().Format(time.RFC3339Nano)
		}
		ev := ""
		if d.EvidenceURL != nil {
			ev = *d.EvidenceURL
		}
		v := ""
		if e.Verdict != nil {
			v = e.Verdict.Verdict + "@" + e.Verdict.SHA
		}
		lines = append(lines, fmt.Sprintf("in|%s|%s|%s|%s|%s|%s|%v|%v|%v|%s|%s|%v|%s", d.ID, d.DependsOnTaskID, d.DependencyType, d.Condition, met, ev,
			e.Blocker.Category, e.Blocker.IsShipped, e.Blocker.HasDeployment, e.BlockerHead, v, d.ConditionSatisfied, e.BlockerName))
	}
	for _, d := range out {
		lines = append(lines, fmt.Sprintf("out|%s|%s|%s|%s", d.ID, d.TaskID, d.DependencyType, d.Condition))
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return "v1:" + hex.EncodeToString(sum[:8])
}

// ShouldWake is the consumer's dedup rule: wake only on a new generation.
func ShouldWake(lastActed, current string) bool { return current != "" && current != lastActed }
