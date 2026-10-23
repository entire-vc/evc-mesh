package domain

import "strings"

// HandoffConfig is the project's policy for passing a card on when the runner
// reports that a session hit a cap (rule_type = "workflow", key "handoff").
//
// The runner only reports facts (starts, turns, compactions for one card). Whether
// a cap is hit and who gets the card next is decided here, from the project's
// assignment rules, so every client of the board gets the same answer.
//
// Off unless Enabled is true: a project without the block, or with it disabled,
// gets HandoffNone for every report and nothing in production changes.
type HandoffConfig struct {
	Enabled bool `json:"enabled,omitempty"`
	// Caps per card; 0 = that cap is not enforced.
	StartCap      int `json:"start_cap,omitempty"`
	TurnCap       int `json:"turn_cap,omitempty"`
	CompactionCap int `json:"compaction_cap,omitempty"`
	// Banner is prepended to the handoff comment text. Empty = DefaultHandoffBanner.
	Banner string `json:"banner,omitempty"`
}

// DefaultHandoffBanner is used when the project config names none.
const DefaultHandoffBanner = "Handoff"

// Handoff fact kinds the runner reports.
const (
	HandoffFactStart      = "start"
	HandoffFactTurn       = "turn"
	HandoffFactCompaction = "compaction"
)

// Handoff outcomes.
const (
	HandoffOutcomeNone      = "none"      // policy off, or no cap reached
	HandoffOutcomeHandoff   = "handoff"   // pass the card to Recipient
	HandoffOutcomeExhausted = "exhausted" // cap reached but nobody in the chain is left
)

// HandoffFacts is what the runner reports about one card.
type HandoffFacts struct {
	Starts      int `json:"starts"`
	Turns       int `json:"turns"`
	Compactions int `json:"compactions"`
	// Current is the card's present assignee ("agent:<id|slug>" or bare id/slug).
	Current string `json:"current"`
	// Visited are the assignees the card has already been handed through, so a
	// chain can never come back to one of them.
	Visited []string `json:"visited,omitempty"`
}

// HandoffDecision is the server's answer.
type HandoffDecision struct {
	Outcome   string `json:"outcome"`
	Cap       string `json:"cap,omitempty"` // start | turn | compaction
	Recipient string `json:"recipient,omitempty"`
	Banner    string `json:"banner,omitempty"`
}

// handoffKey normalises an assignee reference for comparison.
func handoffKey(s string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "agent:")
}

// reachedCap returns the first cap the facts hit, in the order start, turn, compaction.
func (c *HandoffConfig) reachedCap(f HandoffFacts) string {
	switch {
	case c.StartCap > 0 && f.Starts >= c.StartCap:
		return HandoffFactStart
	case c.TurnCap > 0 && f.Turns >= c.TurnCap:
		return HandoffFactTurn
	case c.CompactionCap > 0 && f.Compactions >= c.CompactionCap:
		return HandoffFactCompaction
	}
	return ""
}

// DecideHandoff picks the next assignee. Candidates are the project's
// default_assignee (when it has one) followed by fallback_chain in order; a
// project without a lead therefore falls straight to the chain. The current
// assignee and everyone in Visited are skipped, which makes a cycle impossible:
// each handoff strictly consumes one candidate. With nobody left the outcome is
// HandoffOutcomeExhausted and the caller escalates instead of looping.
//
// Nil-safe: a nil or disabled config returns HandoffOutcomeNone.
func DecideHandoff(cfg *HandoffConfig, rules *EffectiveAssignmentRules, f HandoffFacts) HandoffDecision {
	if cfg == nil || !cfg.Enabled {
		return HandoffDecision{Outcome: HandoffOutcomeNone}
	}
	capHit := cfg.reachedCap(f)
	if capHit == "" {
		return HandoffDecision{Outcome: HandoffOutcomeNone}
	}
	banner := cfg.Banner
	if banner == "" {
		banner = DefaultHandoffBanner
	}
	d := HandoffDecision{Outcome: HandoffOutcomeExhausted, Cap: capHit, Banner: banner}

	skip := map[string]bool{handoffKey(f.Current): true}
	for _, v := range f.Visited {
		skip[handoffKey(v)] = true
	}
	var candidates []string
	if rules != nil {
		if rules.DefaultAssignee != nil && rules.DefaultAssignee.Value != "" {
			candidates = append(candidates, rules.DefaultAssignee.Value)
		}
		candidates = append(candidates, rules.FallbackChain...)
	}
	for _, c := range candidates {
		k := handoffKey(c)
		if k == "" || skip[k] {
			continue
		}
		d.Outcome = HandoffOutcomeHandoff
		d.Recipient = c
		return d
	}
	return d
}
