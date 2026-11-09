package domain

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Finding kinds reported by GraphFindings.
const (
	FindingHardCycle    = "hard_cycle"    // cycle even when only prep-gating edges count: nobody can start
	FindingSemanticLoop = "semantic_loop" // cycle only through final-stage conditions: prep proceeds, final waits
	FindingAndBlockers  = "and_blockers"  // dependent needs two or more unreleased blockers at once
	FindingDiamond      = "diamond"       // two blockers of one card share a common ancestor
)

// Finding is a diagnosis with a concrete resolvable action. GraphFindings never
// deletes or rewrites an edge: the action is for the named owner.
type Finding struct {
	Kind   string      `json:"kind"`
	Tasks  []uuid.UUID `json:"tasks"`
	Edges  []uuid.UUID `json:"edges,omitempty"`
	Action string      `json:"action"`
	Owner  string      `json:"owner,omitempty"`
}

// GraphFindings finds e0/release loops (SCC), AND of blockers and diamonds from
// the live blocks edges. released(edge) tells whether an edge still holds;
// owners maps a task to the responsible name.
func GraphFindings(edges []TaskDependency, released func(TaskDependency) bool, owners map[uuid.UUID]string) []Finding {
	var blocks []TaskDependency
	for _, d := range edges {
		// A released edge no longer holds anything: it cannot be part of a cycle.
		if d.DependencyType == DependencyTypeBlocks && !released(d) {
			blocks = append(blocks, d)
		}
	}
	var out []Finding

	var prepEdges []TaskDependency
	for _, d := range blocks {
		if gatesPrep(d.Condition) {
			prepEdges = append(prepEdges, d)
		}
	}
	hard := GraphCycleComponents(prepEdges)
	all := GraphCycleComponents(blocks)
	seen := map[string]bool{}
	for _, comp := range hard {
		k := compKey(comp)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, Finding{Kind: FindingHardCycle, Tasks: comp, Edges: edgesWithin(prepEdges, comp), Owner: newestEdgeOwner(prepEdges, comp, owners),
			Action: "break the loop: the owner of the newest edge in the cycle retargets it to the real prerequisite artifact (contract_ready/artifact_published) or splits the card; edges are not removed automatically"})
	}
	for _, comp := range all {
		k := compKey(comp)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, Finding{Kind: FindingSemanticLoop, Tasks: comp, Edges: edgesWithin(blocks, comp), Owner: newestEdgeOwner(blocks, comp, owners),
			Action: "complete the prep stage of every card in the loop now (only the final stage is gated by deployed/accepted/shipped); the owner confirms the final gate on the real install"})
	}

	byDependent := map[uuid.UUID][]TaskDependency{}
	blockersOf := map[uuid.UUID][]uuid.UUID{}
	for _, d := range blocks {
		blockersOf[d.TaskID] = append(blockersOf[d.TaskID], d.DependsOnTaskID)
		if !released(d) {
			byDependent[d.TaskID] = append(byDependent[d.TaskID], d)
		}
	}
	for _, id := range sortedIDs(byDependent) {
		ds := byDependent[id]
		if len(ds) < 2 {
			continue
		}
		f := Finding{Kind: FindingAndBlockers, Tasks: []uuid.UUID{id}, Action: "all listed blockers must be released (AND); the dependent proceeds only on stages none of them gates"}
		for _, d := range ds {
			f.Edges = append(f.Edges, d.ID)
			f.Tasks = append(f.Tasks, d.DependsOnTaskID)
			if f.Owner == "" {
				f.Owner = owners[d.DependsOnTaskID]
			}
		}
		out = append(out, f)
	}
	for _, id := range sortedIDs(blockersOf) {
		bs := blockersOf[id]
		if len(bs) < 2 {
			continue
		}
		anc := make([]map[uuid.UUID]bool, len(bs))
		for i, b := range bs {
			anc[i] = ancestors(b, blockersOf)
		}
		for i := 0; i < len(bs); i++ {
			for j := i + 1; j < len(bs); j++ {
				for root := range anc[i] {
					if anc[j][root] && root != id {
						out = append(out, Finding{Kind: FindingDiamond, Tasks: []uuid.UUID{root, bs[i], bs[j], id}, Owner: owners[root],
							Action: "the common root releases once for both paths; do not duplicate its request on each branch"})
						goto nextNode
					}
				}
			}
		}
	nextNode:
	}
	return out
}

