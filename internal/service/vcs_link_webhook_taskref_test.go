package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// These tests are the acceptance criteria of task 695d7ddf: the webhook was
// live and answering 200 to 444 deliveries a week while creating nothing,
// because it recognised only MESH-<uuid> — a spelling no agent writes. Each
// case here is a form that appears in real pull requests.
//
// The assertion is on the stored row, not on the returned Reason: the point of
// the join is that vcs_links has a row in it.

// fixedTaskID returns a deterministic UUID whose first eight hex chars contain
// a letter, so every short-id spelling under test is actually exercised.
func fixedTaskID(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("a%07x-b856-4a2b-9c31-1f0e7d5a4b21", 0x2377a26+n))
}

// openEvent is a pull_request "opened" delivery — the common case, and the one
// that must populate the join long before anything merges.
func openEvent(prNum int, title, body, branch string) GitHubWebhookEvent {
	return GitHubWebhookEvent{
		Action:     "opened",
		PRNumber:   prNum,
		PRTitle:    title,
		PRBody:     body,
		PRBranch:   branch,
		PRHTMLURL:  "https://github.com/entire-vc/evc-mesh/pull/" + uintToString(prNum),
		PRState:    "open",
		Repository: "entire-vc/evc-mesh",
	}
}

// makeTaskWithID seeds a task at a chosen UUID. The short-id cases must not
// depend on uuid.New(): roughly one UUID in forty begins with eight decimal
// digits, and a bare "#<8 digits>" is deliberately not read as a reference —
// a random id would make those tests fail a few percent of the time for a
// reason that has nothing to do with what they assert.
func (h *harness) makeTaskWithID(t *testing.T, id uuid.UUID, cat domain.StatusCategory) *domain.Task {
	t.Helper()
	statusID, ok := h.statusIDs[cat]
	require.True(t, ok, "unknown status category %s", cat)
	tk := &domain.Task{ID: id, ProjectID: h.projectID, StatusID: statusID}
	h.taskRepo.tasks[id] = tk
	return tk
}

// linksFor returns every stored link pointing at taskID.
func (h *harness) linksFor(taskID uuid.UUID) []domain.VCSLink {
	links, err := h.repo.ListByTask(context.Background(), taskID)
	if err != nil {
		panic(err)
	}
	return links
}

func (h *harness) linkCount() int {
	h.repo.mu.Lock()
	defer h.repo.mu.Unlock()
	return len(h.repo.links)
}

func TestHandlePR_RecognisesFormsAgentsActuallyWrite(t *testing.T) {
	tests := []struct {
		name   string
		title  func(task *domain.Task) string
		body   func(task *domain.Task) string
		branch func(task *domain.Task) string
	}{
		{
			name:  "MESH-<uuid> still works (back-compat)",
			title: func(tk *domain.Task) string { return "MESH-" + tk.ID.String() + " fix the gate" },
		},
		{
			name:  "Refs #<8hex> in the body — the form in the PR behind #55dc6e19",
			title: func(*domain.Task) string { return "feat(gate): hold money PRs for sign-off" },
			body:  func(tk *domain.Task) string { return "Refs #" + tk.ID.String()[:8] + "\n\nDetails below." },
		},
		{
			name:  "Refs #<8hex> in the title",
			title: func(tk *domain.Task) string { return "fix: merge train (Refs #" + tk.ID.String()[:8] + ")" },
		},
		{
			name:  "full task URL in the body",
			title: func(*domain.Task) string { return "chore: bump deps" },
			body:  func(tk *domain.Task) string { return "See https://mesh.entire.host/t/" + tk.ID.String() },
		},
		{
			name:  "short task URL in the body",
			title: func(*domain.Task) string { return "chore: bump deps" },
			body:  func(tk *domain.Task) string { return "context: https://mesh.entire.host/t/" + tk.ID.String()[:8] },
		},
		{
			name:  "keyword + bare uuid",
			title: func(*domain.Task) string { return "refactor: extract taskref" },
			body:  func(tk *domain.Task) string { return "Closes: " + tk.ID.String() },
		},
		{
			name:   "id only in the head branch",
			title:  func(*domain.Task) string { return "feat: cost tracking dashboard" },
			body:   func(*domain.Task) string { return "No task reference in the prose at all." },
			branch: func(tk *domain.Task) string { return "linus/" + tk.ID.String()[:8] + "-cost-tracking" },
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			task := h.makeTaskWithID(t, fixedTaskID(i), domain.StatusCategoryInProgress)

			get := func(f func(*domain.Task) string) string {
				if f == nil {
					return ""
				}
				return f(task)
			}
			prNum := 500 + i
			ev := openEvent(prNum, get(tc.title), get(tc.body), get(tc.branch))

			res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
			require.NoError(t, err)
			require.Equal(t, task.ID, res.TaskID, "reference must resolve to the task")

			links := h.linksFor(task.ID)
			require.Len(t, links, 1, "exactly one row must land in vcs_links")
			assert.Equal(t, domain.VCSLinkTypePR, links[0].LinkType, "link_type must be the canonical 'pr'")
			assert.Equal(t, domain.VCSProviderGitHub, links[0].Provider)
			assert.Equal(t, uintToString(prNum), links[0].ExternalID)
			assert.Equal(t, ev.PRHTMLURL, links[0].URL)

			// An "opened" delivery syncs the link and stops — no transition.
			assert.False(t, res.Transitioned)
			assert.Equal(t, "not_closed", res.Reason)
		})
	}
}

