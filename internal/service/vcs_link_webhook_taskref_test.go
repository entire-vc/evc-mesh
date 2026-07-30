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

// ---------------------------------------------------------------------------
// Ambiguity. Added after the first pull request this code met in production
// linked itself to the wrong task: the PR documented the reference syntax, and
// the task used as the example in its body was real.
// ---------------------------------------------------------------------------

// The literal incident, reduced. A body that explains the syntax names several
// real tasks; picking the first by position is a guess, and the guess was wrong.
func TestHandlePR_BodyNamingSeveralTasks_LinksNone(t *testing.T) {
	h := newHarness(t)
	documented := h.makeTaskWithID(t, fixedTaskID(10), domain.StatusCategoryInProgress)
	subject := h.makeTaskWithID(t, fixedTaskID(11), domain.StatusCategoryInProgress)
	before := h.linkCount()

	ev := openEvent(701,
		"fix(vcs-links): a keyword may widen the alphabet, not the shape",
		"| text | result |\n"+
			"|---|---|\n"+
			"| `Refs #"+documented.ID.String()[:8]+"` | ✅ |\n"+
			"| `#"+documented.ID.String()[:8]+"` | ✅ |\n\n"+
			"Refs #"+subject.ID.String()[:8],
		"garfield/taskref-keyword-sigil")

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	// "ambiguous_task_ref", not "no_task_ref": the two are now distinct on
	// purpose. Something here DID name tasks, and that is what forbids reusing
	// a stored link for this PR — see
	// TestHandlePR_AmbiguousPayload_DoesNotReuseStaleLink.
	assert.Equal(t, "ambiguous_task_ref", res.Reason,
		"a payload naming two real tasks must link neither")
	assert.Equal(t, before, h.linkCount())
	assert.Empty(t, h.linksFor(documented.ID), "the documented example must not be linked")
	assert.Empty(t, h.linksFor(subject.ID))
}

// A title is short and deliberate, so one reference there settles a crowded body.
func TestHandlePR_TitleBreaksBodyAmbiguity(t *testing.T) {
	h := newHarness(t)
	subject := h.makeTaskWithID(t, fixedTaskID(12), domain.StatusCategoryInProgress)
	a := h.makeTaskWithID(t, fixedTaskID(13), domain.StatusCategoryInProgress)
	b := h.makeTaskWithID(t, fixedTaskID(14), domain.StatusCategoryInProgress)

	ev := openEvent(702,
		"docs: reference syntax (Refs #"+subject.ID.String()[:8]+")",
		"Examples: #"+a.ID.String()[:8]+" and #"+b.ID.String()[:8]+".",
		"")

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, subject.ID, res.TaskID)
	require.Len(t, h.linksFor(subject.ID), 1)
	assert.Empty(t, h.linksFor(a.ID))
	assert.Empty(t, h.linksFor(b.ID))
}

// Two spellings of the SAME task are agreement, not ambiguity — otherwise the
// common "title says MESH-<uuid>, body links /t/<uuid>" PR would link nothing.
func TestHandlePR_SameTaskNamedTwice_IsNotAmbiguous(t *testing.T) {
	h := newHarness(t)
	task := h.makeTaskWithID(t, fixedTaskID(15), domain.StatusCategoryInProgress)

	ev := openEvent(703,
		"MESH-"+task.ID.String(),
		"See https://mesh.entire.host/t/"+task.ID.String()+" and #"+task.ID.String()[:8],
		"linus/"+task.ID.String()[:8]+"-slug")

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, task.ID, res.TaskID)
	require.Len(t, h.linksFor(task.ID), 1)
}

// One task named by TWO DIFFERENT spellings, neither in the title. The two
// spellings survive ExtractTaskRefs as separate candidates (a full UUID and a
// short prefix are different tokens), so only the resolver can tell they mean
// the same task. Without de-duplication BY RESOLVED TASK these are two hits,
// the ambiguity rule refuses, and a perfectly clear payload links nothing —
// with no title reference to rescue it.
func TestHandlePR_SameTaskViaTwoSpellings_NoTitleRef_StillResolves(t *testing.T) {
	h := newHarness(t)
	task := h.makeTaskWithID(t, fixedTaskID(17), domain.StatusCategoryInProgress)

	ev := openEvent(705,
		"feat: cost tracking dashboard",
		"Context: https://mesh.entire.host/t/"+task.ID.String()+"\n\nRefs #"+task.ID.String()[:8],
		"")

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, task.ID, res.TaskID, "one task named twice is agreement, not ambiguity")
	require.Len(t, h.linksFor(task.ID), 1)
}

