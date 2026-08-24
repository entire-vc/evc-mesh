package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// setupTaskServiceWithVCSIntegrationResolver wires a taskService the same
// way setupTaskServiceWithDoneGateAndGitHubChecker does, but with a dynamic
// VCSIntegrationResolver instead of a static githubPRChecker/gitlabMRChecker
// — proving the #33a4bb57 per-workspace path (§C1) end-to-end through the
// real done-evidence gate, not just the private resolve* helpers in
// isolation. projectRepo comes from newTestTaskService's own default
// (WithDefaultWorkspace(testDefaultWorkspaceID)), so every task's project
// resolves to testDefaultWorkspaceID unless the test overrides it.
func setupTaskServiceWithVCSIntegrationResolver(resolver *VCSIntegrationResolver) (*taskService, *MockTaskRepository, *MockTaskStatusRepository, *MockVCSLinkRepository, *MockCommentRepository) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	vcsRepo := NewMockVCSLinkRepository()
	commentRepo := NewMockCommentRepository()

	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithVCSLinkRepoTask(vcsRepo),
		WithCommentRepoTask(commentRepo),
		WithVCSIntegrationResolver(resolver),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, statusRepo, vcsRepo, commentRepo
}

// ---------------------------------------------------------------------------
// resolveGitHubChecker / resolveGitLabChecker — private-method coverage of
// the per-workspace resolution itself (§C1).
// ---------------------------------------------------------------------------

func TestResolveGitHubChecker_NilResolver_UsesLegacyStaticChecker(t *testing.T) {
	checker := &fakeGitHubPRChecker{}
	svc, _, _, _, _ := setupTaskServiceWithDoneGateAndGitHubChecker(checker)

	got, ok := svc.resolveGitHubChecker(context.Background(), uuid.New())
	require.True(t, ok)
	assert.Same(t, checker, got, "with no vcsIntegrations wired, the static checker must be used unconditionally, ignoring workspaceID")
}

func TestResolveGitHubChecker_ResolverWired_UsableWorkspaceRow(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: githubCfg("workspace-token", "")})
	resolver := NewVCSIntegrationResolver(repo, VCSEnvFallback{})
	svc, _, _, _, _ := setupTaskServiceWithVCSIntegrationResolver(resolver)

	_, ok := svc.resolveGitHubChecker(context.Background(), ws)
	assert.True(t, ok, "an active row with a token must resolve a usable checker")
}

func TestResolveGitHubChecker_ResolverWired_TokenlessRow_NotUsable(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	// Row supplies ONLY a webhook_secret — legitimate for webhook validation
	// but the live-check client construction still requires a token, mirroring
	// the pre-#33a4bb57 `if cfg.Webhook.GitHubToken != ""` gate.
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: githubCfg("", "webhook-secret-only")})
	resolver := NewVCSIntegrationResolver(repo, VCSEnvFallback{})
	svc, _, _, _, _ := setupTaskServiceWithVCSIntegrationResolver(resolver)

	_, ok := svc.resolveGitHubChecker(context.Background(), ws)
	assert.False(t, ok, "a row with no token must not produce a live-check client, even though it IS a usable row for webhook validation")
}

func TestResolveGitHubChecker_ResolverWired_DifferentWorkspacesGetDifferentOutcomes(t *testing.T) {
	wsConfigured, wsUnconfigured := uuid.New(), uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(wsConfigured, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: githubCfg("tok", "")})
	resolver := NewVCSIntegrationResolver(repo, VCSEnvFallback{}) // no env fallback
	svc, _, _, _, _ := setupTaskServiceWithVCSIntegrationResolver(resolver)

	_, ok := svc.resolveGitHubChecker(context.Background(), wsConfigured)
	assert.True(t, ok, "the configured workspace must resolve a checker")

	_, ok = svc.resolveGitHubChecker(context.Background(), wsUnconfigured)
	assert.False(t, ok, "an unrelated workspace with nothing configured (and no env) must NOT resolve a checker — workspaces stay isolated (§C4)")
}

// Proves resolution happens fresh on every call, not once at construction:
// flipping the stored row's is_active between two calls on the SAME
// taskService (no restart, no re-wiring) changes the outcome immediately.
func TestResolveGitHubChecker_ResolverWired_ReflectsChangeWithoutRestart(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: githubCfg("tok-v1", "")})
	resolver := NewVCSIntegrationResolver(repo, VCSEnvFallback{})
	svc, _, _, _, _ := setupTaskServiceWithVCSIntegrationResolver(resolver)

	_, ok := svc.resolveGitHubChecker(context.Background(), ws)
	require.True(t, ok)

	// Simulate a token rotation via the UI (Update handler upserts a new row).
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: false, Config: githubCfg("tok-v1", "")})
	_, ok = svc.resolveGitHubChecker(context.Background(), ws)
	assert.False(t, ok, "deactivating must take effect on the very next call, no restart/reload needed")
}

