package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ---------------------------------------------------------------------------
// Test doubles the mention path needs and the shared mock file does not have.
// ---------------------------------------------------------------------------

// MockDocumentCommentMentionRepository records the rows the service writes.
type MockDocumentCommentMentionRepository struct {
	mu          sync.Mutex
	rows        []domain.DocumentCommentMention
	seen        [][2]uuid.UUID
	errToReturn error
}

func NewMockDocumentCommentMentionRepository() *MockDocumentCommentMentionRepository {
	return &MockDocumentCommentMentionRepository{}
}

// FailWith makes InsertBatch fail, so a test can prove that a comment survives a
// mention-bookkeeping failure.
func (m *MockDocumentCommentMentionRepository) FailWith(err error) *MockDocumentCommentMentionRepository {
	m.errToReturn = err
	return m
}

func (m *MockDocumentCommentMentionRepository) InsertBatch(_ context.Context, mentions []domain.DocumentCommentMention) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.rows = append(m.rows, mentions...)
	return nil
}

func (m *MockDocumentCommentMentionRepository) Rows() []domain.DocumentCommentMention {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.DocumentCommentMention, len(m.rows))
	copy(out, m.rows)
	return out
}

func (m *MockDocumentCommentMentionRepository) List(
	_ context.Context, mentionedID uuid.UUID, mentionedKind string, _ repository.MentionFilter,
) ([]domain.DocumentCommentMentionView, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	var out []domain.DocumentCommentMentionView
	for _, r := range m.Rows() {
		if r.MentionedID == mentionedID && r.MentionedKind == mentionedKind {
			out = append(out, domain.DocumentCommentMentionView{
				CommentID: r.CommentID, MentionedID: r.MentionedID,
				MentionedKind: r.MentionedKind, MentionedSlug: r.MentionedSlug,
			})
		}
	}
	return out, nil
}

func (m *MockDocumentCommentMentionRepository) MarkSeen(_ context.Context, commentID, mentionedID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.seen = append(m.seen, [2]uuid.UUID{commentID, mentionedID})
	return nil
}

func (m *MockDocumentCommentMentionRepository) Seen() [][2]uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][2]uuid.UUID, len(m.seen))
	copy(out, m.seen)
	return out
}

func (m *MockDocumentCommentMentionRepository) CountUnseen(_ context.Context, mentionedID uuid.UUID, mentionedKind string) (int64, error) {
	if m.errToReturn != nil {
		return 0, m.errToReturn
	}
	rows, _ := m.List(context.Background(), mentionedID, mentionedKind, repository.MentionFilter{})
	return int64(len(rows)), nil
}

// MockWSPublisher records the live badge pushes.
type MockWSPublisher struct {
	mu       sync.Mutex
	channels []string
	events   []any
	err      error
}

func NewMockWSPublisher() *MockWSPublisher { return &MockWSPublisher{} }

func (m *MockWSPublisher) Publish(_ context.Context, channel string, event any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels = append(m.channels, channel)
	m.events = append(m.events, event)
	return m.err
}

func (m *MockWSPublisher) Channels() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.channels))
	copy(out, m.channels)
	return out
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// documentMentionFixture is the document-comment fixture with every mention
// dependency wired, which is how the service runs in production (see
// cmd/api/main.go).
type documentMentionFixture struct {
	*documentCommentFixture

	agents      *MockAgentService
	users       *MockUserRepository
	mentions    *MockDocumentCommentMentionRepository
	agentNotify *MockAgentNotifyService
	notify      *MockNotificationService
	wsPub       *MockWSPublisher

	agentID uuid.UUID
	userID  uuid.UUID
}

