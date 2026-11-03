package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Existing links of other cards that name exactly the delivered MR get its
// status and head; nothing else about them or their cards changes.

const secondaryURL = "https://gitlab.example.invalid/throwaway/repo/-/merge_requests/42"

type secondarySyncFixture struct {
	h       *workspaceScopeHarness
	primary *domain.Task // child, already done
	root    *domain.Task // late-linked root, in_progress
}

func newSecondarySyncFixture(t *testing.T) *secondarySyncFixture {
	t.Helper()
	h := newWorkspaceScopeHarness(t)
	done := h.statusRepo.addStatus(h.projA, "done", domain.StatusCategoryDone)
	primary := &domain.Task{ID: fixedTaskID(201), ProjectID: h.projA, StatusID: done.ID}
	root := &domain.Task{ID: fixedTaskID(202), ProjectID: h.projA, StatusID: h.taskA.StatusID}
	h.taskRepo.tasks[primary.ID] = primary
	h.taskRepo.tasks[root.ID] = root
	return &secondarySyncFixture{h: h, primary: primary, root: root}
}

func (f *secondarySyncFixture) addLink(t *testing.T, task *domain.Task, url, externalID, linkType, meta string) *domain.VCSLink {
	t.Helper()
	l := &domain.VCSLink{
		ID: uuid.New(), TaskID: task.ID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkType(linkType),
		ExternalID: externalID, URL: url, Status: domain.VCSLinkStatusOpen, Metadata: []byte(meta),
	}
	require.NoError(t, f.h.repo.Create(context.Background(), l))
	return l
}

func (f *secondarySyncFixture) event(ws uuid.UUID, head string) GitLabWebhookEvent {
	ev := f.h.mergedMREvent(ws, "Child MR for #"+f.primary.ID.String()[:8])
	ev.HeadSHA = head
	return ev
}

func (f *secondarySyncFixture) stored(t *testing.T, id uuid.UUID) domain.VCSLink {
	t.Helper()
	l, err := f.h.repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, l)
	return *l
}

func TestHandleMR_RefreshesExistingSecondaryLink_PrimaryDone(t *testing.T) {
	f := newSecondarySyncFixture(t)
	f.addLink(t, f.primary, secondaryURL, "42", "pr", `{}`)
	sec := f.addLink(t, f.root, secondaryURL, "42", "pr", `{"ship_sha":"abc123","binding":"linked","extra":{"k":1}}`)
	rootStatus := f.root.StatusID

	ctx := context.Background()
	_, err := f.h.svc.HandleGitLabMergeRequestEvent(ctx, f.event(f.h.wsA, "headsha1"))
	require.NoError(t, err)

	got := f.stored(t, sec.ID)
	assert.Equal(t, domain.VCSLinkStatusMerged, got.Status)
	assert.Equal(t, "headsha1", testHeadSHA(got))
	assert.JSONEq(t, `{"ship_sha":"abc123","binding":"linked","extra":{"k":1},"head_sha":"headsha1"}`, string(got.Metadata))
	assert.Equal(t, rootStatus, f.h.taskRepo.tasks[f.root.ID].StatusID, "secondary card must not move")
	links, _ := f.h.repo.ListByTask(ctx, f.root.ID)
	assert.Len(t, links, 1, "no link may be created")

	// Idempotent: a resend changes nothing.
	_, err = f.h.svc.HandleGitLabMergeRequestEvent(ctx, f.event(f.h.wsA, "headsha1"))
	require.NoError(t, err)
	assert.Equal(t, got.Metadata, f.stored(t, sec.ID).Metadata)

	// An empty head clears the stored one (an old head is never current).
	_, err = f.h.svc.HandleGitLabMergeRequestEvent(ctx, f.event(f.h.wsA, ""))
	require.NoError(t, err)
	got = f.stored(t, sec.ID)
	assert.Equal(t, "", testHeadSHA(got))
	assert.Contains(t, string(got.Metadata), `"ship_sha":"abc123"`)
}

