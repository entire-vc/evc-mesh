package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Reasons a memory metric carries instead of a number. A metric without data is
// null with one of these, never 0: 0 is a measured value.
const (
	// MemoryReasonNoJournalBefore: the read journal does not reach back far
	// enough (or is empty) for this metric over the requested window.
	MemoryReasonNoJournalBefore = "no_journal_before"
	// MemoryReasonNotCollected: the signal behind the metric is not recorded yet.
	MemoryReasonNotCollected = "not_collected"
	// MemoryReasonExcludedByFilter: the op filter leaves nothing this metric counts.
	MemoryReasonExcludedByFilter = "excluded_by_filter"
	MemoryReasonRetention        = "retention"
	MemoryReasonTextExpired      = "text_expired"
	MemoryReasonNotApplicable    = "not_applicable"
	MemoryReasonDetailTimeout    = "detail_timeout"
)

// Read operations of the memory read journal, in analytics names: the journal's
// recall-final collapses into recall and recall_graph becomes recall_with_graph
// (U4 memory_stats_rollup does the same on the aggregate side).
var memoryReadOps = []string{"recall", "recall_with_graph", "get_memory", "get_project_knowledge", "get_project_updates"}

// memoryRawOps expands analytics ops to the raw memory_read_events.op values
// they cover, so a drill-down over the raw journal selects the same calls the
// aggregates count (recall-final is most of recall in production).
func memoryRawOps(ops []string) []string {
	raw := make([]string, 0, len(ops)+1)
	for _, op := range ops {
		switch op {
		case "recall":
			raw = append(raw, "recall", "recall-final")
		case "recall_with_graph":
			raw = append(raw, "recall_graph")
		default:
			raw = append(raw, op)
		}
	}
	return raw
}

// memoryRecallOps are the reads the hit, useful, miss and noise rates are about (§3 R).
var memoryRecallOps = []string{"recall", "recall_with_graph"}

// Memory analytics dimensions accepted by group_by / group_by2.
var memoryGroupDims = []string{"agent", "project", "op", "source", "caller_kind", "kind", "scope", "day"}

// MemoryAnalyticsFilter is the selection of GET /analytics/memory and of every
// drill-down and export under it.
type MemoryAnalyticsFilter struct {
	WorkspaceID  uuid.UUID
	ProjectID    *uuid.UUID
	From         time.Time
	To           time.Time
	PreviousFrom *time.Time
	PreviousTo   *time.Time
	AgentIDs     []uuid.UUID
	// Ops: "read", "write" or concrete operations (recall, get_memory, created, archived …).
	Ops         []string
	Sources     []string
	CallerKinds []string
	Kinds       []string
	Scopes      []string
	DeadDays    int
	GroupBy     string
	GroupBy2    string
	TopN        int
	Viewer      AnalyticsViewer
	// Now is the clock of the request; zero means time.Now().
	Now time.Time
}

// MemoryListParams pages, orders and searches a drill-down list.
type MemoryListParams struct {
	Page     int
	PageSize int
	Sort     string
	Search   string
}

// MemoryPeriod is a half-open window [from, to).
type MemoryPeriod struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// MemoryKPIs are the headline numbers. A nil field is a metric without data:
// MemoryAnalytics.Reasons names why.
type MemoryKPIs struct {
	Reads                *int64   `json:"reads"`
	Writes               *int64   `json:"writes"`
	WithReason           *int64   `json:"with_reason"`
	HitRate              *float64 `json:"hit_rate"`
	UsefulRate           *float64 `json:"useful_rate"`
	MissRate             *float64 `json:"miss_rate"`
	Dead30               *float64 `json:"dead30"`
	TokensEst            *int64   `json:"tokens_est"`
	SupersededServedRate *float64 `json:"superseded_served_rate"`
}

// MemorySeriesPoint is one day of the time series.
type MemorySeriesPoint struct {
	Day                string            `json:"day"`
	Reads              *int64            `json:"reads"`
	ReadsByOp          map[string]int64  `json:"reads_by_op"`
	ServedRowsBySource map[string]int64  `json:"served_rows_by_source"`
	UsefulRate         *float64          `json:"useful_rate"`
	Writes             *int64            `json:"writes"`
	WithReason         *int64            `json:"with_reason"`
	Hits               *int64            `json:"hits"`
	Useful             *int64            `json:"useful"`
	Reasons            map[string]string `json:"reasons,omitempty"`
}

// MemoryBreakdownRow is one row of the group_by[/group_by2] table.
type MemoryBreakdownRow struct {
	Key                  *string           `json:"key"`
	Label                string            `json:"label"`
	Key2                 *string           `json:"key2"`
	Label2               string            `json:"label2,omitempty"`
	Reads                *int64            `json:"reads"`
	Hits                 *int64            `json:"hits"`
	Useful               *int64            `json:"useful"`
	Writes               *int64            `json:"writes"`
	WithReason           *int64            `json:"with_reason"`
	HitRate              *float64          `json:"hit_rate"`
	UsefulRate           *float64          `json:"useful_rate"`
	TokensEst            *int64            `json:"tokens_est"`
	ServedRows           *int64            `json:"served_rows"`
	SupersededServedRate *float64          `json:"superseded_served_rate"`
	Reasons              map[string]string `json:"reasons,omitempty"`
}

