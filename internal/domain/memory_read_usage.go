package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Recall usage attribution (memory_read_usage, plan §3 «Полезный recall»).
//
// A recall read r is useful when, within W(r) = (t_r, t_r + UsageWindow] of
// the same session S(r), the same agent acts on a row i of its answer whose
// source is memory or project_knowledge: (a) reads i with get_memory, (b) names
// key(i), id(i) or its 8-hex prefix in a comment, task description, Mesh Doc or
// remember content, or (c) updates, archives or supersedes i.

// UsageWindow is the length of W(r).
const UsageWindow = 2 * time.Hour

// MemoryMissWindow bounds "later in the same session" of a search miss (plan §3
// «Промах поиска», U7): the re-found recall r' or the rewritten memory m' must
// come within (t_r, t_r + MemoryMissWindow]. The SQL function
// memory_search_misses (migration 20261120500) holds the same interval.
const MemoryMissWindow = 2 * time.Hour

// Signals of memory_read_usage.signal.
const (
	UsageSignalGetMemory = "get_memory"
	UsageSignalMention   = "mention"
	UsageSignalRevision  = "revision"
)

// Kinds of the object a signal_ref points at (memory_read_usage.ref_kind).
const (
	UsageRefReadEvent      = "read_event"
	UsageRefComment        = "comment"
	UsageRefTask           = "task"
	UsageRefDocument       = "document"
	UsageRefMemoryRevision = "memory_revision"
)

// RecallReadOps are the journal ops whose answers are attributed.
var RecallReadOps = []string{"recall", "recall-final", "recall_graph"}

// UsageAttributedSource reports whether a served row of this source can be
// attributed (§3: memory and project knowledge, not docs or updates).
func UsageAttributedSource(source string) bool {
	return source == ReadSourceMemory || source == ReadSourceProjectKnowledge
}

// UsageRead is a recall read waiting for attribution.
type UsageRead struct {
	ID         uuid.UUID
	TS         time.Time
	AgentID    uuid.UUID
	SessionID  *string
	TaskID     *uuid.UUID
	CallerKind string
	Results    []MemoryReadResult
}

// UsageGetMemory is a get_memory read: signal (a). It carries its own session.
type UsageGetMemory struct {
	ID        uuid.UUID
	TS        time.Time
	AgentID   uuid.UUID
	SessionID *string
	TaskID    *uuid.UUID
	Results   []MemoryReadResult
}

// UsageText is a text the agent wrote: signal (b). TaskID is the card the text
// belongs to when it has one (a comment); nil for texts outside a card.
type UsageText struct {
	RefKind  string
	Ref      uuid.UUID
	TS       time.Time
	AgentID  uuid.UUID
	TaskID   *uuid.UUID
	Text     string
	MemoryID *uuid.UUID // remember content: the memory written (its own revision is not a mention of itself)
}

// UsageRevision is a revision of a memory by the agent: signal (c). MemoryID is
// the memory the revision acts on (update/archive of i), or, for supersede,
// the memory i that the revised memory superseded.
type UsageRevision struct {
	Ref      uuid.UUID
	TS       time.Time
	AgentID  uuid.UUID
	MemoryID uuid.UUID
}

// UsageSignals are the candidate signals around a batch of reads.
type UsageSignals struct {
	GetMemory []UsageGetMemory
	Texts     []UsageText
	Revisions []UsageRevision
}

// MemoryReadUsage is one row of memory_read_usage.
type MemoryReadUsage struct {
	ReadEventID uuid.UUID
	MemoryID    uuid.UUID
	Signal      string
	RefKind     string
	SignalRef   uuid.UUID
	TS          time.Time
	AgentID     uuid.UUID
	CallerKind  string
}

// inUsageWindow reports ts ∈ (t, t + UsageWindow].
func inUsageWindow(t, ts time.Time) bool {
	return ts.After(t) && !ts.After(t.Add(UsageWindow))
}