func TestResolveGitLabChecker_ResolverWired_RequiresBothBaseURLAndToken(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: true, Config: gitlabCfg("", "tok", "")}) // no base_url
	resolver := NewVCSIntegrationResolver(repo, VCSEnvFallback{})
	svc, _, _, _, _ := setupTaskServiceWithVCSIntegrationResolver(resolver)

	_, ok := svc.resolveGitLabChecker(context.Background(), ws)
	assert.False(t, ok, "a row missing base_url must not produce a live-check client")
}

// ---------------------------------------------------------------------------
// hasLiveChecker — the done-evidence gate's "is access configured at all"
// signal (PRProviderAccessConfigured), now per-workspace.
// ---------------------------------------------------------------------------

func TestHasLiveChecker_ResolverWired_PerWorkspace(t *testing.T) {
	wsConfigured, wsUnconfigured := uuid.New(), uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(wsConfigured, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: githubCfg("tok", "")})
	resolver := NewVCSIntegrationResolver(repo, VCSEnvFallback{})
	svc, _, _, _, _ := setupTaskServiceWithVCSIntegrationResolver(resolver)

	assert.True(t, svc.hasLiveChecker(context.Background(), domain.VCSProviderGitHub, wsConfigured))
	assert.False(t, svc.hasLiveChecker(context.Background(), domain.VCSProviderGitHub, wsUnconfigured))
}

// ---------------------------------------------------------------------------
// MoveTask (done-evidence gate) end-to-end through the dynamic resolver —
// no real HTTP call is made: the link's URL deliberately doesn't parse as a
// GitHub PR URL, so ParsePullRequestURL short-circuits isPRMergedLive to
// live=false BEFORE any network round trip, while still proving the
// checker itself resolved (hasLiveChecker=true → PRProviderAccessConfigured
// in the resulting DoneEvidenceError). This is the same technique the done-
// evidence gate's OWN pre-existing tests use to stay hermetic.
// ---------------------------------------------------------------------------

func TestTaskService_MoveTask_DoneGate_PRProviderAccessConfigured_ReflectsWorkspace(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	repo := newFakeVCSIntegrationRepo()
	repo.put(testDefaultWorkspaceID, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: true, Config: githubCfg("workspace-token", "")})
	resolver := NewVCSIntegrationResolver(repo, VCSEnvFallback{})
	svc, taskRepo, statusRepo, vcsRepo, _ := setupTaskServiceWithVCSIntegrationResolver(resolver)

	taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    projectID, // resolves to testDefaultWorkspaceID via the mock project repo's default
		StatusID:     uuid.New(),
		Title:        "Task with a configured workspace but an unparseable link URL",
		VCSLinkCount: 1,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryDone}
	vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
		ID:       uuid.New(),
		TaskID:   taskID,
		Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR,
		Status:   domain.VCSLinkStatusOpen,
		URL:      "not-a-github-pr-url", // deliberately unparseable — keeps this test hermetic (no HTTP)
		Title:    "some PR",
	})

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})
	require.Error(t, err, "an unparseable link URL means live=false, so the cached (non-merged) status must still block done")

	var doneErr *DoneEvidenceError
	require.ErrorAs(t, err, &doneErr)
	assert.True(t, doneErr.PRProviderAccessConfigured, "the workspace's own GitHub integration IS configured — the gate must say so, not imply no access exists")
}

func TestTaskService_MoveTask_DoneGate_PRProviderAccessConfigured_FalseWhenWorkspaceUnconfigured(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	repo := newFakeVCSIntegrationRepo() // nothing stored for testDefaultWorkspaceID
	resolver := NewVCSIntegrationResolver(repo, VCSEnvFallback{})
	svc, taskRepo, statusRepo, vcsRepo, _ := setupTaskServiceWithVCSIntegrationResolver(resolver)

	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projectID, StatusID: uuid.New(),
		Title: "Task with no configured GitHub access at all", VCSLinkCount: 1,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryDone}
	vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub, LinkType: domain.VCSLinkTypePR,
		Status: domain.VCSLinkStatusOpen, URL: "https://github.com/example/repo/pull/1", Title: "some PR",
	})

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})
	require.Error(t, err)

	var doneErr *DoneEvidenceError
	require.ErrorAs(t, err, &doneErr)
	assert.False(t, doneErr.PRProviderAccessConfigured, "no workspace row and no env: the gate must say access isn't configured, not imply a failed attempt")
}