// MemoryQuality is the base snapshot at the end of the window (memory_base_daily).
type MemoryQuality struct {
	Day              *string `json:"day"`
	Total            *int64  `json:"total"`
	Active           *int64  `json:"active"`
	Archived         *int64  `json:"archived"`
	SupersededActive *int64  `json:"superseded_active"`
	Conflicted       *int64  `json:"conflicted"`
	NoKind           *int64  `json:"no_kind"`
	StatuslogLike    *int64  `json:"statuslog_like"`
	DupGroups        *int64  `json:"dup_groups"`
	DupRows          *int64  `json:"dup_rows"`
	Dead30           *int64  `json:"dead30"`
	Dead90           *int64  `json:"dead90"`
}

// MemoryCallerKindRow is the number of reads one caller kind made.
type MemoryCallerKindRow struct {
	CallerKind string `json:"caller_kind" db:"caller_kind"`
	Reads      int64  `json:"reads" db:"reads"`
}

// MemoryCoverage says how much of the window the read journal covers and who reads.
type MemoryCoverage struct {
	JournalSince *time.Time            `json:"journal_since"`
	UnknownShare *float64              `json:"unknown_share"`
	CallerKind   []MemoryCallerKindRow `json:"caller_kind"`
}

// MemoryAnalytics is the answer of GET /analytics/memory.
type MemoryAnalytics struct {
	Period         MemoryPeriod         `json:"period"`
	PreviousPeriod MemoryPeriod         `json:"previous_period"`
	KPIs           MemoryKPIs           `json:"kpis"`
	Previous       MemoryKPIs           `json:"previous_kpis"`
	Series         []MemorySeriesPoint  `json:"series"`
	Rows           []MemoryBreakdownRow `json:"rows"`
	Totals         MemoryBreakdownRow   `json:"totals"`
	Quality        MemoryQuality        `json:"quality"`
	Coverage       MemoryCoverage       `json:"coverage"`
	// Reasons maps a null metric ("useful_rate", "coverage.unknown_share",
	// "quality.dead30", "previous_kpis.reads" …) to why it has no value.
	Reasons map[string]string `json:"reasons"`
}

// MemoryEmptyQuery is one normalised query that came back empty.
type MemoryEmptyQuery struct {
	QueryHash string     `json:"query_hash" db:"query_hash"`
	QueryText *string    `json:"query_text" db:"query_text"`
	Redacted  bool       `json:"redacted" db:"redacted"`
	Reason    string     `json:"reason,omitempty" db:"reason"`
	AgentID   *uuid.UUID `json:"agent_id" db:"agent_id"`
	AgentName *string    `json:"agent_name" db:"agent_name"`
	Count     int64      `json:"count" db:"cnt"`
	LastAt    time.Time  `json:"last_at" db:"last_at"`
}

// MemoryRecordRow is one memory record in a drill-down. Content is never sent:
// the key leads to the ordinary memory view under its own rights.
type MemoryRecordRow struct {
	MemoryID   uuid.UUID  `json:"memory_id" db:"memory_id"`
	Key        *string    `json:"key" db:"key"`
	Redacted   bool       `json:"redacted" db:"redacted"`
	Scope      string     `json:"scope" db:"scope"`
	Kind       *string    `json:"kind" db:"kind"`
	ProjectID  *uuid.UUID `json:"project_id" db:"project_id"`
	AgentID    *uuid.UUID `json:"agent_id" db:"agent_id"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	Served     int64      `json:"served" db:"served"`
	Useful     int64      `json:"useful" db:"useful"`
	LastServed *time.Time `json:"last_served_at" db:"last_served_at"`
	GroupID    *string    `json:"group_id,omitempty" db:"group_id"`
	GroupSize  int64      `json:"group_size,omitempty" db:"group_size"`
}

// MemoryUnknownSource is one (route, user agent) that read memory without a caller header.
type MemoryUnknownSource struct {
	Route      string   `json:"route" db:"route"`
	UserAgent  string   `json:"user_agent" db:"user_agent"`
	Reads      int64    `json:"reads" db:"reads"`
	SampleKeys []string `json:"sample_keys"`
}

// MemoryList is a page of a drill-down. Reason is set (and Items empty) when the
// list has no data to be computed from.
type MemoryList[T any] struct {
	Items    []T                   `json:"items"`
	Total    int64                 `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
	Reason   string                `json:"reason,omitempty"`
	Coverage *MemoryDetailCoverage `json:"coverage,omitempty"`
}

// MemoryDetailCoverage names the actual beginning of the retained selection.
// A clipping reason does not mean that the entire list is unavailable.
type MemoryDetailCoverage struct {
	DetailSince *time.Time `json:"detail_since"`
	Reason      string     `json:"reason,omitempty"`
}

// MemoryAnalyticsService answers the memory widget and its drill-downs.
type MemoryAnalyticsService interface {
	Get(ctx context.Context, f MemoryAnalyticsFilter) (*MemoryAnalytics, error)
	EmptyQueries(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryEmptyQuery], error)
	TopRecords(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryRecordRow], error)
	DeadRecords(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryRecordRow], error)
	Duplicates(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryRecordRow], error)
	UnknownSources(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryUnknownSource], error)
}
