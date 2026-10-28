package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// an internal ticket: a review card is a different card from the work it reviews, and
// no title/body/branch of the MR names it. It is closed by the merge because a
// link with role=review ties it to the MR's URL.

const reviewSubjectURL = "https://gitlab.example.invalid/entire-vc/evc-mesh/-/merge_requests/101"

func (h *harness) bindReview(task *domain.Task, url, meta string) {
	if _, _, err := h.svcCreateLink(task.ID, url, meta); err != nil {
		panic(err)
	}
}

func (h *harness) svcCreateLink(taskID uuid.UUID, url, meta string) (*domain.VCSLink, bool, error) {
	refs := domain.ExtractChangeRequestRefs(url)
	l := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: refs[0].Provider, LinkType: domain.VCSLinkTypePR,
		ExternalID: refs[0].ExternalID, URL: refs[0].URL, Status: domain.VCSLinkStatusOpen, Metadata: []byte(meta),
	}
	return l, true, h.repo.Create(context.Background(), l)
}

func TestHandleMR_Merged_ClosesBoundReviewCard(t *testing.T) {
	for _, cat := range []domain.StatusCategory{domain.StatusCategoryReview, domain.StatusCategoryTodo, domain.StatusCategoryInProgress} {
		t.Run(string(cat), func(t *testing.T) {
			h := newHarness(t)
			work := h.makeTask(t, domain.StatusCategoryInProgress)
			review := h.makeTask(t, cat)
			h.bindReview(review, reviewSubjectURL, `{"role":"review"}`)

			_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mergedMREvent(101, work.ID, "MESH-"+work.ID.String()))
			require.NoError(t, err)

			assert.Equal(t, h.statusIDs[domain.StatusCategoryDone], h.taskRepo.tasks[review.ID].StatusID, "review card must be done")
			links, _ := h.repo.ListByTask(context.Background(), review.ID)
			require.Len(t, links, 1)
			assert.Equal(t, domain.VCSLinkStatusMerged, links[0].Status)
			assert.True(t, links[0].IsReviewSubject(), "role marker must survive the status update")
		})
	}
}