func setupDocumentMentions(t *testing.T) *documentMentionFixture {
	t.Helper()

	base := setupDocumentCommentService(t)

	agents := NewMockAgentService()
	users := NewMockUserRepository()
	mentions := NewMockDocumentCommentMentionRepository()
	agentNotify := NewMockAgentNotifyService()
	notify := NewMockNotificationService()
	wsPub := NewMockWSPublisher()

	agentID, userID := uuid.New(), uuid.New()
	agents.AddAgent(base.wsID, &domain.Agent{ID: agentID, Slug: "daedalus", Name: "Daedalus"})
	users.AddUser(base.wsID, &domain.User{ID: userID, Username: "pavel", Name: "Pavel"})

	base.svc = NewDocumentCommentService(base.comments, base.docs, base.docs,
		WithDocumentCommentAgentService(agents),
		WithDocumentCommentUserRepo(users),
		WithDocumentCommentMentionRepo(mentions),
		WithDocumentCommentAgentNotifier(agentNotify),
		WithDocumentCommentNotificationService(notify),
		WithDocumentCommentWSPublisher(wsPub),
	)

	return &documentMentionFixture{
		documentCommentFixture: base,
		agents:                 agents,
		users:                  users,
		mentions:               mentions,
		agentNotify:            agentNotify,
		notify:                 notify,
		wsPub:                  wsPub,
		agentID:                agentID,
		userID:                 userID,
	}
}

// authoredBy returns a context carrying the author's identity and display name,
// which is what the notification title is built from.
func authoredBy(id uuid.UUID, kind domain.ActorType, name string) context.Context {
	return actorctx.WithActorName(actorctx.WithActor(context.Background(), id, kind), name)
}

// comment posts a body as the fixture's author.
func (f *documentMentionFixture) comment(ctx context.Context, body string) (*domain.DocumentComment, error) {
	in := f.createInput()
	in.Body = body
	return f.svc.Create(ctx, in)
}

// ---------------------------------------------------------------------------
// Extraction
// ---------------------------------------------------------------------------

func TestExtractDocumentMentionSlugs(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{"plain", "ping @daedalus about this", []string{"daedalus"}},
		{"at line start", "@pavel take a look", []string{"pavel"}},
		{"after a bracket", "(@pavel) and [@daedalus]", []string{"pavel", "daedalus"}},
		{"deduplicated, order preserved", "@pavel @daedalus @pavel", []string{"pavel", "daedalus"}},
		{"an email address is not a mention", "write to bob@example.com", []string{}},
		{"mid-word is not a mention", "foo@bar", []string{}},
		{"none", "no mentions at all", []string{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, extractDocumentMentionSlugs(tc.body))
		})
	}
}

// TestExtractDocumentMentionSlugs_IgnoresCode is the escape hatch that makes the
// hard refusal below survivable: an @ inside code is quoted text, not an
// address, and an author pasting a log line must be able to say so.
func TestExtractDocumentMentionSlugs_IgnoresCode(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{"inline code", "the flag is `--user @nobody` here", []string{}},
		{"fenced block", "```\nsudo -u @nobody make\n```", []string{}},
		{"multiline fenced block", "before\n```sh\n@nobody\n@alsonobody\n```\nafter", []string{}},
		{"code does not swallow the rest of the line", "`@nobody` but @pavel is real", []string{"pavel"}},
		{"an unterminated fence does not mask the document", "```\n@pavel", []string{"pavel"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, extractDocumentMentionSlugs(tc.body))
		})
	}
}

// TestMaskCodeSpans_PreservesLength: the mask has to be byte-for-byte the same
// size as what it replaces, or every offset after it — including the anchor
// offsets stored on the same comment — would refer to different text.
func TestMaskCodeSpans_PreservesLength(t *testing.T) {
	body := "a `code` span and ```a fenced\none```"
	assert.Len(t, maskCodeSpans(body), len(body))
}

// ---------------------------------------------------------------------------
// The negative control: an unresolvable mention is never silent
// ---------------------------------------------------------------------------

// TestCreate_UnresolvableMentionIsRefused is the regression test for the
// incident this feature was specified around: a slug that resolves to nobody
// used to produce no row, no notification and no log, which is
// indistinguishable from a comment that mentioned nobody. It must now be
// impossible to write one by accident.
func TestCreate_UnresolvableMentionIsRefused(t *testing.T) {
	f := setupDocumentMentions(t)

	c, err := f.comment(authoredBy(f.author, domain.ActorTypeUser, "Ann"), "ping @nobodyhere please")

	require.Error(t, err)
	assert.Nil(t, c, "no comment may exist for a request that was refused")

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation["body"], "@nobodyhere",
		"the error has to name the slug that failed, or the author cannot fix it")

	assert.Empty(t, f.mentions.Rows())
	assert.Empty(t, f.notify.Calls())
	assert.Empty(t, f.agentNotify.Calls())
}