func TestHandleMR_SecondarySync_RunsForOpenMR(t *testing.T) {
	f := newSecondarySyncFixture(t)
	f.addLink(t, f.primary, secondaryURL, "42", "pr", `{}`)
	sec := f.addLink(t, f.root, secondaryURL, "42", "pr", `{}`)
	ev := f.event(f.h.wsA, "newhead")
	ev.MRState = "opened"
	_, err := f.h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	got := f.stored(t, sec.ID)
	assert.Equal(t, domain.VCSLinkStatusOpen, got.Status)
	assert.Equal(t, "newhead", testHeadSHA(got))
}

// Known-bad controls: each leaves the other link exactly as it was.
func TestHandleMR_SecondarySync_Negatives(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *secondarySyncFixture, t *testing.T) *domain.VCSLink
		ev    func(f *secondarySyncFixture) GitLabWebhookEvent
	}{
		{"foreign workspace", func(f *secondarySyncFixture, t *testing.T) *domain.VCSLink {
			other := &domain.Task{ID: fixedTaskID(203), ProjectID: f.h.projB, StatusID: f.root.StatusID}
			f.h.taskRepo.tasks[other.ID] = other
			return f.addLink(t, other, secondaryURL, "42", "pr", `{}`)
		}, nil},
		{"same iid other repo", func(f *secondarySyncFixture, t *testing.T) *domain.VCSLink {
			return f.addLink(t, f.root, "https://gitlab.example.invalid/other/repo/-/merge_requests/42", "42", "pr", `{}`)
		}, nil},
		{"same iid other host", func(f *secondarySyncFixture, t *testing.T) *domain.VCSLink {
			return f.addLink(t, f.root, "https://gitlab.example.org/throwaway/repo/-/merge_requests/42", "42", "pr", `{}`)
		}, nil},
		{"reference link", func(f *secondarySyncFixture, t *testing.T) *domain.VCSLink {
			return f.addLink(t, f.root, secondaryURL, "42", "reference", `{}`)
		}, nil},
		{"empty project path", func(f *secondarySyncFixture, t *testing.T) *domain.VCSLink {
			return f.addLink(t, f.root, secondaryURL, "42", "pr", `{}`)
		}, func(f *secondarySyncFixture) GitLabWebhookEvent {
			ev := f.event(f.h.wsA, "h")
			ev.ProjectPath = ""
			return ev
		}},
		{"url disagrees with project", func(f *secondarySyncFixture, t *testing.T) *domain.VCSLink {
			return f.addLink(t, f.root, secondaryURL, "42", "pr", `{}`)
		}, func(f *secondarySyncFixture) GitLabWebhookEvent {
			ev := f.event(f.h.wsA, "h")
			ev.ProjectPath = "other/repo"
			return ev
		}},
		{"unproven legacy scope", func(f *secondarySyncFixture, t *testing.T) *domain.VCSLink {
			return f.addLink(t, f.root, secondaryURL, "42", "pr", `{}`)
		}, func(f *secondarySyncFixture) GitLabWebhookEvent {
			f.h.projectRepo.projects = map[uuid.UUID]*domain.Project{} // project of the primary unknown
			return f.event(uuid.Nil, "h")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSecondarySyncFixture(t)
			f.addLink(t, f.primary, secondaryURL, "42", "pr", `{}`)
			sec := tc.setup(f, t)
			before := f.stored(t, sec.ID)
			ev := f.event(f.h.wsA, "h")
			if tc.ev != nil {
				ev = tc.ev(f)
			}
			_, _ = f.h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
			after := f.stored(t, sec.ID)
			assert.Equal(t, before.Status, after.Status)
			assert.Equal(t, string(before.Metadata), string(after.Metadata))
		})
	}
}