// sameSession reports whether a signal of agent sigAgent, with session sigSession
// (nil when the source has none) and card sigTask, belongs to S(r).
//
// S(r) is r's session_id; without it the pair (agent, task); without a task the
// agent alone (the window bounds it). A source that carries a session (a
// get_memory read) must match r's session exactly. Sources without one
// (comments, descriptions, docs, revisions) match on the agent, and on the card
// when both r and the source name one.
func sameSession(r *UsageRead, sigAgent uuid.UUID, hasSession bool, sigSession *string, sigTask *uuid.UUID) bool {
	if sigAgent != r.AgentID {
		return false
	}
	if hasSession {
		switch {
		case r.SessionID != nil:
			return sigSession != nil && *sigSession == *r.SessionID
		case r.TaskID != nil:
			return sigTask != nil && *sigTask == *r.TaskID
		default:
			return true
		}
	}
	if r.TaskID != nil && sigTask != nil {
		return *sigTask == *r.TaskID
	}
	return true
}

// AttributeMemoryReadUsage returns the usage rows of reads given the candidate
// signals. A row is emitted per (read, memory, signal, ref); duplicates of one
// key collapse.
func AttributeMemoryReadUsage(reads []UsageRead, sig UsageSignals) []MemoryReadUsage {
	var out []MemoryReadUsage
	seen := map[[4]string]bool{}
	emit := func(r *UsageRead, memID uuid.UUID, signal, refKind string, ref uuid.UUID, ts time.Time) {
		k := [4]string{r.ID.String(), memID.String(), signal, ref.String()}
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, MemoryReadUsage{
			ReadEventID: r.ID, MemoryID: memID, Signal: signal, RefKind: refKind,
			SignalRef: ref, TS: ts, AgentID: r.AgentID, CallerKind: r.CallerKind,
		})
	}
	for ri := range reads {
		r := &reads[ri]
		rows := attributableRows(r.Results)
		if len(rows) == 0 {
			continue
		}
		for _, g := range sig.GetMemory {
			if !inUsageWindow(r.TS, g.TS) || !sameSession(r, g.AgentID, true, g.SessionID, g.TaskID) {
				continue
			}
			for _, row := range rows {
				for _, got := range g.Results {
					if got.ID == row.ID || (row.Key != "" && got.Key == row.Key) {
						emit(r, row.ID, UsageSignalGetMemory, UsageRefReadEvent, g.ID, g.TS)
						break
					}
				}
			}
		}
		for _, tx := range sig.Texts {
			if !inUsageWindow(r.TS, tx.TS) || !sameSession(r, tx.AgentID, false, nil, tx.TaskID) {
				continue
			}
			lower := strings.ToLower(tx.Text)
			for _, row := range rows {
				if tx.MemoryID != nil && *tx.MemoryID == row.ID {
					continue
				}
				if TextMentionsMemory(lower, row.Key, row.ID) {
					emit(r, row.ID, UsageSignalMention, tx.RefKind, tx.Ref, tx.TS)
				}
			}
		}
		for _, rv := range sig.Revisions {
			if !inUsageWindow(r.TS, rv.TS) || !sameSession(r, rv.AgentID, false, nil, nil) {
				continue
			}
			for _, row := range rows {
				if rv.MemoryID == row.ID {
					emit(r, row.ID, UsageSignalRevision, UsageRefMemoryRevision, rv.Ref, rv.TS)
				}
			}
		}
	}
	return out
}

func attributableRows(results []MemoryReadResult) []MemoryReadResult {
	var rows []MemoryReadResult
	for _, row := range results {
		if row.ID != uuid.Nil && UsageAttributedSource(row.Source) {
			rows = append(rows, row)
		}
	}
	return rows
}

// TextMentionsMemory reports whether lowerText (already lower-cased) names the
// memory by key, full id or the id's 8-hex prefix. A key counts only as a whole
// token: the characters next to it are not letters, digits, '-' or '_', so the
// key decision-mesh is not found inside decision-mesh-perf. The id prefix counts
// only when no hex digit touches it, which also finds the full id (its prefix
// is followed by '-').
func TextMentionsMemory(lowerText, key string, id uuid.UUID) bool {
	if k := strings.ToLower(strings.TrimSpace(key)); k != "" && containsBounded(lowerText, k, isKeyChar) {
		return true
	}
	if id == uuid.Nil {
		return false
	}
	return containsBounded(lowerText, id.String()[:8], isHexChar)
}

func containsBounded(text, needle string, inToken func(byte) bool) bool {
	for from := 0; from+len(needle) <= len(text); {
		i := strings.Index(text[from:], needle)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(needle)
		if (start == 0 || !inToken(text[start-1])) && (end == len(text) || !inToken(text[end])) {
			return true
		}
		from = start + 1
	}
	return false
}

func isKeyChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_'
}

func isHexChar(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}
