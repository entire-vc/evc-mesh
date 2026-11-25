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

// Mesh 8a018bf6: an agent's display name is not its slug (Nova's slug is
// `atlas`), yet the fleet addresses agents by name. `@Nova` used to resolve to
// nobody and the delivery verdict said recipient_unknown. GetBySlug now falls
// back to a unique case-insensitive name match after every slug lookup misses.

func TestAgentService_GetBySlug_FallsBackToHomeAgentName(t *testing.T) {
	f, ws := setupGrantAuthFixture()
	nova := &domain.Agent{ID: uuid.New(), WorkspaceID: ws.ID, Name: "Nova", Slug: "atlas"}
	f.agentRepo.items[nova.ID] = nova

	got, err := f.svc.GetBySlug(context.Background(), ws.ID, "nova")
	require.NoError(t, err)
	require.NotNil(t, got, "@Nova must resolve to the agent named Nova")
	assert.Equal(t, nova.ID, got.ID)
}

// The name fallback must not depend on a grant repo being wired: the early
// return for grantRepo == nil used to end the lookup after the home slug miss.
func TestAgentService_GetBySlug_NameFallbackWithoutGrantRepo(t *testing.T) {
	agentRepo := NewMockAgentRepository()
	svc := NewAgentService(agentRepo, NewMockActivityLogRepository(),
		NewMockWorkspaceRepository(), NewMockUserRepository()).(*agentService)
	wsID := uuid.New()
	nova := &domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Nova", Slug: "atlas"}
	agentRepo.items[nova.ID] = nova

	got, err := svc.GetBySlug(context.Background(), wsID, "nova")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, nova.ID, got.ID)
}

// Positive control: a home agent whose name equals its slug resolves exactly as
// before, and the slug lookup still answers first.
func TestAgentService_GetBySlug_HomeSlugStillResolves(t *testing.T) {
	f, ws := setupGrantAuthFixture()
	orion := &domain.Agent{ID: uuid.New(), WorkspaceID: ws.ID, Name: "Orion", Slug: "orion"}
	f.agentRepo.items[orion.ID] = orion

	got, err := f.svc.GetBySlug(context.Background(), ws.ID, "orion")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, orion.ID, got.ID)
}

// A slug always beats a name: `@atlas` is the agent with slug atlas even when
// another agent is named Atlas, and `@nova` goes to slug nova, not name Nova.
func TestAgentService_GetBySlug_SlugBeatsName(t *testing.T) {
	f, ws := setupGrantAuthFixture()
	named := &domain.Agent{ID: uuid.New(), WorkspaceID: ws.ID, Name: "Nova", Slug: "atlas"}
	slugged := &domain.Agent{ID: uuid.New(), WorkspaceID: ws.ID, Name: "Someone", Slug: "nova"}
	f.agentRepo.items[named.ID] = named
	f.agentRepo.items[slugged.ID] = slugged

	got, err := f.svc.GetBySlug(context.Background(), ws.ID, "nova")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, slugged.ID, got.ID, "slug match wins over name match")

	// A guest holding the slug also beats a home agent holding only the name.
	f2, ws2 := setupGrantAuthFixture()
	home := &domain.Agent{ID: uuid.New(), WorkspaceID: ws2.ID, Name: "Robin", Slug: "bird"}
	f2.agentRepo.items[home.ID] = home
	guest := seedGuestAgent(f2, "robin")
	seedGrant(t, f2, guest.ID, ws2, "member", false)

	got, err = f2.svc.GetBySlug(context.Background(), ws2.ID, "robin")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, guest.ID, got.ID, "guest slug wins over home name")
}

