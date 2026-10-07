package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func jobFixture(id int, name, status string) pipelineJob {
	j := pipelineJob{ID: id, Name: name, Status: status}
	j.Pipeline.ID = 93
	return j
}

func writeJobsPage(w http.ResponseWriter, page, total int, jobs []pipelineJob) {
	pages := max(1, (total+99)/100)
	w.Header().Set("X-Page", strconv.Itoa(page))
	w.Header().Set("X-Per-Page", "100")
	w.Header().Set("X-Total", strconv.Itoa(total))
	w.Header().Set("X-Total-Pages", strconv.Itoa(pages))
	w.Header().Set("X-Next-Page", "")
	if page < pages {
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
	}
	_ = json.NewEncoder(w).Encode(jobs)
}

func TestPipelineRequiredJobsNewestAttemptAndExactIdentity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		jobs       []pipelineJob
		ready, bad bool
	}{
		{"mixed aggregate manual", []pipelineJob{jobFixture(5, "unrelated", "manual"), jobFixture(4, "unrelated-failed", "failed"), jobFixture(3, "verify", "success"), jobFixture(2, "build", "success")}, true, false},
		{"missing", []pipelineJob{jobFixture(2, "build", "success")}, false, false},
		{"skipped", []pipelineJob{jobFixture(3, "verify", "skipped"), jobFixture(2, "build", "success")}, false, false},
		{"failed", []pipelineJob{jobFixture(3, "verify", "failed"), jobFixture(2, "build", "success")}, false, false},
		{"running retry supersedes old success", []pipelineJob{jobFixture(4, "build", "running"), jobFixture(3, "verify", "success"), jobFixture(2, "build", "success")}, false, false},
		{"successful retry supersedes old failed", []pipelineJob{jobFixture(4, "build", "success"), jobFixture(3, "verify", "success"), jobFixture(2, "build", "failed")}, true, false},
		{"exact name", []pipelineJob{jobFixture(3, "verify", "success"), jobFixture(2, "build-other", "success")}, false, false},
		{"unsorted", []pipelineJob{jobFixture(2, "build", "success"), jobFixture(3, "verify", "success")}, false, true},
		{"duplicate identity", []pipelineJob{jobFixture(3, "build", "success"), jobFixture(3, "verify", "success")}, false, true},
		{"missing identity", []pipelineJob{{Name: "build", Status: "success"}}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, "/api/v4/projects/group%2Fsubgroup%2Fproject/pipelines/93/jobs", r.URL.EscapedPath())
				require.Equal(t, "true", r.URL.Query().Get("include_retried"))
				require.Equal(t, "100", r.URL.Query().Get("per_page"))
				require.Empty(t, r.URL.Query().Get("scope"))
				require.Equal(t, "fixture-token", r.Header.Get("PRIVATE-TOKEN"))
				writeJobsPage(w, 1, len(tc.jobs), tc.jobs)
			}))
			defer srv.Close()
			ready, err := NewClient(srv.URL, "fixture-token").PipelineRequiredJobsSucceeded(context.Background(), "group/subgroup/project", 93, []string{"build", "verify"})
			if tc.bad {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, 2, calls)
			}
			require.Equal(t, tc.ready, ready)
		})
	}
}