// newestEdgeOwner names the owner of the dependent side of the newest edge
// inside the component (latest CreatedAt, larger id on a tie): the person who
// added the edge last is the one asked to retarget it.
func newestEdgeOwner(edges []TaskDependency, comp []uuid.UUID, owners map[uuid.UUID]string) string {
	in := map[uuid.UUID]bool{}
	for _, id := range comp {
		in[id] = true
	}
	var best *TaskDependency
	for i := range edges {
		d := &edges[i]
		if !in[d.TaskID] || !in[d.DependsOnTaskID] {
			continue
		}
		if best == nil || d.CreatedAt.After(best.CreatedAt) || (d.CreatedAt.Equal(best.CreatedAt) && d.ID.String() > best.ID.String()) {
			best = d
		}
	}
	if best == nil {
		return ""
	}
	return owners[best.TaskID]
}

func gatesPrep(c DependencyCondition) bool {
	for _, s := range StageGates(c) {
		if s == StagePrep {
			return true
		}
	}
	return false
}

func compKey(c []uuid.UUID) string {
	s := make([]string, len(c))
	for i, id := range c {
		s[i] = id.String()
	}
	return strings.Join(s, ",")
}

func edgesWithin(edges []TaskDependency, comp []uuid.UUID) []uuid.UUID {
	in := map[uuid.UUID]bool{}
	for _, id := range comp {
		in[id] = true
	}
	var out []uuid.UUID
	for _, d := range edges {
		if in[d.TaskID] && in[d.DependsOnTaskID] {
			out = append(out, d.ID)
		}
	}
	return out
}

func sortedIDs[V any](m map[uuid.UUID]V) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}

// ancestors is the set of tasks reachable going to blockers, the start included.
func ancestors(start uuid.UUID, blockersOf map[uuid.UUID][]uuid.UUID) map[uuid.UUID]bool {
	seen := map[uuid.UUID]bool{}
	stack := []uuid.UUID{start}
	for len(stack) > 0 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[v] {
			continue
		}
		seen[v] = true
		stack = append(stack, blockersOf[v]...)
	}
	return seen
}

// ---- WAIT reply evaluation -------------------------------------------------

// WaitReply is a comment on a card that has an armed WAIT on a decision.
type WaitReply struct {
	ID        string
	AuthorID  uuid.UUID
	Body      string
	CreatedAt time.Time
	// HasWaitMarker: the last line of the comment is a `⏳ WAIT ...` line.
	HasWaitMarker bool
}

// WaitDecision is the verdict of EvaluateWait.
type WaitDecision struct {
	Resolved  bool   `json:"resolved"`
	ByComment string `json:"by_comment,omitempty"`
	// OwnerOutcome: the owner posted a substantive outcome without a new WAIT.
	// It ends the wait episode; it must not create another wake or bonus.
	OwnerOutcome bool   `json:"owner_outcome"`
	Reason       string `json:"reason"`
}

var quoteLine = regexp.MustCompile("^\\s*[>`]")

// substantive reports that a comment carries its own content: at least one line
// that is not a quote, a code span or a bare marker, and not a short ack.
func substantive(body string) bool {
	n := 0
	fenced := false
	for _, l := range strings.Split(body, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced || t == "" || quoteLine.MatchString(l) || strings.HasPrefix(t, "⏳") {
			continue
		}
		n += len([]rune(inlineCode.ReplaceAllString(t, "")))
	}
	return n >= 20
}

var inlineCode = regexp.MustCompile("`[^`]*`")

