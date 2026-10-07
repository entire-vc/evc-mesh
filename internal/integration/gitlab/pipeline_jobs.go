package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const pipelineJobsPerPage = 100
const pipelineJobsMaxPages = 100

type pipelineJob struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Pipeline struct {
		ID int `json:"id"`
	} `json:"pipeline"`
}

// PipelineRequiredJobsSucceeded verifies a complete, bounded provider snapshot.
// include_retried=true and descending IDs let us select the newest attempt by
// name. Never stop when the required names first appear: a later page or a new
// retry can invalidate that apparent success. No aggregate status is consulted.
func (c *Client) PipelineRequiredJobsSucceeded(ctx context.Context, projectPath string, id int, required []string) (bool, error) {
	if id <= 0 || len(required) == 0 || len(required) > 20 {
		return false, fmt.Errorf("gitlab jobs: invalid required condition")
	}
	seenNames := make(map[string]bool)
	for _, name := range required {
		if strings.TrimSpace(name) == "" || seenNames[name] {
			return false, fmt.Errorf("gitlab jobs: invalid required name")
		}
		seenNames[name] = true
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	latest := make(map[string]pipelineJob)
	var snapshot [][]pipelineJob
	lastID, expectedTotal := 0, -1
	for page := 1; ; page++ {
		jobs, total, err := c.pipelineJobsPage(ctx, projectPath, id, page)
		if err != nil {
			return false, err
		}
		if expectedTotal >= 0 && total != expectedTotal {
			return false, fmt.Errorf("gitlab jobs: pagination changed")
		}
		expectedTotal = total
		snapshot = append(snapshot, jobs)
		for _, job := range jobs {
			if job.ID <= 0 || job.Pipeline.ID != id || job.Name == "" || job.Status == "" || (lastID != 0 && job.ID >= lastID) {
				return false, fmt.Errorf("gitlab jobs: identity/order mismatch")
			}
			lastID = job.ID
			if _, exists := latest[job.Name]; !exists {
				latest[job.Name] = job
			}
		}
		if page*pipelineJobsPerPage >= total {
			break
		}
	}
	// Re-read every page, including statuses on later pages. An unchanged head
	// alone does not establish that the complete observed snapshot is stable.
	for index, original := range snapshot {
		jobs, total, err := c.pipelineJobsPage(ctx, projectPath, id, index+1)
		if err != nil {
			return false, err
		}
		if total != expectedTotal || !slices.Equal(original, jobs) {
			return false, fmt.Errorf("gitlab jobs: snapshot changed during verification")
		}
	}
	// Catch retries added while the later pages of that second traversal were
	// being read. A single-page snapshot was already rechecked above.
	if len(snapshot) > 1 {
		head, total, err := c.pipelineJobsPage(ctx, projectPath, id, 1)
		if err != nil {
			return false, err
		}
		if total != expectedTotal || !slices.Equal(snapshot[0], head) {
			return false, fmt.Errorf("gitlab jobs: snapshot changed during verification")
		}
	}
	for _, name := range required {
		if job, ok := latest[name]; !ok || job.Status != "success" {
			return false, nil
		}
	}
	return true, nil
}

func (c *Client) pipelineJobsPage(ctx context.Context, projectPath string, id, page int) ([]pipelineJob, int, error) {
	endpoint := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/jobs?include_retried=true&per_page=%d&page=%d", c.baseURL, url.PathEscape(projectPath), id, pipelineJobsPerPage, page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	}
	client := *c.httpClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return fmt.Errorf("gitlab jobs: redirect refused") }
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("gitlab jobs: unexpected status %d", resp.StatusCode)
	}
	// Explicit counts and page headers prove that we consumed every page. Missing,
	// inconsistent or over-bound metadata fails closed, even if required jobs were
	// already found. The operator-configured self-hosted GitLab supplies these.
	total, totalErr := strconv.Atoi(resp.Header.Get("X-Total"))
	pages, pagesErr := strconv.Atoi(resp.Header.Get("X-Total-Pages"))
	expectedPages := max(1, (total+pipelineJobsPerPage-1)/pipelineJobsPerPage)
	next, hasNext := resp.Header[http.CanonicalHeaderKey("X-Next-Page")]
	expectedNext := ""
	if page < pages {
		expectedNext = strconv.Itoa(page + 1)
	}
	if totalErr != nil || pagesErr != nil || total < 0 || total > pipelineJobsPerPage*pipelineJobsMaxPages || pages != expectedPages || page > pages ||
		resp.Header.Get("X-Page") != strconv.Itoa(page) || resp.Header.Get("X-Per-Page") != strconv.Itoa(pipelineJobsPerPage) || !hasNext || len(next) != 1 || next[0] != expectedNext {
		return nil, 0, fmt.Errorf("gitlab jobs: incomplete pagination metadata")
	}
	reader := &io.LimitedReader{R: resp.Body, N: 4 * 1024 * 1024}
	decoder := json.NewDecoder(reader)
	var jobs []pipelineJob
	if err := decoder.Decode(&jobs); err != nil {
		return nil, 0, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || reader.N == 0 {
		return nil, 0, fmt.Errorf("gitlab jobs: invalid or oversized response")
	}
	expectedCount := min(pipelineJobsPerPage, total-(page-1)*pipelineJobsPerPage)
	if jobs == nil || len(jobs) != expectedCount {
		return nil, 0, fmt.Errorf("gitlab jobs: incomplete page")
	}
	return jobs, total, nil
}
