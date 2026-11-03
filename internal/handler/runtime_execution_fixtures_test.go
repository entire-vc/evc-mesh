package handler

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

const executionFixtureDir = "../../docs/api/fixtures/agent-executions"

// The shared fixture set the UI and the Runner publisher consume (contract §7).
var executionFixtureNames = []string{
	"managed-current", "managed-no-usage", "managed-model-unknown", "two-attempts-two-models",
	"policy-revision-mismatch", "resume-model-reset", "controller-restart", "late-old-publisher",
	"unmanaged", "disabled", "forbidden-foreign", "rejected-secret-field",
}

type executionFixture struct {
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`
	Now      time.Time       `json:"now"`
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
	Expect   struct {
		Status     int    `json:"status"`
		EnforcedBy string `json:"enforced_by"`
		JQ         string `json:"jq"`
	} `json:"expect"`
}

func loadExecutionFixture(t *testing.T, name string) executionFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(executionFixtureDir, name+".json"))
	require.NoError(t, err, "fixture %s must exist", name)
	var f executionFixture
	require.NoError(t, json.Unmarshal(data, &f))
	require.Equal(t, name, f.Name)
	return f
}

func TestAgentExecutionFixturesAreAWholeSet(t *testing.T) {
	entries, err := os.ReadDir(executionFixtureDir)
	require.NoError(t, err)
	var onDisk []string
	for _, e := range entries {
		onDisk = append(onDisk, e.Name()[:len(e.Name())-len(".json")])
	}
	slices.Sort(onDisk)
	want := slices.Clone(executionFixtureNames)
	slices.Sort(want)
	require.Equal(t, want, onDisk)
}

func TestAgentExecutionFixtureReadResponses(t *testing.T) {
	freshness := []string{"unknown", "current", "stale", "offline", "history"}
	for _, name := range executionFixtureNames {
		f := loadExecutionFixture(t, name)
		if f.Kind != "read" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			// Strict decode: a field the wire schema does not know fails.
			dec := json.NewDecoder(bytes.NewReader(f.Response))
			dec.DisallowUnknownFields()
			var list postgres.ExecutionList
			require.NoError(t, dec.Decode(&list))
			require.Equal(t, domain.ExecutionSchemaVersion, list.SchemaVersion)
			require.Equal(t, domain.ExecutionFreshnessSeconds, list.MaxAgeSeconds)
			var raw map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(f.Response, &raw))
			require.Contains(t, raw, "next_cursor", "next_cursor is required, null when there is no next page")
			require.NotEmpty(t, list.Items)
			for _, item := range list.Items {
				require.Contains(t, freshness, item.Freshness)
				require.Equal(t, "agent_profile", item.Profile.Source)
				if !item.Managed {
					require.NotNil(t, item.Reason)
					require.Nil(t, item.Configured)
				}
				for _, a := range item.Attempts {
					require.Contains(t, freshness, a.Freshness)
					require.NotEqual(t, "agent_profile", a.Observed.Source, "an observation never comes from the profile")
					require.False(t, a.Observed.ObservedAt.After(f.Now), "no future observation")
					// A model is only ever shown with the source and time it was observed at.
					if a.Observed.Model != nil {
						require.NotEmpty(t, a.Observed.Source)
					}
					if a.Freshness == "current" {
						require.NotNil(t, a.Observed.Model, "current needs a known model")
						require.True(t, item.Enabled)
					}
					require.Equal(t, a.Stale, a.Freshness == "stale" || a.Freshness == "offline")
				}
			}
			if f.Expect.JQ != "" {
				if _, err := exec.LookPath("jq"); err != nil {
					t.Skip("jq not installed; the Go assertions above still ran")
				}
				cmd := exec.Command("jq", "-e", f.Expect.JQ)
				cmd.Stdin = bytes.NewReader(f.Response)
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, string(out))
			}
		})
	}
}

func TestExecutionReportFixtureRequests(t *testing.T) {
	for _, name := range executionFixtureNames {
		f := loadExecutionFixture(t, name)
		if f.Kind != "report" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			var report domain.ExecutionReport
			decodeErr := domain.DecodeExecutionJSON(f.Request, &report)
			validateErr := decodeErr
			if decodeErr == nil {
				validateErr = report.Validate(f.Now, "controller-a")
			}
			if f.Expect.Status == 200 || f.Expect.EnforcedBy == "db" {
				require.NoError(t, validateErr, "the request is well-formed; %s is refused by the %s", name, f.Expect.EnforcedBy)
				return
			}
			require.ErrorIs(t, validateErr, domain.ErrRuntimeInput)
			// Positive control: without the offending field the same request is valid.
			var generic map[string]any
			require.NoError(t, json.Unmarshal(f.Request, &generic))
			for _, a := range generic["attempts"].([]any) {
				delete(a.(map[string]any), "env")
			}
			clean, err := json.Marshal(generic)
			require.NoError(t, err)
			var ok domain.ExecutionReport
			require.NoError(t, domain.DecodeExecutionJSON(clean, &ok))
			require.NoError(t, ok.Validate(f.Now, "controller-a"))
		})
	}
}
