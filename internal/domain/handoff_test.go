package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func handoffRules(lead string, chain ...string) *EffectiveAssignmentRules {
	r := &EffectiveAssignmentRules{FallbackChain: chain}
	if lead != "" {
		r.DefaultAssignee = &EffectiveAssignmentRule{Value: lead, Source: "project"}
	}
	return r
}

func TestDecideHandoff_OffByDefault(t *testing.T) {
	hot := HandoffFacts{Starts: 99, Turns: 99, Compactions: 99, Current: "dave"}
	rules := handoffRules("carol", "alice")
	assert.Equal(t, HandoffOutcomeNone, DecideHandoff(nil, rules, hot).Outcome)
	assert.Equal(t, HandoffOutcomeNone, DecideHandoff(&HandoffConfig{StartCap: 1}, rules, hot).Outcome, "enabled=false")
}

func TestDecideHandoff_UnderCap(t *testing.T) {
	cfg := &HandoffConfig{Enabled: true, StartCap: 3, TurnCap: 10, CompactionCap: 2}
	d := DecideHandoff(cfg, handoffRules("carol"), HandoffFacts{Starts: 2, Turns: 9, Compactions: 1, Current: "dave"})
	assert.Equal(t, HandoffOutcomeNone, d.Outcome)
}

func TestDecideHandoff_EachCap(t *testing.T) {
	cfg := &HandoffConfig{Enabled: true, StartCap: 3, TurnCap: 10, CompactionCap: 2}
	rules := handoffRules("carol")
	for capName, f := range map[string]HandoffFacts{
		HandoffFactStart:      {Starts: 3, Current: "dave"},
		HandoffFactTurn:       {Turns: 10, Current: "dave"},
		HandoffFactCompaction: {Compactions: 2, Current: "dave"},
	} {
		d := DecideHandoff(cfg, rules, f)
		assert.Equal(t, HandoffOutcomeHandoff, d.Outcome, capName)
		assert.Equal(t, capName, d.Cap)
		assert.Equal(t, "carol", d.Recipient)
		assert.Equal(t, DefaultHandoffBanner, d.Banner)
	}
}

// The earlier defect: Dave → Carol → Alice → back. With Visited fed back the
// chain walks forward once and then stops instead of circling.
func TestDecideHandoff_ChainNoCycle(t *testing.T) {
	cfg := &HandoffConfig{Enabled: true, StartCap: 1}
	rules := handoffRules("agent:carol", "agent:alice", "dave")
	f := HandoffFacts{Starts: 5, Current: "dave"}

	var path []string
	for i := 0; i < 10; i++ {
		d := DecideHandoff(cfg, rules, f)
		if d.Outcome != HandoffOutcomeHandoff {
			assert.Equal(t, HandoffOutcomeExhausted, d.Outcome)
			assert.Empty(t, d.Recipient)
			break
		}
		path = append(path, d.Recipient)
		f.Visited = append(f.Visited, f.Current)
		f.Current = d.Recipient
	}
	assert.Equal(t, []string{"agent:carol", "agent:alice"}, path)
}

func TestDecideHandoff_NoLeadFallsToChain(t *testing.T) {
	cfg := &HandoffConfig{Enabled: true, TurnCap: 1}
	d := DecideHandoff(cfg, handoffRules("", "agent:Alice", "carol"), HandoffFacts{Turns: 1, Current: "dave"})
	assert.Equal(t, HandoffOutcomeHandoff, d.Outcome)
	assert.Equal(t, "agent:Alice", d.Recipient)
}

func TestDecideHandoff_NoLeadNoChainExhausted(t *testing.T) {
	cfg := &HandoffConfig{Enabled: true, TurnCap: 1, Banner: "CAP"}
	for _, rules := range []*EffectiveAssignmentRules{nil, handoffRules("")} {
		d := DecideHandoff(cfg, rules, HandoffFacts{Turns: 1, Current: "dave"})
		assert.Equal(t, HandoffOutcomeExhausted, d.Outcome)
		assert.Equal(t, "CAP", d.Banner)
	}
}

func TestDecideHandoff_SkipsCurrentAndDuplicates(t *testing.T) {
	cfg := &HandoffConfig{Enabled: true, StartCap: 1}
	d := DecideHandoff(cfg, handoffRules("carol", "carol", "agent:CAROL", "alice"), HandoffFacts{Starts: 1, Current: "agent:carol"})
	assert.Equal(t, "alice", d.Recipient)
}

func TestDecideHandoff_PrefixCaseInsensitive(t *testing.T) {
	cfg := &HandoffConfig{Enabled: true, StartCap: 1}
	d := DecideHandoff(cfg, handoffRules("carol", "alice"), HandoffFacts{Starts: 1, Current: "Agent:carol"})
	assert.Equal(t, "alice", d.Recipient)
	d = DecideHandoff(cfg, handoffRules("AGENT:Carol", "alice"), HandoffFacts{Starts: 1, Current: "carol"})
	assert.Equal(t, "alice", d.Recipient)
}