// Two agents sharing a name: no unique match, so the handle stays unresolved.
// It is (nil, nil), not an error, so every caller treats it exactly like the
// unknown handle it was before this fallback existed.
func TestAgentService_GetBySlug_AmbiguousNameDoesNotResolve(t *testing.T) {
	f, ws := setupGrantAuthFixture()
	for i := 0; i < 2; i++ {
		a := &domain.Agent{ID: uuid.New(), WorkspaceID: ws.ID, Name: "Twin", Slug: uuid.NewString()[:8]}
		f.agentRepo.items[a.ID] = a
	}

	got, err := f.svc.GetBySlug(context.Background(), ws.ID, "twin")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// A guest addressed by its name resolves when no home agent carries it; two
// guests with the same name do not.
func TestAgentService_GetBySlug_GuestByName(t *testing.T) {
	f, ws := setupGrantAuthFixture()
	guest := seedGuestAgent(f, "pixel-dev")
	guest.Name = "Pixel"
	f.grantRepo.SeedName(guest.ID, "Pixel")
	seedGrant(t, f, guest.ID, ws, "member", false)

	got, err := f.svc.GetBySlug(context.Background(), ws.ID, "pixel")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, guest.ID, got.ID)

	other := seedGuestAgent(f, "pixel-two")
	f.grantRepo.SeedName(other.ID, "PIXEL")
	seedGrant(t, f, other.ID, ws, "member", false)

	got, err = f.svc.GetBySlug(context.Background(), ws.ID, "pixel")
	require.NoError(t, err)
	assert.Nil(t, got, "two guests named Pixel is no unique match")
}

// A home agent named Nova wins over a guest named Nova, like home wins on slug.
func TestAgentService_GetBySlug_HomeNameBeatsGuestName(t *testing.T) {
	f, ws := setupGrantAuthFixture()
	home := &domain.Agent{ID: uuid.New(), WorkspaceID: ws.ID, Name: "Nova", Slug: "atlas"}
	f.agentRepo.items[home.ID] = home
	guest := seedGuestAgent(f, "nova-guest")
	f.grantRepo.SeedName(guest.ID, "Nova")
	seedGrant(t, f, guest.ID, ws, "member", false)

	got, err := f.svc.GetBySlug(context.Background(), ws.ID, "nova")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, home.ID, got.ID)
}

// commentEnvWithRealAgentService swaps the mention env's slug-only mock agent
// service for the real agentService over the mock repo, so notifyMentions runs
// the production resolver.
func commentEnvWithRealAgentService(t *testing.T) (mentionTestEnv, *MockAgentRepository, *fakeDeliveryRepo) {
	t.Helper()
	env := setupCommentServiceWithMentions()
	repo := &fakeDeliveryRepo{}
	env.svc.deliveryRepo = repo
	agentRepo := NewMockAgentRepository()
	env.svc.agentSvc = NewAgentService(agentRepo, NewMockActivityLogRepository(),
		NewMockWorkspaceRepository(), NewMockUserRepository())
	return env, agentRepo, repo
}

// AC1/AC2 of 8a018bf6 end to end: `@Nova` on a card Nova does not hold. The
// server legitimately reports skipped/not_assignee for a non-assignee, so the
// assertion is that the handle resolved to the agent, not recipient_unknown.
func TestNotifyMentions_AgentAddressedByNameResolves(t *testing.T) {
	env, agentRepo, repo := commentEnvWithRealAgentService(t)
	recent := time.Now().Add(-30 * time.Second)
	nova := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Name: "Nova", Slug: "atlas", LastHeartbeat: &recent}
	agentRepo.items[nova.ID] = nova

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, Title: "other card",
		AssigneeType: domain.AssigneeTypeUnassigned,
	}
	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@Nova проверь, пожалуйста, ревью?"}
	env.svc.notifyMentions(context.Background(), comment, env.taskRepo.items[taskID], "", env.wsID)

	rows := repo.snapshot()
	require.Len(t, rows, 1)
	row := rows[0]
	assert.Equal(t, "nova", row.RecipientSlug, "the written handle is kept, the Runner matches the body on it")
	assert.NotEqual(t, domain.ReasonRecipientUnknown, row.Reason)
	assert.Equal(t, domain.RecipientKindAgent, row.RecipientKind)
	require.NotNil(t, row.RecipientID)
	assert.Equal(t, nova.ID, *row.RecipientID)
	assert.Equal(t, domain.ReasonNotAssignee, row.Reason)
}

