package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

type recordedEvent struct {
	ws      uuid.UUID
	event   string
	payload map[string]any
}

// captureDispatcher is the true boundary here (the webhook/extension fan-out);
// everything above it is the real service.
type captureDispatcher struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (d *captureDispatcher) Dispatch(_ context.Context, ws uuid.UUID, event string, payload any) {
	p, _ := payload.(map[string]any)
	d.mu.Lock()
	d.events = append(d.events, recordedEvent{ws, event, p})
	d.mu.Unlock()
}

func (d *captureDispatcher) waitFor(t *testing.T, event string) recordedEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		for _, e := range d.events {
			if e.event == event {
				d.mu.Unlock()
				return e
			}
		}
		d.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("event %q was not dispatched", event)
	return recordedEvent{}
}

func (d *captureDispatcher) count(event string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, e := range d.events {
		if e.event == event {
			n++
		}
	}
	return n
}

func TestProjectService_DispatchesCreatedAndUpdated(t *testing.T) {
	svc, _, _ := setupProjectService()
	d := &captureDispatcher{}
	svc.SetEventDispatcher(d)
	ctx := context.Background()

	p := &domain.Project{WorkspaceID: uuid.New(), Name: "Alpha"}
	require.NoError(t, svc.Create(ctx, p))
	created := d.waitFor(t, "project.created")
	assert.Equal(t, p.WorkspaceID, created.ws)
	assert.Equal(t, p.ID, created.payload["project_id"], "carries the id an external_ref is built from")

	p.Name = "Alpha 2"
	require.NoError(t, svc.Update(ctx, p))
	d.waitFor(t, "project.updated")
	require.NoError(t, svc.Archive(ctx, p.ID))
	require.Eventually(t, func() bool { return d.count("project.updated") == 2 }, 2*time.Second, 5*time.Millisecond)

	// A failed create dispatches nothing.
	bad := &domain.Project{WorkspaceID: uuid.New(), Name: " "}
	require.Error(t, svc.Create(ctx, bad))
	assert.Equal(t, 1, d.count("project.created"))
}

func TestWorkspaceMemberService_DispatchesAddedAndRemoved(t *testing.T) {
	svc, _, _ := newIdentityFixture()
	d := &captureDispatcher{}
	svc.(*workspaceMemberService).SetEventDispatcher(d)
	ctx := context.Background()
	ws := uuid.New()

	m, err := svc.AddMemberWithCreate(ctx, ws, "keep-sync@example.com", "Keep Sync", domain.RoleMember, "StrongP4ss", uuid.Nil)
	require.NoError(t, err)
	added := d.waitFor(t, "workspace.member.added")
	assert.Equal(t, m.User.ID, added.payload["user_id"])
	assert.Equal(t, domain.RoleMember, added.payload["role"])
	assert.NotContains(t, added.payload, "email", "the event carries ids, the export carries identity")

}

func TestWorkspaceMemberService_RemoveDispatchesRemoved(t *testing.T) {
	memberRepo := NewMockWorkspaceMemberRepository()
	svc := NewWorkspaceMemberService(memberRepo, NewMockUserRepository(), NewMockProjectMemberRepository(), nil, nil)
	d := &captureDispatcher{}
	svc.(*workspaceMemberService).SetEventDispatcher(d)
	ws, user := uuid.New(), uuid.New()
	memberRepo.Seed(ws, domain.WorkspaceMemberWithUser{WorkspaceMember: domain.WorkspaceMember{
		ID: uuid.New(), UserID: user, Role: domain.RoleMember}})

	require.NoError(t, svc.RemoveMember(context.Background(), ws, user))
	removed := d.waitFor(t, "workspace.member.removed")
	assert.Equal(t, ws, removed.ws)
	assert.Equal(t, user, removed.payload["user_id"])

	// Removing someone who is not a member dispatches nothing.
	require.Error(t, svc.RemoveMember(context.Background(), ws, uuid.New()))
	assert.Equal(t, 1, d.count("workspace.member.removed"))
}

type sponsorRecorder struct {
	agent, user uuid.UUID
	calls       int
}

func (r *sponsorRecorder) SetAgentSponsor(_ context.Context, a, u uuid.UUID) error {
	r.agent, r.user = a, u
	r.calls++
	return nil
}

func TestRecordSponsor(t *testing.T) {
	agent, human, supervisor := uuid.New(), uuid.New(), uuid.New()
	userCtx := actorctx.WithActor(context.Background(), human, domain.ActorTypeUser)
	agentCtx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	r := &sponsorRecorder{}
	recordSponsor(userCtx, r, agent, &supervisor)
	assert.Equal(t, human, r.user, "the human who made the request wins over the named supervisor")

	r = &sponsorRecorder{}
	recordSponsor(agentCtx, r, agent, &supervisor)
	assert.Equal(t, supervisor, r.user, "a non-human request falls back to the supervisor")

	r = &sponsorRecorder{}
	recordSponsor(agentCtx, r, agent, nil)
	assert.Zero(t, r.calls, "nobody to name: sponsor stays unknown (NULL), not a guess")

	recordSponsor(userCtx, nil, agent, nil) // no recorder wired: no panic
}

// The wiring in cmd/api asserts on the cache wrapper. Without the forward it
// succeeds and records nothing — the same trap SetAgentWorkspaceGrantRepo
// documents.
func TestCachedAgentAuth_ForwardsSponsorRecorder(t *testing.T) {
	inner := &agentService{}
	wrapped := NewCachedAgentAuth(inner, AgentAuthCacheTTL)
	c, ok := wrapped.(AgentSponsorConfigurable)
	require.True(t, ok)
	r := &sponsorRecorder{}
	c.SetAgentSponsorRecorder(r)
	assert.Same(t, r, inner.sponsors)
}
