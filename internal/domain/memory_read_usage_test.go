package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTextMentionsMemory(t *testing.T) {
	id := uuid.MustParse("5eed0099-2e5a-49a3-8eb5-fad43e4d3377")
	cases := []struct {
		name, text, key string
		want            bool
	}{
		{"key as a word", "решение в decision-mesh.", "decision-mesh", true},
		{"key in backticks", "см. `decision-mesh`", "decision-mesh", true},
		{"key is a prefix of a longer key", "decision-mesh-perf-bundle", "decision-mesh", false},
		{"key glued to a word", "xdecision-mesh", "decision-mesh", false},
		{"key case-insensitive", "DECISION-MESH", "decision-mesh", true},
		{"full id", "memory 5eed0099-2e5a-49a3-8eb5-fad43e4d3377 updated", "", true},
		{"8-hex prefix with hash", "запись #5eed0099 устарела", "", true},
		{"8-hex prefix at end", "5eed0099", "", true},
		{"prefix inside a longer hex run", "dead5eed0099", "", false},
		{"prefix followed by hex", "5eed0099ff", "", false},
		{"nothing", "ничего общего", "decision-mesh", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, TextMentionsMemory(strings.ToLower(tc.text), tc.key, id))
		})
	}
}

func TestAttributeMemoryReadUsage_SessionWindowAndSource(t *testing.T) {
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	agent, other := uuid.New(), uuid.New()
	memID, docID := uuid.New(), uuid.New()
	taskA, taskB := uuid.New(), uuid.New()
	sess, otherSess := "s-1", "s-2"
	results := []MemoryReadResult{
		{ID: memID, Key: "decision-x", Source: ReadSourceMemory, Rank: 1},
		{ID: docID, Key: "doc-y", Source: ReadSourceDoc, Rank: 2},
	}
	comment := func(at time.Duration, by uuid.UUID, task *uuid.UUID, text string) UsageText {
		return UsageText{RefKind: UsageRefComment, Ref: uuid.New(), TS: t0.Add(at), AgentID: by, TaskID: task, Text: text}
	}
	got := func(r UsageRead, sig UsageSignals) []MemoryReadUsage {
		return AttributeMemoryReadUsage([]UsageRead{r}, sig)
	}
	read := UsageRead{ID: uuid.New(), TS: t0, AgentID: agent, CallerKind: CallerKindRunner, Results: results}

	t.Run("window is (t, t+2h]", func(t *testing.T) {
		require.Empty(t, got(read, UsageSignals{Texts: []UsageText{comment(0, agent, nil, "decision-x")}}))
		require.Len(t, got(read, UsageSignals{Texts: []UsageText{comment(UsageWindow, agent, nil, "decision-x")}}), 1)
		require.Empty(t, got(read, UsageSignals{Texts: []UsageText{comment(UsageWindow+time.Second, agent, nil, "decision-x")}}))
	})
	t.Run("other agent never counts", func(t *testing.T) {
		require.Empty(t, got(read, UsageSignals{Texts: []UsageText{comment(time.Minute, other, nil, "decision-x")}}))
	})
	t.Run("doc rows are not attributed", func(t *testing.T) {
		require.Empty(t, got(read, UsageSignals{Texts: []UsageText{comment(time.Minute, agent, nil, "doc-y")}}))
	})
	t.Run("caller kind is kept", func(t *testing.T) {
		u := got(read, UsageSignals{Texts: []UsageText{comment(time.Minute, agent, nil, "decision-x")}})
		require.Len(t, u, 1)
		require.Equal(t, CallerKindRunner, u[0].CallerKind)
		require.Equal(t, UsageSignalMention, u[0].Signal)
	})
	t.Run("session id must match for get_memory", func(t *testing.T) {
		r := read
		r.SessionID = &sess
		g := func(s *string) UsageSignals {
			return UsageSignals{GetMemory: []UsageGetMemory{{ID: uuid.New(), TS: t0.Add(time.Minute), AgentID: agent, SessionID: s,
				Results: []MemoryReadResult{{ID: memID, Key: "decision-x"}}}}}
		}
		require.Len(t, got(r, g(&sess)), 1)
		require.Empty(t, got(r, g(&otherSess)))
		require.Empty(t, got(r, g(nil)))
	})
	t.Run("without session the task must match", func(t *testing.T) {
		r := read
		r.TaskID = &taskA
		require.Len(t, got(r, UsageSignals{Texts: []UsageText{comment(time.Minute, agent, &taskA, "decision-x")}}), 1)
		require.Empty(t, got(r, UsageSignals{Texts: []UsageText{comment(time.Minute, agent, &taskB, "decision-x")}}))
		// A text outside any card (doc, remember) matches on the agent.
		require.Len(t, got(r, UsageSignals{Texts: []UsageText{{RefKind: UsageRefDocument, Ref: uuid.New(), TS: t0.Add(time.Minute), AgentID: agent, Text: "decision-x"}}}), 1)
	})
	t.Run("remember content of the memory itself is not a mention", func(t *testing.T) {
		self := memID
		require.Empty(t, got(read, UsageSignals{Texts: []UsageText{{RefKind: UsageRefMemoryRevision, Ref: uuid.New(), TS: t0.Add(time.Minute), AgentID: agent, Text: "decision-x", MemoryID: &self}}}))
	})
	t.Run("revision of the served memory", func(t *testing.T) {
		u := got(read, UsageSignals{Revisions: []UsageRevision{{Ref: uuid.New(), TS: t0.Add(time.Minute), AgentID: agent, MemoryID: memID}}})
		require.Len(t, u, 1)
		require.Equal(t, UsageSignalRevision, u[0].Signal)
		require.Empty(t, got(read, UsageSignals{Revisions: []UsageRevision{{Ref: uuid.New(), TS: t0.Add(time.Minute), AgentID: other, MemoryID: memID}}}))
	})
}