// TestCreate_UnresolvableMentionNamesEverySlugThatFailed: reporting only the
// first would send the author round the loop once per typo.
func TestCreate_UnresolvableMentionNamesEverySlugThatFailed(t *testing.T) {
	f := setupDocumentMentions(t)

	_, err := f.comment(context.Background(), "@ghostone and @ghosttwo and @pavel")

	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Validation["body"], "@ghostone")
	assert.Contains(t, apiErr.Validation["body"], "@ghosttwo")
	assert.NotContains(t, apiErr.Validation["body"], "@pavel",
		"a slug that resolved must not be reported as broken")
}

// TestCreate_BackticksAreTheDocumentedEscapeHatch: the refusal is only
// acceptable because there is a way to write an @ that is not a mention, and the
// error message tells the author what it is.
func TestCreate_BackticksAreTheDocumentedEscapeHatch(t *testing.T) {
	f := setupDocumentMentions(t)

	_, err := f.comment(context.Background(), "run `deploy --as @nobodyhere` first")

	require.NoError(t, err, "an @ inside code is quoted text, not an address")
	assert.Empty(t, f.mentions.Rows())

	_, err = f.comment(context.Background(), "run deploy --as @nobodyhere first")
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Validation["body"], "backticks",
		"the refusal must say how to write an @ that is not a mention")
}

// TestCreate_LookupFailureIsNotReportedAsATypo: a database blip must not tell
// the author their perfectly good mention was misspelt.
func TestCreate_LookupFailureIsNotReportedAsATypo(t *testing.T) {
	f := setupDocumentMentions(t)
	boom := errors.New("agents table unavailable")
	f.agents.errToReturn = boom

	_, err := f.comment(context.Background(), "ping @daedalus")

	require.ErrorIs(t, err, boom)
	var apiErr *apierror.Error
	assert.False(t, errors.As(err, &apiErr), "an infrastructure failure is not a 400 about the body")
}

// TestMentionsDisabled_LeavesATraceInsteadOfRefusing: a service built without
// either lookup cannot tell a real slug from a typo, so it must not refuse — but
// it must not pretend to have delivered anything either.
func TestMentionsDisabled_LeavesATraceInsteadOfRefusing(t *testing.T) {
	base := setupDocumentCommentService(t)

	in := base.createInput()
	in.Body = "ping @daedalus"
	c, err := base.svc.Create(context.Background(), in)

	require.NoError(t, err, "a service that cannot resolve must not refuse what it cannot judge")
	require.NotNil(t, c)
}

// ---------------------------------------------------------------------------
// Delivery — agent and human, separately
// ---------------------------------------------------------------------------

// TestCreate_MentionedAgentIsNotified proves the agent half of the delivery
// contract: the push goes down AgentNotifyService, which is an agent's actual
// channel, and carries what a consumer needs to open the page.
func TestCreate_MentionedAgentIsNotified(t *testing.T) {
	f := setupDocumentMentions(t)

	c, err := f.comment(authoredBy(f.author, domain.ActorTypeUser, "Ann"), "@daedalus please review")
	require.NoError(t, err)

	calls := f.agentNotify.Calls()
	require.Len(t, calls, 1, "the mentioned agent must actually be pushed to")
	got := calls[0]
	assert.Equal(t, DocumentMentionedEvent, got.EventType)
	assert.Equal(t, f.agentID, got.AgentID)
	assert.Equal(t, f.wsID, got.WorkspaceID)
	assert.Equal(t, f.projectID, got.ProjectID)
	assert.Equal(t, "Ann", got.ActorName)
	assert.Equal(t, f.documentID, got.Payload["document_id"], "the payload must name the page to open")
	assert.Equal(t, "Runbook", got.Payload["document_title"])
	assert.Equal(t, "daedalus", got.Payload["mentioned_slug"])
	assert.Equal(t, c.ID, got.Payload["comment_id"])
	assert.Nil(t, got.Task, "there is no task; an invented one would resolve to nothing")
	assert.Equal(t, uuid.Nil, got.TaskID)

	assert.Empty(t, f.notify.Calls(), "an agent mention must not go down a human's channels")

	rows := f.mentions.Rows()
	require.Len(t, rows, 1)
	assert.Equal(t, "agent", rows[0].MentionedKind)
	assert.Equal(t, f.agentID, rows[0].MentionedID)
	assert.Equal(t, "daedalus", rows[0].MentionedSlug)
}

