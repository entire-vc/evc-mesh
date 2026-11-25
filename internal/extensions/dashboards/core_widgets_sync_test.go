package dashboards

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The web client draws core widgets from its own descriptors, but sizes,
// categories and the ids a `metric` tile may name have one source: catalog.go. web/src/core/dashboard/core-widgets.json
// is its generated copy, which the descriptors read (and a vitest holds them
// to). This test fails when the copy and the catalog differ; regenerate with
//
//	go test ./internal/extensions/dashboards -run TestCoreWidgetsJSON -update
var updateCoreWidgets = flag.Bool("update", false, "rewrite web/src/core/dashboard/core-widgets.json from the core catalog")

var coreWidgetsJSONPath = filepath.Join("..", "..", "..", "web", "src", "core", "dashboard", "core-widgets.json")

// coreWidgetsJSON renders the client copy: one line per type in §5.8 order, the
// legacy aliases and the metric ids the server accepts (metricSchema's enum), so
// a diff names the type or the metric that moved.
func coreWidgetsJSON() []byte {
	var b bytes.Buffer
	b.WriteString("{\n")
	b.WriteString(`  "$comment": "Generated from internal/extensions/dashboards/catalog.go. Do not edit: go test ./internal/extensions/dashboards -run TestCoreWidgetsJSON -update",` + "\n")
	b.WriteString(`  "types": [` + "\n")
	types := CoreTypes()
	for i, t := range types {
		line, _ := json.Marshal(struct {
			ID       string    `json:"id"`
			Category string    `json:"category"`
			Size     SizeRange `json:"size"`
		}{t.ID, t.Category, t.Size})
		b.WriteString("    ")
		b.Write(line)
		if i < len(types)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("  ],\n")
	aliases, _ := json.Marshal(legacyAliases) // map keys are sorted
	b.WriteString(`  "aliases": `)
	b.Write(aliases)
	b.WriteString(",\n")
	metrics, _ := json.Marshal(metricIDs)
	b.WriteString(`  "metrics": `)
	b.Write(metrics)
	b.WriteString("\n}\n")
	return b.Bytes()
}

func TestCoreWidgetsJSON(t *testing.T) {
	want := coreWidgetsJSON()
	if *updateCoreWidgets {
		if err := os.WriteFile(coreWidgetsJSONPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(coreWidgetsJSONPath)
	if err != nil {
		t.Fatalf("read the client copy: %v", err)
	}
	if bytes.Equal(got, want) {
		return
	}
	var gotDoc, wantDoc struct {
		Types   []json.RawMessage `json:"types"`
		Aliases map[string]string `json:"aliases"`
		Metrics []string          `json:"metrics"`
	}
	_ = json.Unmarshal(got, &gotDoc)
	_ = json.Unmarshal(want, &wantDoc)
	byID := func(rows []json.RawMessage) map[string]string {
		m := map[string]string{}
		for _, r := range rows {
			var x struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(r, &x)
			m[x.ID] = string(r)
		}
		return m
	}
	g, w := byID(gotDoc.Types), byID(wantDoc.Types)
	ids := map[string]bool{}
	for id := range g {
		ids[id] = true
	}
	for id := range w {
		ids[id] = true
	}
	var diffs []string
	for id := range ids {
		if g[id] != w[id] {
			diffs = append(diffs, fmt.Sprintf("  %s\n    web:     %s\n    catalog: %s", id, orNone(g[id]), orNone(w[id])))
		}
	}
	sort.Strings(diffs)
	if fmt.Sprint(gotDoc.Aliases) != fmt.Sprint(wantDoc.Aliases) {
		diffs = append(diffs, fmt.Sprintf("  aliases\n    web:     %v\n    catalog: %v", gotDoc.Aliases, wantDoc.Aliases))
	}
	if fmt.Sprint(gotDoc.Metrics) != fmt.Sprint(wantDoc.Metrics) {
		diffs = append(diffs, fmt.Sprintf("  metrics\n    web:     %v\n    catalog: %v", gotDoc.Metrics, wantDoc.Metrics))
	}
	t.Fatalf("%s differs from the core catalog (catalog.go); regenerate it with "+
		"`go test ./internal/extensions/dashboards -run TestCoreWidgetsJSON -update`:\n%s",
		filepath.ToSlash(coreWidgetsJSONPath), strings.Join(diffs, "\n"))
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// prd58 is the «Р» column of PRD §5.8 and the groups of §5.7, written out once
// more so a change of catalog.go that leaves the PRD is a red test, not a
// silent new size on every client.
var prd58 = []struct {
	id, category string
	size         SizeRange
}{
	{"my-tasks", "tasks", size(6, 4, 3, 2, 12, 8)},
	{"triage-queue", "tasks", size(6, 4, 3, 2, 12, 10)},
	{"task-list", "tasks", size(6, 4, 3, 2, 12, 10)},
	{"mentions", "communication", size(6, 4, 3, 2, 12, 10)},
	{"my-comments", "communication", size(6, 4, 3, 2, 12, 10)},
	{"recent-comments", "communication", size(6, 4, 3, 2, 12, 10)},
	{"agents-active", "agents", size(6, 4, 3, 2, 12, 8)},
	{"agents-status", "agents", size(12, 6, 4, 3, 12, 12)},
	{"agents-summary", "agents", size(4, 2, 3, 1, 6, 2)},
	{"cost-kpis", "agents", size(12, 2, 6, 2, 12, 3)},
	{"cost-by-agent", "agents", size(6, 4, 3, 3, 12, 8)},
	{"cost-top-tasks", "agents", size(6, 4, 3, 3, 12, 8)},
	{"cost-by-project", "agents", size(6, 4, 3, 3, 12, 8)},
	{"cost-by-day", "agents", size(6, 4, 4, 3, 12, 8)},
	{"agent-leaderboard", "agents", size(6, 4, 3, 3, 12, 8)},
	// Statistics plan §4 extends the original §5.8 catalog.
	{"agent-usage", "agents", size(12, 6, 6, 4, 12, 12)},
	{"metric", "analytics", size(3, 2, 2, 2, 6, 3)},
	{"tasks-breakdown", "analytics", size(6, 4, 4, 3, 12, 8)},
	{"tasks-timeline", "analytics", size(12, 4, 6, 3, 12, 8)},
	{"events-breakdown", "analytics", size(6, 4, 4, 3, 12, 8)},
	{"memory-usage", "analytics", size(12, 10, 6, 4, 12, 12)},
	{"event-feed", "events", size(12, 6, 6, 4, 12, 12)},
	{"initiatives", "initiatives", size(12, 4, 4, 3, 12, 10)},
	{"initiative", "initiatives", size(6, 3, 4, 2, 12, 6)},
}

func TestCoreTypes_MatchPRD58(t *testing.T) {
	types := CoreTypes()
	if len(types) != len(prd58) {
		t.Fatalf("core catalog has %d types, §5.8 has %d", len(types), len(prd58))
	}
	for i, p := range prd58 {
		c := types[i]
		if c.ID != p.id || c.Category != p.category || c.Size != p.size {
			t.Errorf("§5.8 row %d: want %s %s %+v, catalog has %s %s %+v", i+1, p.id, p.category, p.size, c.ID, c.Category, c.Size)
		}
	}
}
