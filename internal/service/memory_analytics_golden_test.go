package service

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

var updateGolden = flag.Bool("golden-update", false, "rewrite web/e2e-memory/u5-golden.json from the current types")

// goldenAt is the fixed clock of the sample: 2026-11-18T12:00:00Z.
func goldenAt() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }

// TestMemoryAnalyticsGolden pins the JSON shape of /analytics/memory and its
// drill-downs to web/e2e-memory/u5-golden.json — the file the U6 widget renders
// its fixtures from. A field name, an omitempty or a null-versus-0 change on
// the Go side must consciously regenerate that file:
//
//	go test ./internal/service -run TestMemoryAnalyticsGolden -golden-update
func TestMemoryAnalyticsGolden(t *testing.T) {
	at := goldenAt()
	id := uuid.MustParse("6a1b669f-5cb4-4822-99fc-45cdc5b0fc95")
	name := "Liam"
	key := "deploy-url"
	hash := "h1"
	text := "how to restart runner"
	kind := "fact"
	group := "g1"
	day := "2026-11-17"
	agentID, recall := id.String(), "recall"
	kpis := MemoryKPIs{
		Reads: i64(84), Writes: i64(30), WithReason: i64(24),
		HitRate: f64(0.75), UsefulRate: f64(0.6), MissRate: f64(0.25),
		Dead30: f64(0.4), TokensEst: i64(12000), SupersededServedRate: f64(0.05),
	}
	row := MemoryBreakdownRow{
		Key: &agentID, Label: name, Key2: &recall, Label2: "recall",
		Reads: i64(50), Hits: i64(40), Useful: i64(28), Writes: i64(20), WithReason: i64(16),
		HitRate: f64(0.8), UsefulRate: f64(0.7), TokensEst: i64(8000),
		ServedRows:           i64(90),
		SupersededServedRate: f64(0.05),
		Reasons:              map[string]string{"superseded_served_rate": "no_data"},
	}
	coverage := &MemoryDetailCoverage{
		DetailSince: ptrTime(at.AddDate(0, 0, -30)), Reason: MemoryReasonRetention,
	}
	sample := struct {
		Analytics      *MemoryAnalytics                 `json:"analytics"`
		EmptyQueries   *MemoryList[MemoryEmptyQuery]    `json:"empty_queries"`
		Records        *MemoryList[MemoryRecordRow]     `json:"records"`
		UnknownSources *MemoryList[MemoryUnknownSource] `json:"unknown_sources"`
	}{
		Analytics: &MemoryAnalytics{
			Period:         MemoryPeriod{From: at.AddDate(0, 0, -7), To: at},
			PreviousPeriod: MemoryPeriod{From: at.AddDate(0, 0, -15), To: at.AddDate(0, 0, -7)},
			KPIs:           kpis,
			Previous:       kpis,
			Series: []MemorySeriesPoint{{
				Day: day, Reads: i64(12),
				ReadsByOp:          map[string]int64{"get_memory": 4, "recall": 8},
				ServedRowsBySource: map[string]int64{"doc": 5, "memory": 20},
				UsefulRate:         f64(0.6), Writes: i64(4), WithReason: i64(3), Hits: i64(6), Useful: i64(4),
				Reasons: map[string]string{"writes": MemoryReasonExcludedByFilter},
			}},
			Rows:   []MemoryBreakdownRow{row},
			Totals: row,
			Quality: MemoryQuality{
				Day: &day, Total: i64(130), Active: i64(100), Archived: i64(30),
				SupersededActive: i64(8), Conflicted: i64(2), NoKind: i64(10),
				StatuslogLike: i64(5), DupGroups: i64(3), DupRows: i64(7),
				Dead30: i64(40), Dead90: i64(25),
			},
			Coverage: MemoryCoverage{
				JournalSince: ptrTime(at.AddDate(0, 0, -365)), UnknownShare: f64(0.02),
				CallerKind: []MemoryCallerKindRow{{CallerKind: "agent_tool", Reads: 70}},
			},
			Reasons: map[string]string{"quality.dead90": MemoryReasonNoJournalBefore},
		},
		EmptyQueries: &MemoryList[MemoryEmptyQuery]{
			Items: []MemoryEmptyQuery{{
				QueryHash: hash, QueryText: &text, Redacted: false,
				Reason: MemoryReasonTextExpired, AgentID: &id, AgentName: &name,
				Count: 3, LastAt: at,
			}},
			Total: 1, Page: 1, PageSize: 20, Reason: "no_data", Coverage: coverage,
		},
		Records: &MemoryList[MemoryRecordRow]{
			Items: []MemoryRecordRow{{
				MemoryID: id, Key: &key, Redacted: false, Scope: "workspace", Kind: &kind,
				ProjectID: &id, AgentID: &id, CreatedAt: at,
				Served: 12, Useful: 5, LastServed: ptrTime(at), GroupID: &group, GroupSize: 2,
			}},
			Total: 1, Page: 1, PageSize: 20, Reason: "no_data", Coverage: coverage,
		},
		UnknownSources: &MemoryList[MemoryUnknownSource]{
			Items: []MemoryUnknownSource{{
				Route: "/api/v1/memories/recall", UserAgent: "curl/8", Reads: 9,
				SampleKeys: []string{key},
			}},
			Total: 1, Page: 1, PageSize: 20, Reason: "no_data", Coverage: coverage,
		},
	}
	got, err := json.MarshalIndent(sample, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("..", "..", "web", "e2e-memory", "u5-golden.json")
	if *updateGolden {
		if writeErr := os.WriteFile(path, got, 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(want, got) {
		return
	}
	gotLines, wantLines := bytes.Split(got, []byte("\n")), bytes.Split(want, []byte("\n"))
	for i := 0; i < len(gotLines); i++ {
		var wantLine []byte
		if i < len(wantLines) {
			wantLine = wantLines[i]
		}
		if !bytes.Equal(gotLines[i], wantLine) {
			t.Fatalf("u5-golden.json mismatch at line %d:\n got: %s\nwant: %s\nregenerate: go test ./internal/service -run TestMemoryAnalyticsGolden -golden-update",
				i+1, gotLines[i], wantLine)
		}
	}
	t.Fatalf("u5-golden.json differs (%d vs %d bytes); regenerate: go test ./internal/service -run TestMemoryAnalyticsGolden -golden-update", len(got), len(want))
}

func f64(v float64) *float64 { return &v }

func ptrTime(t time.Time) *time.Time { return &t }