// TestCreate_MentionedUserIsNotified proves the human half: the notification
// service is what reaches the channels a person actually subscribed to, and the
// WebSocket badge is the extra, not the delivery.
func TestCreate_MentionedUserIsNotified(t *testing.T) {
	f := setupDocumentMentions(t)

	c, err := f.comment(authoredBy(f.author, domain.ActorTypeUser, "Ann"), "@pavel does this still hold?")
	require.NoError(t, err)

	calls := f.notify.Calls()
	require.Len(t, calls, 1, "the mentioned person must reach the notification fan-out, not just the live badge")
	got := calls[0]
	assert.Equal(t, DocumentMentionedEvent, got.EventType)
	assert.Equal(t, f.wsID, got.WorkspaceID)
	require.NotNil(t, got.TargetUserID)
	assert.Equal(t, f.userID, *got.TargetUserID,
		"TargetUserID is what keeps the comment body from being fanned out to the whole workspace")
	require.NotNil(t, got.ProjectID)
	assert.Equal(t, f.projectID, *got.ProjectID)
	assert.Nil(t, got.TaskID, "a document mention names no task")
	assert.Equal(t, "Ann mentioned you on: Runbook", got.Title)
	assert.Equal(t, "@pavel does this still hold?", got.Body)
	assert.Equal(t, f.documentID, got.Metadata["document_id"])
	assert.Equal(t, c.ID, got.Metadata["comment_id"])

	assert.Equal(t, []string{"ws:user:" + f.userID.String()}, f.wsPub.Channels())
	assert.Empty(t, f.agentNotify.Calls(), "a human mention must not go down an agent's channel")

	rows := f.mentions.Rows()
	require.Len(t, rows, 1)
	assert.Equal(t, "user", rows[0].MentionedKind)
	assert.Equal(t, f.userID, rows[0].MentionedID)
}

// TestCreate_TitleFallsBackWhenTheActorHasNoName: an unnamed actor must still
// produce a sentence, not "  mentioned you on: …".
func TestCreate_TitleFallsBackWhenTheActorHasNoName(t *testing.T) {
	f := setupDocumentMentions(t)

	_, err := f.comment(context.Background(), "@pavel look")
	require.NoError(t, err)

	calls := f.notify.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "You were mentioned on: Runbook", calls[0].Title)
}

// TestCreate_MixedMentionsReachBothKinds: one comment, two actor types, two
// different delivery mechanisms.
func TestCreate_MixedMentionsReachBothKinds(t *testing.T) {
	f := setupDocumentMentions(t)

	_, err := f.comment(context.Background(), "@daedalus and @pavel — both of you")
	require.NoError(t, err)

	assert.Len(t, f.agentNotify.Calls(), 1)
	assert.Len(t, f.notify.Calls(), 1)
	assert.Len(t, f.mentions.Rows(), 2)
}

// TestCreate_SelfMentionIsNotDelivered: naming yourself is a way of writing, not
// a way of being told — and no row, so it cannot light up your own badge.
func TestCreate_SelfMentionIsNotDelivered(t *testing.T) {
	f := setupDocumentMentions(t)

	in := f.createInput()
	in.AuthorID, in.AuthorType = f.userID, domain.ActorTypeUser
	in.Body = "note to self: @pavel"

	_, err := f.svc.Create(authoredBy(f.userID, domain.ActorTypeUser, "Pavel"), in)
	require.NoError(t, err)

	assert.Empty(t, f.notify.Calls())
	assert.Empty(t, f.wsPub.Channels())
	assert.Empty(t, f.mentions.Rows())
}