// Known-bad controls: each of these must leave the review card alone.
func TestHandleMR_Merged_LeavesUnboundOrForeignReviewCards(t *testing.T) {
	cases := []struct {
		name string
		bind func(h *harness, review *domain.Task)
	}{
		{"no link at all (the an internal ticket defect)", func(*harness, *domain.Task) {}},
		{"link without the review role", func(h *harness, r *domain.Task) { h.bindReview(r, reviewSubjectURL, `{}`) }},
		{"same iid in another project", func(h *harness, r *domain.Task) {
			h.bindReview(r, "https://gitlab.example.invalid/entire-vc/evc-keep/-/merge_requests/101", `{"role":"review"}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			work := h.makeTask(t, domain.StatusCategoryInProgress)
			review := h.makeTask(t, domain.StatusCategoryReview)
			tc.bind(h, review)

			_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mergedMREvent(101, work.ID, "MESH-"+work.ID.String()))
			require.NoError(t, err)

			assert.Equal(t, h.statusIDs[domain.StatusCategoryReview], h.taskRepo.tasks[review.ID].StatusID)
		})
	}
}

func TestHandleMR_ClosedWithoutMerge_KeepsReviewCardOpen(t *testing.T) {
	h := newHarness(t)
	work := h.makeTask(t, domain.StatusCategoryInProgress)
	review := h.makeTask(t, domain.StatusCategoryReview)
	h.bindReview(review, reviewSubjectURL, `{"role":"review"}`)

	ev := mergedMREvent(101, work.ID, "MESH-"+work.ID.String())
	ev.MRState = "closed"
	_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	require.NoError(t, err)

	assert.Equal(t, h.statusIDs[domain.StatusCategoryReview], h.taskRepo.tasks[review.ID].StatusID)
	links, _ := h.repo.ListByTask(context.Background(), review.ID)
	assert.Equal(t, domain.VCSLinkStatusClosed, links[0].Status)
}

func TestHandlePR_Merged_ClosesBoundReviewCard(t *testing.T) {
	h := newHarness(t)
	work := h.makeTask(t, domain.StatusCategoryInProgress)
	review := h.makeTask(t, domain.StatusCategoryReview)
	h.bindReview(review, "https://github.com/example/repo/pull/7", `{"role":"review"}`)

	_, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), mergedClosedEvent(7, work.ID, "MESH-"+work.ID.String()))
	require.NoError(t, err)

	assert.Equal(t, h.statusIDs[domain.StatusCategoryDone], h.taskRepo.tasks[review.ID].StatusID)
}

func TestExtractChangeRequestRefs(t *testing.T) {
	refs := domain.ExtractChangeRequestRefs("Ревью MR: https://gitlab.example.invalid/entire-vc/evc-keep/-/merge_requests/19, again (https://gitlab.example.invalid/entire-vc/evc-keep/-/merge_requests/19/) and https://github.com/o/r/pull/5.")
	require.Len(t, refs, 2)
	assert.Equal(t, domain.VCSProviderGitLab, refs[0].Provider)
	assert.Equal(t, "19", refs[0].ExternalID)
	assert.Equal(t, domain.VCSProviderGitHub, refs[1].Provider)
	assert.Equal(t, "5", refs[1].ExternalID)
	assert.Empty(t, domain.ExtractChangeRequestRefs("no links, MR !19"))
}

func TestBindReviewSubject(t *testing.T) {
	desc := "Ревью MR Calvin'а: https://gitlab.example.invalid/entire-vc/evc-keep/-/merge_requests/19"
	cases := []struct {
		name   string
		labels []string
		desc   string
		want   int
	}{
		{"review label, one MR", []string{"keep", "review"}, desc, 1},
		{"no review label", []string{"keep"}, desc, 0},
		{"two different MRs is prose, not a subject", []string{"review"}, desc + " и https://gitlab.example.invalid/entire-vc/evc-keep/-/merge_requests/20", 0},
		{"no URL", []string{"review"}, "посмотри MR !19", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := setupTaskService()
			repo := newFakeVCSLinkRepo()
			svc.vcsLinkRepo = repo
			task := &domain.Task{ID: uuid.New(), Title: "Ревью", Description: tc.desc, Labels: pq.StringArray(tc.labels)}

			svc.bindReviewSubject(context.Background(), task)

			links, _ := repo.ListByTask(context.Background(), task.ID)
			require.Len(t, links, tc.want)
			if tc.want == 1 {
				assert.True(t, links[0].IsReviewSubject())
				assert.Equal(t, "19", links[0].ExternalID)
				assert.Equal(t, domain.VCSLinkStatusOpen, links[0].Status)
			}
		})
	}
}

// The verdict is a review card's deliverable, so an unmerged subject must not
// hold the card out of done; the identical link without the marker still does.
func TestMoveTask_DoneGate_ReviewSubjectLinkDoesNotBlock(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata string
		wantErr  bool
	}{
		{"review subject passes", `{"role":"review"}`, false},
		{"relates_to mention passes", `{"relation":"relates_to"}`, false},
		{"plain open MR still blocks", `{}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectID, taskID, statusID := uuid.New(), uuid.New(), uuid.New()
			checker := &fakeGitLabMRChecker{err: fmt.Errorf("dial tcp: connection refused")}
			svc, taskRepo, statusRepo, vcsRepo, commentRepo := setupTaskServiceWithDoneGateAndGitLabChecker(checker)
			taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, StatusID: uuid.New(), Title: "review", VCSLinkCount: 1}
			// A comment is evidence in its own right, so only the link decides.
			commentRepo.items[uuid.New()] = &domain.Comment{TaskID: taskID, Body: "verdict"}
			statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryDone}
			vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
				ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
				Status: domain.VCSLinkStatusOpen, URL: reviewSubjectURL, ExternalID: "101", Metadata: []byte(tc.metadata),
			})

			err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

			if tc.wantErr {
				var doneErr *DoneEvidenceError
				require.ErrorAs(t, err, &doneErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// A relates_to mention is not the task's evidence: a task whose only link is a
// mention needs an artifact or a comment like a task with no links at all.
func TestMoveTask_DoneGate_RelatesToOnlyNeedsEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		comment bool
		wantErr bool
	}{
		{"no comment blocks", false, true},
		{"comment passes", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectID, taskID, statusID := uuid.New(), uuid.New(), uuid.New()
			svc, taskRepo, statusRepo, vcsRepo, commentRepo := setupTaskServiceWithDoneGateAndGitLabChecker(&fakeGitLabMRChecker{})
			taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, StatusID: uuid.New(), Title: "mentioned", VCSLinkCount: 1}
			statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryDone}
			vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
				ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
				Status: domain.VCSLinkStatusMerged, URL: reviewSubjectURL, ExternalID: "101", Metadata: []byte(`{"relation":"relates_to"}`),
			})
			if tc.comment {
				commentRepo.items[uuid.New()] = &domain.Comment{TaskID: taskID, Body: "done"}
			}

			err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

			if tc.wantErr {
				var doneErr *DoneEvidenceError
				require.ErrorAs(t, err, &doneErr)
				assert.Empty(t, doneErr.PRURL, "the mention is not reported as the blocking PR")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// A review card that is already terminal must not be moved or commented again
// when its subject merges (redelivery, or the verdict closed it first).
func TestHandleMR_Merged_SkipsAlreadyDoneReviewCard(t *testing.T) {
	h := newHarness(t)
	work := h.makeTask(t, domain.StatusCategoryInProgress)
	review := h.makeTask(t, domain.StatusCategoryDone)
	h.bindReview(review, reviewSubjectURL, `{"role":"review"}`)

	_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mergedMREvent(101, work.ID, "MESH-"+work.ID.String()))
	require.NoError(t, err)

	assert.Equal(t, h.statusIDs[domain.StatusCategoryDone], h.taskRepo.tasks[review.ID].StatusID)
	links, _ := h.repo.ListByTask(context.Background(), review.ID)
	require.Len(t, links, 1)
	assert.Equal(t, domain.VCSLinkStatusMerged, links[0].Status)
}