func TestHandleMR_SecondarySync_LegacyDeliveryNarrowedToPrimaryWorkspace(t *testing.T) {
	f := newSecondarySyncFixture(t)
	f.addLink(t, f.primary, secondaryURL, "42", "pr", `{}`)
	same := f.addLink(t, f.root, secondaryURL, "42", "pr", `{}`)
	foreign := &domain.Task{ID: fixedTaskID(203), ProjectID: f.h.projB, StatusID: f.root.StatusID}
	f.h.taskRepo.tasks[foreign.ID] = foreign
	fl := f.addLink(t, foreign, secondaryURL, "42", "pr", `{}`)

	_, err := f.h.svc.HandleGitLabMergeRequestEvent(context.Background(), f.event(uuid.Nil, "h1"))
	require.NoError(t, err)
	assert.Equal(t, "h1", testHeadSHA(f.stored(t, same.ID)))
	assert.Equal(t, domain.VCSLinkStatusOpen, f.stored(t, fl.ID).Status, "other workspace must stay untouched")
}

func TestHandleMR_SecondarySync_AmbiguousRefNotWidened(t *testing.T) {
	f := newSecondarySyncFixture(t)
	f.addLink(t, f.primary, secondaryURL, "42", "pr", `{}`)
	sec := f.addLink(t, f.root, secondaryURL, "42", "pr", `{}`)
	ev := f.event(f.h.wsA, "h")
	// two different task refs in one payload: ambiguous, the unmentioned root's link is not widened into
	third := &domain.Task{ID: fixedTaskID(204), ProjectID: f.h.projA, StatusID: f.root.StatusID}
	f.h.taskRepo.tasks[third.ID] = third
	ev.MRTitle = "both #" + f.primary.ID.String()[:8] + " and #" + third.ID.String()[:8]
	_, _ = f.h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	assert.Equal(t, domain.VCSLinkStatusOpen, f.stored(t, sec.ID).Status)
}

func testHeadSHA(l domain.VCSLink) string {
	var m map[string]any
	_ = json.Unmarshal(l.Metadata, &m)
	v, _ := m[domain.VCSLinkMetaHeadSHA].(string)
	return v
}

// No ref in the payload and exactly one stored link: that link's card is the
// unambiguous primary; the delivery still resolves to it and a link of another
// workspace is left alone.
func TestHandleMR_SecondarySync_SingleStoredLinkFallback(t *testing.T) {
	f := newSecondarySyncFixture(t)
	f.addLink(t, f.primary, secondaryURL, "42", "pr", `{}`)
	foreign := &domain.Task{ID: fixedTaskID(203), ProjectID: f.h.projB, StatusID: f.root.StatusID}
	f.h.taskRepo.tasks[foreign.ID] = foreign
	fl := f.addLink(t, foreign, secondaryURL, "42", "pr", `{}`)

	ev := f.event(f.h.wsA, "h2")
	ev.MRTitle = "no reference here"
	res, err := f.h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, f.primary.ID, res.TaskID)
	assert.Equal(t, domain.VCSLinkStatusOpen, f.stored(t, fl.ID).Status)
}

// The primary being a foreign-repository mention (a reference, not its own
// deliverable) does not stop the other card's exact-MR link being refreshed.
func TestHandleMR_SecondarySync_PrimaryIsForeignMention(t *testing.T) {
	f := newSecondarySyncFixture(t)
	sec := f.addLink(t, f.root, secondaryURL, "42", "pr", `{"ship_sha":"abc"}`)
	f.addLink(t, f.primary, secondaryURL, "42", "reference", `{}`) // the primary only mentions this MR
	_, err := f.h.svc.HandleGitLabMergeRequestEvent(context.Background(), f.event(f.h.wsA, "fh"))
	require.NoError(t, err)
	got := f.stored(t, sec.ID)
	assert.Equal(t, "fh", testHeadSHA(got))
	assert.Contains(t, string(got.Metadata), `"ship_sha":"abc"`)
}