// TestCreate_AnAgentMentioningItselfIsNotDelivered: the same rule for the other
// actor type. An id can collide across the two namespaces, so the kind is
// compared as well as the id.
func TestCreate_AnAgentMentioningItselfIsNotDelivered(t *testing.T) {
	f := setupDocumentMentions(t)

	in := f.createInput()
	in.AuthorID, in.AuthorType = f.agentID, domain.ActorTypeAgent
	in.Body = "@daedalus reminding itself"

	_, err := f.svc.Create(authoredBy(f.agentID, domain.ActorTypeAgent, "Daedalus"), in)
	require.NoError(t, err)

	assert.Empty(t, f.agentNotify.Calls())
	assert.Empty(t, f.mentions.Rows())
}

// TestCreate_AUserSharingAnAgentsIdStillGetsNotified: the self-mention skip
// compares the kind too, so a user whose uuid happens to equal the authoring
// agent's is a different principal and must still be told.
func TestCreate_AUserSharingAnAgentsIdStillGetsNotified(t *testing.T) {
	f := setupDocumentMentions(t)
	f.users.AddUser(f.wsID, &domain.User{ID: f.agentID, Username: "twin", Name: "Twin"})

	in := f.createInput()
	in.AuthorID, in.AuthorType = f.agentID, domain.ActorTypeAgent
	in.Body = "@twin over to you"

	_, err := f.svc.Create(authoredBy(f.agentID, domain.ActorTypeAgent, "Daedalus"), in)
	require.NoError(t, err)

	require.Len(t, f.notify.Calls(), 1)
	assert.Len(t, f.mentions.Rows(), 1)
}

// TestCreate_BookkeepingFailureDoesNotFailTheComment: the write already
// succeeded, and answering 500 would tell the author their comment was lost.
func TestCreate_BookkeepingFailureDoesNotFailTheComment(t *testing.T) {
	f := setupDocumentMentions(t)
	f.mentions.FailWith(errors.New("mentions table unavailable"))

	c, err := f.comment(context.Background(), "@pavel look")

	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Len(t, f.notify.Calls(), 1, "the notification still goes out")
}

// TestCreate_LongBodyIsTruncatedForTheNotification: a notification is a pointer
// to a comment, not a copy of it.
func TestCreate_LongBodyIsTruncatedForTheNotification(t *testing.T) {
	f := setupDocumentMentions(t)

	_, err := f.comment(context.Background(), "@pavel "+strings.Repeat("x", 400))
	require.NoError(t, err)

	calls := f.notify.Calls()
	require.Len(t, calls, 1)
	assert.Len(t, calls[0].Body, maxMentionNotificationBody)
}

// TestTruncateRunes_DoesNotSplitACharacter: slicing bytes at the limit turns a
// cut through a multi-byte rune into a replacement character in an email
// subject line.
func TestTruncateRunes_DoesNotSplitACharacter(t *testing.T) {
	assert.Equal(t, "short", truncateRunes("short", 10))
	assert.Equal(t, "абв", truncateRunes("абвгд", 3))
	assert.True(t, len(truncateRunes(strings.Repeat("é", 300), 200)) > 200,
		"the cap counts characters, so 200 two-byte runes are more than 200 bytes")
}

// ---------------------------------------------------------------------------
// Editing
// ---------------------------------------------------------------------------