// EvaluateWait decides whether a WAIT decision on `recipient`, armed at
// armedAt, is resolved. Only a substantive comment by the recipient after
// armedAt resolves it; a quote, an ack, a comment by anyone else, or a comment
// older than the wait resolves nothing. Replies are evaluated by id, so the same
// reply is never "new" on a later tick (callers pass lastHandled).
func EvaluateWait(recipient uuid.UUID, armedAt time.Time, lastHandled string, replies []WaitReply) WaitDecision {
	sort.Slice(replies, func(i, j int) bool { return replies[i].CreatedAt.Before(replies[j].CreatedAt) })
	d := WaitDecision{Reason: "no authorized answer"}
	var newest *WaitReply
	for i := range replies {
		r := replies[i]
		if !r.CreatedAt.After(armedAt) {
			continue
		}
		if r.AuthorID != recipient {
			d.Reason = "reply by a non-addressed author does not resolve"
			continue
		}
		if !substantive(r.Body) {
			d.Reason = "informational/quoted/ack reply does not resolve"
			continue
		}
		newest = &replies[i] // sorted ascending: the last qualifying reply wins
	}
	if newest == nil {
		return d
	}
	if newest.ID == lastHandled {
		return WaitDecision{Resolved: true, ByComment: newest.ID, Reason: "already handled; no new wake"}
	}
	return WaitDecision{Resolved: true, ByComment: newest.ID, Reason: "addressed substantive reply", OwnerOutcome: !newest.HasWaitMarker}
}

// ---- binding change proposals ----------------------------------------------

var (
	ErrStaleGeneration = errors.New("next-action generation changed: proposal is stale")
	ErrNotOwner        = errors.New("only the authorized owner may confirm")
	ErrAutomatedActor  = errors.New("an LLM/automated actor cannot confirm or satisfy a condition")
	ErrHeldGate        = errors.New("human/security gate cannot be lifted by a binding change")
	ErrReplay          = errors.New("proposal already applied")
)

// BindingProposal suggests moving an edge from one condition to another. It
// carries its sources and scope and is applied only through Confirm.
type BindingProposal struct {
	ID         string              `json:"id"`
	EdgeID     uuid.UUID           `json:"edge_id"`
	Old        DependencyCondition `json:"old"`
	New        DependencyCondition `json:"new"`
	Sources    []string            `json:"sources"`
	Scope      string              `json:"scope"`
	Generation string              `json:"base_generation"`
	Applied    bool                `json:"applied"`
}

// Validate checks that the proposal is complete and changes something.
func (p BindingProposal) Validate() error {
	if strings.TrimSpace(p.Generation) == "" {
		return errors.New("proposal needs a base generation")
	}
	if len(p.Sources) == 0 || strings.TrimSpace(p.Scope) == "" || p.EdgeID == uuid.Nil {
		return errors.New("proposal needs edge, sources and scope")
	}
	if !p.Old.Valid() || !p.New.Valid() || p.Old == p.New {
		return errors.New("proposal needs two different valid conditions")
	}
	return nil
}

// Confirm applies the proposal if the confirming actor is the edge owner, is
// not automated, the generation is still current (CAS) and it was not applied
// before. It returns the condition the edge must take; it never marks anything
// satisfied and never touches a human/security gate.
func (p *BindingProposal) Confirm(actorIsOwner, actorAutomated, gateHeld bool, currentGeneration string) (DependencyCondition, error) {
	switch {
	case p.Applied:
		return "", ErrReplay
	case actorAutomated:
		return "", ErrAutomatedActor
	case !actorIsOwner:
		return "", ErrNotOwner
	case gateHeld:
		return "", ErrHeldGate
	case strings.TrimSpace(currentGeneration) == "" || p.Generation != currentGeneration:
		return "", ErrStaleGeneration
	}
	if err := p.Validate(); err != nil {
		return "", err
	}
	p.Applied = true
	return p.New, nil
}

// Revoked reports whether a previously accepted result no longer counts: the
// generation it was accepted at differs from the current one.
func Revoked(acceptedGeneration, currentGeneration string) bool {
	return acceptedGeneration != "" && acceptedGeneration != currentGeneration
}

// StoredBindingProposal is a BindingProposal as persisted: who proposed it and
// when it was applied. Applied is the single replay guard (set once in SQL).
type StoredBindingProposal struct {
	BindingProposal
	ProposedBy  uuid.UUID  `json:"proposed_by"`
	ConfirmedBy *uuid.UUID `json:"confirmed_by,omitempty"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}