// The negative arm of the acceptance criteria. It has to be a payload that
// looks busy — a body full of hex that is not a task id — or it proves only
// that empty text yields nothing.
func TestHandlePR_NoReference_CreatesNothing(t *testing.T) {
	h := newHarness(t)
	task := h.makeTaskWithID(t, fixedTaskID(0), domain.StatusCategoryInProgress)
	before := h.linkCount()

	ev := openEvent(601,
		"chore: bump golangci-lint to 2.11.3",
		"Reverts commit "+task.ID.String()[:8]+"b856c04d1f9e3a7b5c2d8e6f0a1b3c4d.\n"+
			"Closes #114. See https://github.com/entire-vc/evc-mesh/commit/"+task.ID.String()[:8]+".",
		"garfield/bump-linter")

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, "no_task_ref", res.Reason)
	assert.Equal(t, uuid.Nil, res.TaskID)
	assert.Equal(t, before, h.linkCount(), "no row may be created without a resolvable reference")
	assert.Empty(t, h.linksFor(task.ID))
}

// A reference that parses but names nothing must be treated as no reference.
// vcs_links.task_id carries an FK to tasks(id), so an unverified id becomes a
// constraint violation at insert instead of a clean miss.
func TestHandlePR_ReferenceToUnknownTask_CreatesNothing(t *testing.T) {
	h := newHarness(t)
	ghost := uuid.New()
	before := h.linkCount()

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(),
		openEvent(602, "MESH-"+ghost.String(), "", ""))
	require.NoError(t, err)
	assert.Equal(t, "no_task_ref", res.Reason)
	assert.Equal(t, before, h.linkCount())
}

// Two tenants behind one 8-hex prefix must not silently resolve to whichever
// row sorted first. GetByShortID refuses; the resolver must honour the refusal
// rather than falling back to a guess.
func TestHandlePR_AmbiguousShortPrefix_DoesNotResolve(t *testing.T) {
	h := newHarness(t)

	// Force a collision by seeding two tasks that share the first 8 hex chars.
	shared := "abcdef12"
	a := &domain.Task{ID: uuid.MustParse(shared + "-1111-4111-8111-111111111111"),
		ProjectID: h.projectID, StatusID: h.statusIDs[domain.StatusCategoryInProgress]}
	b := &domain.Task{ID: uuid.MustParse(shared + "-2222-4222-8222-222222222222"),
		ProjectID: h.projectID, StatusID: h.statusIDs[domain.StatusCategoryInProgress]}
	h.taskRepo.tasks[a.ID] = a
	h.taskRepo.tasks[b.ID] = b
	before := h.linkCount()

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(),
		openEvent(603, "fix: something", "Refs #"+shared, ""))
	require.NoError(t, err)
	assert.Equal(t, "no_task_ref", res.Reason, "an ambiguous prefix must not resolve to either task")
	assert.Equal(t, before, h.linkCount())

	// And the unambiguous full uuid for the same pair still resolves.
	res, err = h.svc.HandleGitHubPullRequestEvent(context.Background(),
		openEvent(604, "fix: something", "Closes: "+a.ID.String(), ""))
	require.NoError(t, err)
	assert.Equal(t, a.ID, res.TaskID)
}