// TestUpdate_OnlyNewlyAddedMentionsAreNotified: otherwise fixing a typo pings
// everyone the paragraph names, every time.
func TestUpdate_OnlyNewlyAddedMentionsAreNotified(t *testing.T) {
	f := setupDocumentMentions(t)

	c, err := f.comment(context.Background(), "@pavel first pass")
	require.NoError(t, err)
	require.Len(t, f.notify.Calls(), 1)

	_, err = f.svc.Update(context.Background(), c.ID, f.wsID, UpdateDocumentCommentInput{
		Body:       "@pavel first pass, now also @daedalus",
		EditorID:   f.author,
		EditorType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	assert.Len(t, f.notify.Calls(), 1, "@pavel was already there and must not be pinged twice")
	require.Len(t, f.agentNotify.Calls(), 1, "@daedalus is new and must be told")
	assert.Equal(t, "daedalus", f.agentNotify.Calls()[0].Payload["mentioned_slug"])
}

// TestUpdate_UnresolvableMentionIsRefused: the refusal applies to an edit too,
// or the picker's guarantee ends the moment somebody edits.
func TestUpdate_UnresolvableMentionIsRefused(t *testing.T) {
	f := setupDocumentMentions(t)

	c, err := f.comment(context.Background(), "first pass")
	require.NoError(t, err)

	_, err = f.svc.Update(context.Background(), c.ID, f.wsID, UpdateDocumentCommentInput{
		Body:       "actually @ghost should see this",
		EditorID:   f.author,
		EditorType: domain.ActorTypeUser,
	})

	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Validation["body"], "@ghost")

	fresh, getErr := f.comments.GetByID(context.Background(), c.ID)
	require.NoError(t, getErr)
	assert.Equal(t, "first pass", fresh.Body, "a refused edit must not have been applied")
}

// TestUpdate_AnAlreadyPresentSlugThatStoppedResolvingDoesNotBlockTheEdit: a
// member who left would otherwise make every later edit of an old comment a 400
// about words the editor did not write.
func TestUpdate_AnAlreadyPresentSlugThatStoppedResolvingDoesNotBlockTheEdit(t *testing.T) {
	f := setupDocumentMentions(t)

	c, err := f.comment(context.Background(), "@pavel please check")
	require.NoError(t, err)

	f.users.byUsername = map[string]*domain.User{} // Pavel leaves the workspace.

	_, err = f.svc.Update(context.Background(), c.ID, f.wsID, UpdateDocumentCommentInput{
		Body:       "@pavel please check the second paragraph",
		EditorID:   f.author,
		EditorType: domain.ActorTypeUser,
	})

	require.NoError(t, err)
}

// TestUpdate_NoNewMentionsSkipsTheDocumentRead: documentFor is a database call,
// and an edit that added nothing should not pay for one.
func TestUpdate_NoNewMentionsSkipsTheDocumentRead(t *testing.T) {
	f := setupDocumentMentions(t)

	c, err := f.comment(context.Background(), "no mentions here")
	require.NoError(t, err)

	f.docs.errToReturn = errors.New("documents unavailable")

	_, err = f.svc.Update(context.Background(), c.ID, f.wsID, UpdateDocumentCommentInput{
		Body:       "still no mentions here",
		EditorID:   f.author,
		EditorType: domain.ActorTypeUser,
	})

	require.NoError(t, err)
}

// TestDocumentFor_FallsBackRatherThanCrashing: the comment is already written by
// the time the notifier needs the page, so an unreadable document degrades to a
// placeholder title instead of a nil dereference.
func TestDocumentFor_FallsBackRatherThanCrashing(t *testing.T) {
	f := setupDocumentMentions(t)

	c, err := f.comment(context.Background(), "no mentions yet")
	require.NoError(t, err)

	f.docs.errToReturn = errors.New("documents unavailable")

	_, err = f.svc.Update(context.Background(), c.ID, f.wsID, UpdateDocumentCommentInput{
		Body:       "now with @pavel",
		EditorID:   f.author,
		EditorType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	calls := f.notify.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "You were mentioned on: a document", calls[0].Title)
}

// ---------------------------------------------------------------------------
// Read side
// ---------------------------------------------------------------------------

func TestDocumentMentionService_PassesThroughToTheRepository(t *testing.T) {
	repo := NewMockDocumentCommentMentionRepository()
	svc := NewDocumentMentionService(repo)
	ctx := context.Background()

	commentID, recipient := uuid.New(), uuid.New()
	require.NoError(t, repo.InsertBatch(ctx, []domain.DocumentCommentMention{{
		CommentID: commentID, MentionedID: recipient, MentionedKind: "user", MentionedSlug: "pavel",
	}}))

	views, err := svc.List(ctx, recipient, "user", repository.MentionFilter{})
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, "pavel", views[0].MentionedSlug)

	count, err := svc.CountUnseen(ctx, recipient, "user")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	require.NoError(t, svc.MarkSeen(ctx, commentID, recipient))
	assert.Equal(t, [][2]uuid.UUID{{commentID, recipient}}, repo.Seen())
}

func TestDocumentMentionService_SurfacesRepositoryErrors(t *testing.T) {
	boom := errors.New("db down")
	svc := NewDocumentMentionService(NewMockDocumentCommentMentionRepository().FailWith(boom))
	ctx := context.Background()

	_, err := svc.List(ctx, uuid.New(), "user", repository.MentionFilter{})
	assert.ErrorIs(t, err, boom)

	_, err = svc.CountUnseen(ctx, uuid.New(), "user")
	assert.ErrorIs(t, err, boom)

	assert.ErrorIs(t, svc.MarkSeen(ctx, uuid.New(), uuid.New()), boom)
}

// ---------------------------------------------------------------------------
// The optional dependencies, absent
// ---------------------------------------------------------------------------

// TestDelivery_MissingChannelsDegradeRatherThanPanic: every notification
// collaborator is an option, and a deployment that has not wired one must lose
// that channel rather than the comment. The mention row is still written, so the
// inbox has it even when nothing could be pushed.
func TestDelivery_MissingChannelsDegradeRatherThanPanic(t *testing.T) {
	base := setupDocumentCommentService(t)

	agents := NewMockAgentService()
	users := NewMockUserRepository()
	mentionRepo := NewMockDocumentCommentMentionRepository()
	agentID, userID := uuid.New(), uuid.New()
	agents.AddAgent(base.wsID, &domain.Agent{ID: agentID, Slug: "daedalus"})
	users.AddUser(base.wsID, &domain.User{ID: userID, Username: "pavel"})

	// Resolution wired, delivery not: no agent notifier, no notification
	// service, no WebSocket publisher.
	svc := NewDocumentCommentService(base.comments, base.docs, base.docs,
		WithDocumentCommentAgentService(agents),
		WithDocumentCommentUserRepo(users),
		WithDocumentCommentMentionRepo(mentionRepo),
	)

	in := base.createInput()
	in.Body = "@daedalus and @pavel"

	c, err := svc.Create(context.Background(), in)

	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Len(t, mentionRepo.Rows(), 2, "the mention is still recorded for the inbox")
}

// TestDelivery_AFailedBadgeDoesNotStopTheNotification: the WebSocket badge is
// the extra, not the delivery. Losing it must not cost the recipient the email
// and the bell as well.
func TestDelivery_AFailedBadgeDoesNotStopTheNotification(t *testing.T) {
	f := setupDocumentMentions(t)
	f.wsPub.err = errors.New("redis unavailable")

	_, err := f.comment(context.Background(), "@pavel look")

	require.NoError(t, err)
	assert.Len(t, f.notify.Calls(), 1, "the subscribed channels still get it")
}

// TestCreate_AUserLookupFailureIsNotReportedAsATypo is the user-side half of
// TestCreate_LookupFailureIsNotReportedAsATypo: agents are checked first, so
// reaching the user repository at all needs a slug no agent claims.
func TestCreate_AUserLookupFailureIsNotReportedAsATypo(t *testing.T) {
	f := setupDocumentMentions(t)
	boom := errors.New("users table unavailable")
	f.users.errToReturn = boom

	_, err := f.comment(context.Background(), "ping @pavel")

	require.ErrorIs(t, err, boom)
	var apiErr *apierror.Error
	assert.False(t, errors.As(err, &apiErr), "an infrastructure failure is not a 400 about the body")
}

// TestTruncateRunes_CountsCharactersNotBytes: a body under the limit in
// characters but over it in bytes is not truncated, which is the branch a
// byte-only length check would skip.
func TestTruncateRunes_CountsCharactersNotBytes(t *testing.T) {
	cyrillic := strings.Repeat("я", 150) // 150 runes, 300 bytes
	assert.Equal(t, cyrillic, truncateRunes(cyrillic, 200))
}