func TestPipelineRequiredJobsRequiresEveryPageAndStableHead(t *testing.T) {
	for _, variant := range []string{"complete", "page-two-error", "page-two-truncated", "page-two-wrong-pipeline", "page-two-duplicate", "new-retry-at-head", "changed-total", "changed-head-status", "changed-later-status", "new-retry-during-recheck", "recheck-page-two-error"} {
		t.Run(variant, func(t *testing.T) {
			first := make([]pipelineJob, 100)
			for i := range first {
				first[i] = jobFixture(200-i, fmt.Sprintf("unrelated-%d", i), "manual")
			}
			first[1].Name, first[1].Status = "build", "success"
			last := []pipelineJob{jobFixture(100, "verify", "success")}
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				if page == 2 {
					switch variant {
					case "page-two-error":
						w.WriteHeader(503)
						return
					case "page-two-truncated":
						writeJobsPage(w, 2, 101, []pipelineJob{})
						return
					case "page-two-wrong-pipeline":
						last[0].Pipeline.ID = 94
					case "page-two-duplicate":
						last[0].ID = 101
					case "changed-later-status":
						if calls > 2 {
							last[0].Status = "failed"
						}
					case "recheck-page-two-error":
						if calls > 2 {
							w.WriteHeader(503)
							return
						}
					}
					writeJobsPage(w, 2, 101, last)
					return
				}
				total := 101
				if calls > 1 {
					switch variant {
					case "new-retry-at-head":
						first[0] = jobFixture(201, "build", "running")
					case "changed-total":
						total = 102
					case "changed-head-status":
						first[1].Status = "running"
					case "new-retry-during-recheck":
						if calls > 4 {
							first[0] = jobFixture(201, "build", "running")
						}
					}
				}
				writeJobsPage(w, 1, total, first)
			}))
			defer srv.Close()
			ready, err := NewClient(srv.URL, "").PipelineRequiredJobsSucceeded(context.Background(), "group/project", 93, []string{"build", "verify"})
			if variant == "complete" {
				require.NoError(t, err)
				require.True(t, ready)
				require.Equal(t, 5, calls)
			} else {
				require.Error(t, err)
				require.False(t, ready)
			}
		})
	}
}

func TestPipelineJobsRejectsIncompleteMetadataAndProviderFailures(t *testing.T) {
	for _, variant := range []string{"missing-next", "missing-total", "wrong-page", "wrong-per-page", "wrong-pages", "skipped-next", "over-bound", "wrong-count", "null", "invalid-json", "trailing-json", "provider-error", "redirect"} {
		t.Run(variant, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Page", "1")
				w.Header().Set("X-Per-Page", "100")
				w.Header().Set("X-Total", "1")
				w.Header().Set("X-Total-Pages", "1")
				w.Header().Set("X-Next-Page", "")
				switch variant {
				case "missing-next":
					w.Header().Del("X-Next-Page")
				case "missing-total":
					w.Header().Del("X-Total")
				case "wrong-page":
					w.Header().Set("X-Page", "2")
				case "wrong-per-page":
					w.Header().Set("X-Per-Page", "20")
				case "wrong-pages":
					w.Header().Set("X-Total-Pages", "2")
				case "skipped-next":
					w.Header().Set("X-Next-Page", "3")
				case "over-bound":
					w.Header().Set("X-Total", "10001")
				case "wrong-count":
					_, _ = w.Write([]byte(`[]`))
					return
				case "null":
					_, _ = w.Write([]byte(`null`))
					return
				case "invalid-json":
					_, _ = w.Write([]byte(`oops`))
					return
				case "provider-error":
					w.WriteHeader(500)
					return
				case "redirect":
					w.Header().Set("Location", "/leak")
					w.WriteHeader(302)
					return
				}
				_ = json.NewEncoder(w).Encode([]pipelineJob{jobFixture(1, "build", "success")})
				if variant == "trailing-json" {
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			defer srv.Close()
			ready, err := NewClient(srv.URL, "").PipelineRequiredJobsSucceeded(context.Background(), "group/project", 93, []string{"build"})
			require.Error(t, err)
			require.False(t, ready)
		})
	}
}

func TestPipelineJobsRejectsInvalidConditionAndTransport(t *testing.T) {
	for _, required := range [][]string{nil, {}, {""}, {" "}, {"build", "build"}, make([]string, 21)} {
		ready, err := NewClient("\n", "").PipelineRequiredJobsSucceeded(context.Background(), "group/project", 93, required)
		require.Error(t, err)
		require.False(t, ready)
	}
	_, err := NewClient("\n", "").PipelineRequiredJobsSucceeded(context.Background(), "group/project", 93, []string{"build"})
	require.Error(t, err)
	_, err = NewClient("https://example.invalid", "").PipelineRequiredJobsSucceeded(context.Background(), "group/project", 0, []string{"build"})
	require.Error(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	_, err = NewClient(srv.URL, "").PipelineRequiredJobsSucceeded(context.Background(), "group/project", 93, []string{"build"})
	require.Error(t, err)
}