// Positive control through the same path: a home agent addressed by its slug.
func TestNotifyMentions_AgentAddressedBySlugStillResolves(t *testing.T) {
	env, agentRepo, repo := commentEnvWithRealAgentService(t)
	recent := time.Now().Add(-30 * time.Second)
	orion := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Name: "Orion", Slug: "orion", LastHeartbeat: &recent}
	agentRepo.items[orion.ID] = orion

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, Title: "T",
		AssigneeType: domain.AssigneeTypeUnassigned,
	}
	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@orion посмотри?"}
	env.svc.notifyMentions(context.Background(), comment, env.taskRepo.items[taskID], "", env.wsID)

	rows := repo.snapshot()
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].RecipientID)
	assert.Equal(t, orion.ID, *rows[0].RecipientID)
	assert.Equal(t, domain.ReasonNotAssignee, rows[0].Reason)
}

// Two agents named Twin: the comment is accepted and the handle is recorded as
// unknown, exactly as an unresolvable handle was before.
func TestNotifyMentions_AmbiguousNameStaysUnknown(t *testing.T) {
	env, agentRepo, repo := commentEnvWithRealAgentService(t)
	for i := 0; i < 2; i++ {
		a := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Name: "Twin", Slug: uuid.NewString()[:8]}
		agentRepo.items[a.ID] = a
	}
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "T"}
	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@Twin look?"}
	env.svc.notifyMentions(context.Background(), comment, env.taskRepo.items[taskID], "", env.wsID)

	rows := repo.snapshot()
	require.Len(t, rows, 1)
	assert.Equal(t, domain.ReasonRecipientUnknown, rows[0].Reason)
	assert.Nil(t, rows[0].RecipientID)
}

// A human username outranks an agent's display name. firstResolvedUserSlug and
// the gate-owner check use GetBySlug to tell an agent address from a human
// login, so a name match on a handle that is also a person's username would
// silently suppress that person's blocking gate. The slug lookups keep their
// old precedence; only the name fallback yields.
func TestAgentService_GetBySlug_NameFallbackYieldsToHumanUsername(t *testing.T) {
	agentRepo := NewMockAgentRepository()
	userRepo := NewMockUserRepository()
	svc := NewAgentService(agentRepo, NewMockActivityLogRepository(),
		NewMockWorkspaceRepository(), userRepo).(*agentService)
	wsID := uuid.New()
	a := &domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Nova", Slug: "atlas"}
	agentRepo.items[a.ID] = a
	userRepo.AddUser(wsID, &domain.User{ID: uuid.New(), Username: "nova"})

	got, err := svc.GetBySlug(context.Background(), wsID, "nova")
	require.NoError(t, err)
	assert.Nil(t, got, "a person's username is not taken over by an agent's display name")

	// The agent's own slug still resolves it.
	got, err = svc.GetBySlug(context.Background(), wsID, "atlas")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, a.ID, got.ID)
}

// Without a user repository the service cannot rule out a human login, so the
// name fallback stays off and the handle behaves exactly as before.
func TestAgentService_GetBySlug_NoNameFallbackWithoutUserRepo(t *testing.T) {
	agentRepo := NewMockAgentRepository()
	svc := NewAgentService(agentRepo, NewMockActivityLogRepository(),
		NewMockWorkspaceRepository(), nil).(*agentService)
	wsID := uuid.New()
	a := &domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Nova", Slug: "atlas"}
	agentRepo.items[a.ID] = a

	got, err := svc.GetBySlug(context.Background(), wsID, "nova")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// The reviewed regression end to end: `❓ Blocking @nova` where nova is a
// person's login and also another agent's display name still arms the human
// gate for the person.
func TestFirstResolvedUserSlug_HumanUsernameBeatsAgentDisplayName(t *testing.T) {
	env := setupCommentServiceWithMentions()
	agentRepo := NewMockAgentRepository()
	userRepo := NewMockUserRepository()
	env.svc.agentSvc = NewAgentService(agentRepo, NewMockActivityLogRepository(),
		NewMockWorkspaceRepository(), userRepo)
	env.svc.userRepo = userRepo
	a := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Name: "Nova", Slug: "atlas"}
	agentRepo.items[a.ID] = a
	userRepo.AddUser(env.wsID, &domain.User{ID: uuid.New(), Username: "nova"})

	assert.Equal(t, "nova", env.svc.firstResolvedUserSlug(context.Background(), env.wsID, []string{"nova"}))
}