// A failing link write is logged and skipped; the delivery itself still
// succeeds and the review card is left untouched.
func TestHandleMR_Merged_ReviewLinkUpsertFailureDoesNotFailDelivery(t *testing.T) {
	h := newHarness(t)
	work := h.makeTask(t, domain.StatusCategoryInProgress)
	review := h.makeTask(t, domain.StatusCategoryReview)
	h.bindReview(review, reviewSubjectURL, `{"role":"review"}`)
	// Fail only the review-subject write: the primary transition upserts first.
	h.repo.upsertErrFor = review.ID

	_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mergedMREvent(101, work.ID, "MESH-"+work.ID.String()))
	require.NoError(t, err)

	assert.Equal(t, h.statusIDs[domain.StatusCategoryReview], h.taskRepo.tasks[review.ID].StatusID)
}

func TestBindReviewSubject_CreateFailureIsSwallowed(t *testing.T) {
	svc, _, _ := setupTaskService()
	repo := newFakeVCSLinkRepo()
	repo.createErr = fmt.Errorf("db down")
	svc.vcsLinkRepo = repo
	task := &domain.Task{ID: uuid.New(), Title: "Ревью", Labels: pq.StringArray{"review"},
		Description: "https://gitlab.example.invalid/entire-vc/evc-keep/-/merge_requests/19"}

	svc.bindReviewSubject(context.Background(), task)

	links, _ := repo.ListByTask(context.Background(), task.ID)
	assert.Empty(t, links)
}

// A review card that a person still has to accept is not closed by the merge of
// the MR it reviews, exactly like the primary card (25.10 incident).
func TestHandleMR_Merged_DoesNotCloseGatedOrSignalledReviewCard(t *testing.T) {
	cases := map[string]func(*domain.Task){
		"human gate":        func(tk *domain.Task) { tk.HumanGate = true },
		"completion signal": func(tk *domain.Task) { tk.CompletionSignal = true },
	}
	for name, mark := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			work := h.makeTask(t, domain.StatusCategoryInProgress)
			review := h.makeTask(t, domain.StatusCategoryReview)
			mark(review)
			h.bindReview(review, reviewSubjectURL, `{"role":"review"}`)

			_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mergedMREvent(101, work.ID, "MESH-"+work.ID.String()))
			require.NoError(t, err)

			assert.Equal(t, h.statusIDs[domain.StatusCategoryReview], h.taskRepo.tasks[review.ID].StatusID, "review card must stay")
			require.NotEmpty(t, h.commentSvc.created)
			assert.Contains(t, h.commentSvc.created[len(h.commentSvc.created)-1].Body, "NOT moved to done")
			links, _ := h.repo.ListByTask(context.Background(), review.ID)
			require.Len(t, links, 1)
			assert.Equal(t, domain.VCSLinkStatusMerged, links[0].Status, "the link still records the merge")
		})
	}
}

// an internal ticket: a card that was not created as a review card can still declare
// its open MR the subject of its review through the metadata PATCH; the
// gate then treats it exactly as a bound review subject. Without the marker
// the link keeps blocking, and no other role value is accepted.
func TestPatchMetadata_RoleReviewLetsDoneGatePass(t *testing.T) {
	for _, tc := range []struct {
		name    string
		patch   map[string]any
		wantErr bool
	}{
		{"role=review patched", map[string]any{"role": "review"}, false},
		{"no role", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			linkSvc, linkRepo, _ := patchMetaSetup(domain.VCSLinkTypePR)
			link := &domain.VCSLink{ID: uuid.New(), TaskID: uuid.New(), LinkType: domain.VCSLinkTypePR, Metadata: []byte(`{}`)}
			linkRepo.links[link.ID] = link
			meta := link.Metadata
			if tc.patch != nil {
				got, err := linkSvc.PatchMetadata(context.Background(), link.TaskID, link.ID, tc.patch)
				require.NoError(t, err)
				require.True(t, got.IsReviewSubject())
				meta = got.Metadata
			}

			projectID, statusID := uuid.New(), uuid.New()
			checker := &fakeGitLabMRChecker{err: fmt.Errorf("dial tcp: connection refused")}
			svc, taskRepo, statusRepo, vcsRepo, commentRepo := setupTaskServiceWithDoneGateAndGitLabChecker(checker)
			taskRepo.items[link.TaskID] = &domain.Task{ID: link.TaskID, ProjectID: projectID, StatusID: uuid.New(), Title: "sign-off", VCSLinkCount: 1}
			commentRepo.items[uuid.New()] = &domain.Comment{TaskID: link.TaskID, Body: "verdict"}
			statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryDone}
			vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
				ID: link.ID, TaskID: link.TaskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
				Status: domain.VCSLinkStatusOpen, URL: reviewSubjectURL, ExternalID: "101", Metadata: meta,
			})

			err := svc.MoveTask(context.Background(), link.TaskID, MoveTaskInput{StatusID: &statusID})

			if tc.wantErr {
				var doneErr *DoneEvidenceError
				require.ErrorAs(t, err, &doneErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