// An id that names nothing is not a competing candidate — a body quoting a
// deleted task beside the real reference must still link the real one.
func TestHandlePR_UnresolvableIDDoesNotCreateAmbiguity(t *testing.T) {
	h := newHarness(t)
	task := h.makeTaskWithID(t, fixedTaskID(16), domain.StatusCategoryInProgress)
	ghost := uuid.New()

	ev := openEvent(704, "fix: something",
		"Supersedes #"+ghost.String()[:8]+" (deleted). Refs #"+task.ID.String()[:8], "")

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, task.ID, res.TaskID)
}

// A PR that was mislinked once must not keep re-confirming that link.
//
// This is the bug the ambiguity refusal did NOT close, found by independent
// verification of 695d7ddf against the already-deployed code. ResolveTaskRef
// correctly declined to choose — and the handler then fell through to
// "reuse whatever is already stored for this PR number", which is the wrong
// task. Upsert keys on (task_id, provider, link_type, external_id), so every
// redelivery rewrote the same wrong row: the link could never self-correct,
// and no log line said anything was amiss.
func TestHandlePR_AmbiguousPayload_DoesNotReuseStaleLink(t *testing.T) {
	h := newHarness(t)
	documented := h.makeTaskWithID(t, fixedTaskID(40), domain.StatusCategoryInProgress)
	subject := h.makeTaskWithID(t, fixedTaskID(41), domain.StatusCategoryInProgress)

	// The wrong row, exactly as it exists in production for PR #433.
	stale := &domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     documented.ID,
		Provider:   domain.VCSProviderGitHub,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: "433",
		URL:        "https://github.com/entire-vc/evc-mesh/pull/433",
		Status:     domain.VCSLinkStatusOpen,
	}
	_, err := h.repo.Upsert(context.Background(), stale)
	require.NoError(t, err)
	before := h.linkCount()

	// The same ambiguous body arrives again — a "synchronize" or "closed"
	// redelivery, which GitHub sends for the life of the pull request.
	ev := openEvent(433,
		"fix(vcs-links): a keyword may widen the alphabet, not the shape",
		"| `Refs #"+documented.ID.String()[:8]+"` | ✅ |\n\nRefs #"+subject.ID.String()[:8],
		"garfield/taskref-keyword-sigil")

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, "ambiguous_task_ref", res.Reason,
		"an ambiguous payload must report the refusal, not silently adopt history")
	assert.Equal(t, uuid.Nil, res.TaskID,
		"the stale link must not be resurrected as this delivery's answer")
	assert.Equal(t, before, h.linkCount(), "no row may be added on a refusal")
}

// The fallback itself is legitimate and must survive: a PR linked by hand names
// no task in its own text, and its later deliveries are the only thing that
// keeps the link's status current. Removing the fallback outright would break
// exactly the manual path add_vcs_link exists for.
func TestHandlePR_NoRefInPayload_StillUsesStoredLink(t *testing.T) {
	h := newHarness(t)
	task := h.makeTaskWithID(t, fixedTaskID(42), domain.StatusCategoryInProgress)

	linked := &domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     task.ID,
		Provider:   domain.VCSProviderGitHub,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: "434",
		URL:        "https://github.com/entire-vc/evc-mesh/pull/434",
		Status:     domain.VCSLinkStatusOpen,
	}
	_, err := h.repo.Upsert(context.Background(), linked)
	require.NoError(t, err)

	ev := openEvent(434, "chore: bump deps", "No task reference anywhere in here.", "chore/deps")

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, task.ID, res.TaskID,
		"a payload naming nothing must still ride the link someone created by hand")
}