// The whole feature exists so that a merged PR carrying a real-world reference
// drives the task transition end to end — not just so a row appears.
func TestHandlePR_ShortRefDrivesTransitionOnMerge(t *testing.T) {
	h := newHarness(t)
	task := h.makeTaskWithID(t, fixedTaskID(1), domain.StatusCategoryInProgress)

	ev := mergedClosedEvent(605, task.ID, "feat: dense rows")
	ev.PRBody = "Refs #" + task.ID.String()[:8]

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.True(t, res.Transitioned, "a short-id reference must drive the same transition MESH- does")
	assert.Equal(t, "in_progress", res.OldStatus)
	assert.Equal(t, "review", res.NewStatus)
	assert.Equal(t, h.statusIDs[domain.StatusCategoryReview], h.taskRepo.tasks[task.ID].StatusID)

	links := h.linksFor(task.ID)
	require.Len(t, links, 1)
	assert.Equal(t, domain.VCSLinkStatusMerged, links[0].Status)
}

// Title outranks body: a deliberate reference must not lose to an incidental
// one buried in a long description.
func TestHandlePR_TitleReferenceOutranksBody(t *testing.T) {
	h := newHarness(t)
	wanted := h.makeTaskWithID(t, fixedTaskID(2), domain.StatusCategoryInProgress)
	other := h.makeTaskWithID(t, fixedTaskID(3), domain.StatusCategoryInProgress)

	ev := openEvent(606,
		"MESH-"+wanted.ID.String()+" the real one",
		"Follow-up to https://mesh.entire.host/t/"+other.ID.String(),
		"")

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, wanted.ID, res.TaskID)
	assert.Empty(t, h.linksFor(other.ID))
}

// The push (commit) path shares the resolver, so a commit message saying
// "Refs #<short>" links too.
func TestResolveTaskRef_UsedByPushPath(t *testing.T) {
	h := newHarness(t)
	task := h.makeTaskWithID(t, fixedTaskID(4), domain.StatusCategoryInProgress)

	id, ref := h.svc.ResolveTaskRef(context.Background(),
		TaskRefSource{Name: "body", Text: "fix(gate): stop the train\n\nRefs #" + task.ID.String()[:8]},
		TaskRefSource{Name: "branch", Text: "garfield/webhook-taskref-formats"},
	)
	assert.Equal(t, task.ID, id)
	assert.Equal(t, RefKindShortID, ref.Kind)

	// And a commit that names nothing resolves to nothing.
	id, _ = h.svc.ResolveTaskRef(context.Background(),
		TaskRefSource{Name: "body", Text: "chore: gofmt"},
		TaskRefSource{Name: "branch", Text: "garfield/gofmt"},
	)
	assert.Equal(t, uuid.Nil, id)
}

// Guard against the resolver being handed a prefix the repository would reject
// as too short to be selective.
func TestExtractTaskRefs_RejectsPrefixShorterThanSix(t *testing.T) {
	assert.Empty(t, ExtractTaskRefs(TaskRefSource{Name: "body", Text: "Refs #abc1"}))
	assert.Empty(t, ExtractTaskRefs(TaskRefSource{Name: "body",
		Text: "https://mesh.entire.host/t/" + strings.Repeat("a", 5)}))
}
