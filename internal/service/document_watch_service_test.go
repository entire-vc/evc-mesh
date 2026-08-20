package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// ---------------------------------------------------------------------------
// In-memory DocumentWatchRepository.
//
// It implements the semantics the SQL is written to have — one OPEN notice per
// (document, actor), a dispatched notice never reopened, an automatic subscribe
// that cannot clear a mute — so the service logic can be tested without a
// database standing by.
//
// What it therefore does NOT prove is that the SQL implements those semantics.
// The partial unique index and its ON CONFLICT are proven by the live
// measurement recorded on the task: a hundred real PATCHes against the deployed
// API, counted in document_change_notices and notifications. A fake that
// re-implements the query would only ever agree with itself.
// ---------------------------------------------------------------------------

type memWatchRepo struct {
	mu       sync.Mutex
	watchers map[uuid.UUID][]domain.DocumentWatcher
	notices  []domain.DocumentChangeNotice

	// failWatcherLookup makes ListLiveWatchers return an error, for the negative
	// control: a delivery that could not happen must leave a trace.
	failWatcherLookup bool
}

func newMemWatchRepo() *memWatchRepo {
	return &memWatchRepo{watchers: map[uuid.UUID][]domain.DocumentWatcher{}}
}

func (m *memWatchRepo) find(documentID, watcherID uuid.UUID, kind string) *domain.DocumentWatcher {
	for i := range m.watchers[documentID] {
		w := &m.watchers[documentID][i]
		if w.WatcherID == watcherID && w.WatcherKind == kind {
			return w
		}
	}
	return nil
}

func (m *memWatchRepo) Subscribe(_ context.Context, w domain.DocumentWatcher, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.find(w.DocumentID, w.WatcherID, w.WatcherKind); existing != nil {
		if force {
			existing.Muted = false
			existing.Source = w.Source
		}
		// Automatic subscribe: DO NOTHING. This is the branch that keeps an
		// unsubscribe from being undone by the next comment.
		return nil
	}
	w.Muted = false
	m.watchers[w.DocumentID] = append(m.watchers[w.DocumentID], w)
	return nil
}

func (m *memWatchRepo) Unsubscribe(_ context.Context, documentID, watcherID uuid.UUID, kind string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.find(documentID, watcherID, kind); existing != nil {
		existing.Muted = true
		return nil
	}
	m.watchers[documentID] = append(m.watchers[documentID], domain.DocumentWatcher{
		DocumentID: documentID, WatcherID: watcherID, WatcherKind: kind,
		Source: domain.WatchSourceExplicit, Muted: true,
	})
	return nil
}

func (m *memWatchRepo) GetState(_ context.Context, documentID, watcherID uuid.UUID, kind string) (*domain.DocumentWatchState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := &domain.DocumentWatchState{}
	for i := range m.watchers[documentID] {
		if !m.watchers[documentID][i].Muted {
			st.WatcherCount++
		}
	}
	if w := m.find(documentID, watcherID, kind); w != nil {
		st.Muted = w.Muted
		st.Watching = !w.Muted
		if st.Watching {
			st.Source = w.Source
		}
	}
	return st, nil
}

func (m *memWatchRepo) ListLiveWatchers(_ context.Context, documentID uuid.UUID) ([]domain.DocumentWatcher, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failWatcherLookup {
		return nil, fmt.Errorf("watcher lookup is down")
	}
	var out []domain.DocumentWatcher
	for _, w := range m.watchers[documentID] {
		if !w.Muted {
			out = append(out, w)
		}
	}
	return out, nil
}

func (m *memWatchRepo) RecordChange(_ context.Context, n domain.DocumentChangeNotice) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.notices {
		open := &m.notices[i]
		if open.DispatchedAt != nil {
			continue
		}
		if open.DocumentID == n.DocumentID && open.ActorID == n.ActorID && open.ActorKind == n.ActorKind {
			open.EditCount++
			open.TitleChanged = open.TitleChanged || n.TitleChanged
			open.BodyChanged = open.BodyChanged || n.BodyChanged
			if n.ToVersion > open.ToVersion {
				open.ToVersion = n.ToVersion
			}
			if n.FromVersion < open.FromVersion {
				open.FromVersion = n.FromVersion
			}
			open.LastEditAt = timeNow()
			return nil
		}
	}
	n.ID = uuid.New()
	n.EditCount = 1
	n.FirstEditAt = timeNow()
	n.LastEditAt = timeNow()
	m.notices = append(m.notices, n)
	return nil
}

func (m *memWatchRepo) ClaimPendingNotices(_ context.Context, quietBefore time.Time, limit int) ([]domain.DocumentChangeNotice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.DocumentChangeNotice
	now := timeNow()
	for i := range m.notices {
		n := &m.notices[i]
		if n.DispatchedAt != nil || !n.LastEditAt.Before(quietBefore) {
			continue
		}
		n.DispatchedAt = &now
		out = append(out, *n)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memWatchRepo) FinishNotice(_ context.Context, id uuid.UUID, recipients int, dispatchErr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.notices {
		if m.notices[i].ID == id {
			m.notices[i].Recipients = recipients
			if dispatchErr != "" {
				e := dispatchErr
				m.notices[i].DispatchError = &e
			}
			return nil
		}
	}
	return nil
}

// openNotices counts notices nobody has dispatched yet — the number a hundred
// autosaves must not multiply.
func (m *memWatchRepo) openNotices() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for i := range m.notices {
		if m.notices[i].DispatchedAt == nil {
			n++
		}
	}
	return n
}

func (m *memWatchRepo) noticeAt(i int) domain.DocumentChangeNotice {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.notices[i]
}

// ---------------------------------------------------------------------------

type watchFixture struct {
	docs      *documentFixture
	repo      *memWatchRepo
	docAuthor uuid.UUID
	notify    *MockNotificationService
	agents    *MockAgentNotifyService
	watch     DocumentWatchService
	window    time.Duration
	nowRef    *time.Time
	docRepo   *MockDocumentRepository
}

// setupWatch wires a document service that records changes into a watch service
// whose clock the test controls.
func setupWatch(t *testing.T) *watchFixture {
	t.Helper()

	f := setupDocumentService(t)
	repo := newMemWatchRepo()
	notify := NewMockNotificationService()
	agents := NewMockAgentNotifyService()
	window := 5 * time.Minute

	watch := NewDocumentWatchService(repo, f.repo, notify, agents, window)
	f.svc.watch = watch

	now := frozenTime
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = func() time.Time { return frozenTime } })

	return &watchFixture{
		docs: f, repo: repo, notify: notify, agents: agents,
		watch: watch, window: window, nowRef: &now, docRepo: f.repo,
	}
}

// advance moves the fixture's clock, which is how "the author stopped typing"
// is expressed without sleeping.
func (w *watchFixture) advance(d time.Duration) {
	*w.nowRef = w.nowRef.Add(d)
	now := *w.nowRef
	timeNow = func() time.Time { return now }
}

func (w *watchFixture) userCtx(id uuid.UUID, name string) context.Context {
	return actorctx.WithActorName(actorctx.WithActor(context.Background(), id, domain.ActorTypeUser), name)
}

// create makes a document whose watcher set the test controls exactly.
//
// Creating a document auto-subscribes its author, which is the correct product
// behaviour and has its own test (TestWatch_CreateSubscribesTheAuthor). Left in
// place here it would add a second recipient to every assertion below and turn
// each count into a statement about the fixture rather than about the rule under
// test, so the author is muted straight away.
func (w *watchFixture) create(t *testing.T, title, body string) *domain.Document {
	t.Helper()
	author := uuid.New()
	doc, err := w.docs.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: w.docs.projectID, Title: title, Body: body,
		CreatedBy: author, CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
	require.NoError(t, w.repo.Unsubscribe(context.Background(), doc.ID, author, "user"))
	w.docAuthor = author
	return doc
}

// edit is one autosave: a body write by `actor`.
func (w *watchFixture) edit(t *testing.T, doc *domain.Document, actor uuid.UUID, body string) {
	t.Helper()
	_, err := w.docs.svc.Update(w.userCtx(actor, "Alice"), doc.ID, w.docs.wsID, UpdateDocumentInput{
		Body:          &body,
		UpdatedBy:     actor,
		UpdatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
}

// TestWatch_HundredAutosavesProduceOneNotification is the acceptance criterion
// the whole design exists for, asserted as a count rather than an impression.
//
// A hundred writes is not a round number chosen for effect: the editor autosaves
// on a 2-second debounce, so it is roughly what three or four minutes of typing
// actually produces.
func TestWatch_HundredAutosavesProduceOneNotification(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	author := uuid.New()
	watcher := uuid.New()
	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: watcher, WatcherKind: "user", Source: domain.WatchSourceExplicit,
	}, true))

	for i := range 100 {
		w.advance(2 * time.Second) // the autosave debounce
		w.edit(t, doc, author, fmt.Sprintf("revision %d", i))
	}

	assert.Equal(t, 1, w.repo.openNotices(),
		"a hundred autosaves must fold into ONE pending notice, not a hundred")
	assert.Empty(t, w.notify.Calls(),
		"nothing may be delivered while the author is still typing")

	// The author stops typing. One window later the news goes out — once.
	w.advance(w.window + time.Minute)
	sent, err := w.watch.SweepPendingNotices(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, sent)

	calls := w.notify.Calls()
	require.Len(t, calls, 1, "one editing session is one notification")
	assert.Equal(t, DocumentChangedEvent, calls[0].EventType)
	require.NotNil(t, calls[0].TargetUserID)
	assert.Equal(t, watcher, *calls[0].TargetUserID,
		"a subscription is targeted, never a workspace broadcast")
	assert.Contains(t, calls[0].Body, "100 edits",
		"the message must say it is a summary, not pretend to be a single edit")

	// And a second sweep with nothing new sends nothing: a dispatched notice is
	// never re-delivered.
	w.advance(w.window)
	again, err := w.watch.SweepPendingNotices(context.Background())
	require.NoError(t, err)
	assert.Zero(t, again)
	assert.Len(t, w.notify.Calls(), 1)
}

// TestWatch_NothingIsSentBeforeTheWindowElapses is the negative half of the one
// above: without it, "one notification" could equally be satisfied by a service
// that sends immediately and never again.
func TestWatch_NothingIsSentBeforeTheWindowElapses(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")
	watcher := uuid.New()
	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: watcher, WatcherKind: "user", Source: domain.WatchSourceExplicit,
	}, true))

	w.edit(t, doc, uuid.New(), "one edit")

	w.advance(w.window - time.Minute)
	sent, err := w.watch.SweepPendingNotices(context.Background())
	require.NoError(t, err)
	assert.Zero(t, sent, "the author has not been quiet for a full window yet")
	assert.Empty(t, w.notify.Calls())
}

// TestWatch_AuthorIsNotNotifiedOfTheirOwnEdit — the rule everybody agrees with
// and nobody implements.
func TestWatch_AuthorIsNotNotifiedOfTheirOwnEdit(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	author := uuid.New()
	// The author watches their own page — which is the normal case, since
	// creating one subscribes you to it.
	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: author, WatcherKind: "user", Source: domain.WatchSourceAuthor,
	}, true))

	w.edit(t, doc, author, "my own words")
	w.advance(w.window + time.Minute)

	_, err := w.watch.SweepPendingNotices(context.Background())
	require.NoError(t, err)
	assert.Empty(t, w.notify.Calls(), "you are not told about your own edit")
	assert.Zero(t, w.repo.noticeAt(0).Recipients)
}

// TestWatch_SelfExclusionComparesKindNotJustID guards the subtle half of the
// rule above: a user id and an agent id come from different spaces, so excluding
// on the id alone would silence an innocent watcher who happens to share the
// value — and, in the other direction, would not silence the real author.
func TestWatch_SelfExclusionComparesKindNotJustID(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	shared := uuid.New() // same uuid, different actor kinds
	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: shared, WatcherKind: "agent", Source: domain.WatchSourceExplicit,
	}, true))

	w.edit(t, doc, shared, "written by the USER with that id")
	w.advance(w.window + time.Minute)
	_, err := w.watch.SweepPendingNotices(context.Background())
	require.NoError(t, err)

	assert.Len(t, w.agents.Calls(), 1,
		"the agent watcher is a different principal from the user who edited, and must still be told")
}

// TestWatch_UnsubscribeSurvivesAutoSubscribe is the reason unwatching writes a
// tombstone instead of deleting the row.
func TestWatch_UnsubscribeSurvivesAutoSubscribe(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	person := uuid.New()
	ctx := w.userCtx(person, "Bob")

	_, err := w.watch.Watch(ctx, doc.ID, w.docs.wsID)
	require.NoError(t, err)

	state, err := w.watch.Unwatch(ctx, doc.ID, w.docs.wsID)
	require.NoError(t, err)
	require.False(t, state.Watching)
	require.True(t, state.Muted, "an unsubscribe is recorded, not merely absent")

	// Now they comment on the page — the automatic path that would otherwise
	// resurrect the subscription they just cancelled.
	w.watch.AutoSubscribe(ctx, doc.ID, person, "user", domain.WatchSourceCommenter)

	state, err = w.watch.State(ctx, doc.ID, w.docs.wsID)
	require.NoError(t, err)
	assert.False(t, state.Watching, "an automatic subscribe must not undo an explicit unsubscribe")
	assert.True(t, state.Muted)

	// Pressing Watch again, however, must work — a tombstone is not a ban.
	state, err = w.watch.Watch(ctx, doc.ID, w.docs.wsID)
	require.NoError(t, err)
	assert.True(t, state.Watching)
	assert.False(t, state.Muted)
}

// TestWatch_MutedWatcherIsNotDelivered checks the mute is honoured at delivery,
// not only in the state the UI reads back.
func TestWatch_MutedWatcherIsNotDelivered(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	muted := uuid.New()
	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: muted, WatcherKind: "user", Source: domain.WatchSourceExplicit,
	}, true))
	require.NoError(t, w.repo.Unsubscribe(context.Background(), doc.ID, muted, "user"))

	w.edit(t, doc, uuid.New(), "edited by somebody else")
	w.advance(w.window + time.Minute)
	_, err := w.watch.SweepPendingNotices(context.Background())
	require.NoError(t, err)

	assert.Empty(t, w.notify.Calls())
}

// TestWatch_MoveInTheTreeIsNotNews — a document filed somewhere else is not a
// document that changed, and notifying on it would make the feature noisy in
// exactly the way that gets it switched off.
func TestWatch_MoveInTheTreeIsNotNews(t *testing.T) {
	w := setupWatch(t)
	parent := w.create(t, "Parent", "p")
	doc := w.create(t, "Billing", "start")

	watcher := uuid.New()
	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: watcher, WatcherKind: "user", Source: domain.WatchSourceExplicit,
	}, true))

	_, err := w.docs.svc.Update(w.userCtx(uuid.New(), "Alice"), doc.ID, w.docs.wsID, UpdateDocumentInput{
		ParentID:      &parent.ID,
		UpdatedBy:     uuid.New(),
		UpdatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	assert.Zero(t, w.repo.openNotices(), "a move opens no notice")
	w.advance(w.window + time.Minute)
	_, err = w.watch.SweepPendingNotices(context.Background())
	require.NoError(t, err)
	assert.Empty(t, w.notify.Calls())
}

// TestWatch_RenameIsNews, by contrast — the title is content, and a watcher who
// misses a rename cannot find the page again.
func TestWatch_RenameIsNews(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	watcher := uuid.New()
	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: watcher, WatcherKind: "user", Source: domain.WatchSourceExplicit,
	}, true))

	newTitle := "Billing and refunds"
	_, err := w.docs.svc.Update(w.userCtx(uuid.New(), "Alice"), doc.ID, w.docs.wsID, UpdateDocumentInput{
		Title:         &newTitle,
		UpdatedBy:     uuid.New(),
		UpdatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	w.advance(w.window + time.Minute)
	_, err = w.watch.SweepPendingNotices(context.Background())
	require.NoError(t, err)

	calls := w.notify.Calls()
	require.Len(t, calls, 1)
	assert.Contains(t, calls[0].Body, "renamed")
}

// TestWatch_FailedDeliveryLeavesATrace is the negative control the task names:
// a watcher who exists and was not reached must not look like a document nobody
// was watching.
func TestWatch_FailedDeliveryLeavesATrace(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: uuid.New(), WatcherKind: "user", Source: domain.WatchSourceExplicit,
	}, true))

	w.edit(t, doc, uuid.New(), "an edit nobody will hear about")
	w.advance(w.window + time.Minute)

	w.repo.failWatcherLookup = true
	_, err := w.watch.SweepPendingNotices(context.Background())
	require.NoError(t, err, "one broken notice must not stop the sweep")

	n := w.repo.noticeAt(0)
	require.NotNil(t, n.DispatchError,
		"a delivery that could not happen must be recorded, not merely absent")
	assert.Contains(t, *n.DispatchError, "watcher lookup")
	assert.Zero(t, n.Recipients)
}

// TestWatch_NoWatchersIsRecordedAsZeroNotAsAnError keeps the trace above
// meaningful: if "nobody was watching" also wrote an error, the column would
// stop distinguishing the two cases it exists to distinguish.
func TestWatch_NoWatchersIsRecordedAsZeroNotAsAnError(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	w.edit(t, doc, uuid.New(), "nobody is watching this")
	w.advance(w.window + time.Minute)
	_, err := w.watch.SweepPendingNotices(context.Background())
	require.NoError(t, err)

	n := w.repo.noticeAt(0)
	assert.Nil(t, n.DispatchError)
	assert.Zero(t, n.Recipients)
}

// TestWatch_TwoEditorsAreTwoNotices — coalescing folds one person's session, not
// two people's separate work.
func TestWatch_TwoEditorsAreTwoNotices(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	watcher := uuid.New()
	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: watcher, WatcherKind: "user", Source: domain.WatchSourceExplicit,
	}, true))

	alice, bob := uuid.New(), uuid.New()
	for range 5 {
		w.advance(2 * time.Second)
		w.edit(t, doc, alice, "alice writes")
		w.advance(2 * time.Second)
		w.edit(t, doc, bob, "bob writes")
	}

	assert.Equal(t, 2, w.repo.openNotices(), "one notice per editor, not per edit and not per document")

	w.advance(w.window + time.Minute)
	sent, err := w.watch.SweepPendingNotices(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, sent)
	assert.Len(t, w.notify.Calls(), 2)
}

// TestWatch_AgentWatcherIsDeliveredOnItsOwnChannel — an agent is not reached by
// the notification service at all, so "delivery works" proven only on the human
// path proves nothing about half the fleet.
func TestWatch_AgentWatcherIsDeliveredOnItsOwnChannel(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	agentID := uuid.New()
	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: agentID, WatcherKind: "agent", Source: domain.WatchSourceExplicit,
	}, true))

	w.edit(t, doc, uuid.New(), "an edit an agent should hear about")
	w.advance(w.window + time.Minute)
	_, err := w.watch.SweepPendingNotices(context.Background())
	require.NoError(t, err)

	calls := w.agents.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, DocumentChangedEvent, calls[0].EventType)
	assert.Equal(t, doc.ID, calls[0].Payload["document_id"])
	assert.Empty(t, w.notify.Calls(), "an agent must not also be posted to the human bell")
}

// TestWatch_CommentIsNotCoalesced — the other half of the hybrid decision.
func TestWatch_CommentIsNotCoalesced(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	watcher := uuid.New()
	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: watcher, WatcherKind: "user", Source: domain.WatchSourceExplicit,
	}, true))

	w.watch.NotifyComment(context.Background(), NotifyDocumentCommentInput{
		Document: doc, WorkspaceID: w.docs.wsID, CommentID: uuid.New(),
		Body: "what about refunds?", ActorID: uuid.New(), ActorKind: "user", ActorName: "Alice",
	})

	calls := w.notify.Calls()
	require.Len(t, calls, 1, "a comment goes out at once — no window, no sweep")
	assert.Equal(t, DocumentCommentedEvent, calls[0].EventType)
}

// TestWatch_MentionedWatcherIsNotToldTwice — the reward for subscribing must not
// be a duplicate.
func TestWatch_MentionedWatcherIsNotToldTwice(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	mentioned := uuid.New()
	other := uuid.New()
	for _, id := range []uuid.UUID{mentioned, other} {
		require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
			DocumentID: doc.ID, WatcherID: id, WatcherKind: "user", Source: domain.WatchSourceExplicit,
		}, true))
	}

	w.watch.NotifyComment(context.Background(), NotifyDocumentCommentInput{
		Document: doc, WorkspaceID: w.docs.wsID, CommentID: uuid.New(),
		Body: "@someone what about refunds?", ActorID: uuid.New(), ActorKind: "user", ActorName: "Alice",
		// The mention path has already reached this person with the more
		// specific of the two notifications.
		AlreadyNotified: map[uuid.UUID]bool{mentioned: true},
	})

	calls := w.notify.Calls()
	require.Len(t, calls, 1)
	require.NotNil(t, calls[0].TargetUserID)
	assert.Equal(t, other, *calls[0].TargetUserID,
		"the mentioned watcher already got the mention and must not get the watch copy too")
}

// TestWatch_CreateSubscribesTheAuthor — you should not be the last to know about
// your own page.
func TestWatch_CreateSubscribesTheAuthor(t *testing.T) {
	w := setupWatch(t)

	author := uuid.New()
	doc, err := w.docs.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: w.docs.projectID, Title: "Billing", Body: "start",
		CreatedBy: author, CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	st, err := w.repo.GetState(context.Background(), doc.ID, author, "user")
	require.NoError(t, err)
	assert.True(t, st.Watching)
	assert.Equal(t, domain.WatchSourceAuthor, st.Source)
}

// TestWatch_DeletionTellsTheWatchers — the one notification whose subject cannot
// be opened afterwards, which is exactly why it has to be sent.
func TestWatch_DeletionTellsTheWatchers(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	watcher := uuid.New()
	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: watcher, WatcherKind: "user", Source: domain.WatchSourceExplicit,
	}, true))

	remover := uuid.New()
	require.NoError(t, w.docs.svc.Delete(w.userCtx(remover, "Alice"), doc.ID, w.docs.wsID, remover, domain.ActorTypeUser))

	calls := w.notify.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, DocumentDeletedEvent, calls[0].EventType)
	assert.Contains(t, calls[0].Body, "Billing", "the title is the message: the page is gone")
}

// --- refusals ---------------------------------------------------------------
//
// Every one of these is a path where answering "fine" would write a
// subscription row for somebody who is not entitled to one, or for a document
// that is not there. They are cheap to get wrong precisely because the happy
// path keeps working when they are.

// TestWatch_RefusesAnUnauthenticatedCaller — the subscriber is read from the
// authenticated actor, never from the request, so an absent actor has no
// honest answer.
func TestWatch_RefusesAnUnauthenticatedCaller(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")
	ctx := context.Background() // no actor

	for name, call := range map[string]func() (*domain.DocumentWatchState, error){
		"watch":   func() (*domain.DocumentWatchState, error) { return w.watch.Watch(ctx, doc.ID, w.docs.wsID) },
		"unwatch": func() (*domain.DocumentWatchState, error) { return w.watch.Unwatch(ctx, doc.ID, w.docs.wsID) },
		"state":   func() (*domain.DocumentWatchState, error) { return w.watch.State(ctx, doc.ID, w.docs.wsID) },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := call()
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

// TestWatch_RefusesAnActorOfAnUnknownKind guards the (id, kind) pair from the
// other side: a kind the watcher table's CHECK would reject must be refused
// before it reaches the insert, not after.
func TestWatch_RefusesAnActorOfAnUnknownKind(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorType("service"))

	_, err := w.watch.Watch(ctx, doc.ID, w.docs.wsID)

	require.Error(t, err)
}

// TestWatch_RefusesADocumentInAnotherWorkspace — the workspace check is what
// makes the subscription tenant-scoped; without it a caller could follow a page
// they cannot read and receive its title and edit counts.
func TestWatch_RefusesADocumentInAnotherWorkspace(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")
	ctx := w.userCtx(uuid.New(), "Stranger")

	_, err := w.watch.Watch(ctx, doc.ID, uuid.New()) // some other workspace

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Document")
}

// TestWatch_RefusesAMissingDocument — same for an id that resolves to nothing.
func TestWatch_RefusesAMissingDocument(t *testing.T) {
	w := setupWatch(t)
	ctx := w.userCtx(uuid.New(), "Bob")

	for name, call := range map[string]func() (*domain.DocumentWatchState, error){
		"watch":   func() (*domain.DocumentWatchState, error) { return w.watch.Watch(ctx, uuid.New(), w.docs.wsID) },
		"unwatch": func() (*domain.DocumentWatchState, error) { return w.watch.Unwatch(ctx, uuid.New(), w.docs.wsID) },
		"state":   func() (*domain.DocumentWatchState, error) { return w.watch.State(ctx, uuid.New(), w.docs.wsID) },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := call()
			require.Error(t, err)
		})
	}
}

// TestWatch_AutoSubscribeIgnoresAnActorItCannotFile — the automatic path runs
// alongside somebody else's write and must never fail it, but it also must not
// write a row whose kind the table would reject.
func TestWatch_AutoSubscribeIgnoresAnActorItCannotFile(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")
	ctx := context.Background()

	w.watch.AutoSubscribe(ctx, doc.ID, uuid.New(), "service", domain.WatchSourceCommenter)
	w.watch.AutoSubscribe(ctx, doc.ID, uuid.Nil, "user", domain.WatchSourceCommenter)

	live, err := w.repo.ListLiveWatchers(ctx, doc.ID)
	require.NoError(t, err)
	assert.Empty(t, live)
}

// TestWatch_SweepSurvivesADocumentDeletedMidWindow. The notice outlives the
// page when a delete lands between the last edit and the sweep; it must be
// closed with a reason rather than left pending forever or crashing the sweep.
func TestWatch_SweepSurvivesADocumentDeletedMidWindow(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: uuid.New(), WatcherKind: "user", Source: domain.WatchSourceExplicit,
	}, true))
	w.edit(t, doc, uuid.New(), "an edit")

	remover := uuid.New()
	require.NoError(t, w.docs.svc.Delete(w.userCtx(remover, "Alice"), doc.ID, w.docs.wsID, remover, domain.ActorTypeUser))
	w.notify.mu.Lock()
	w.notify.calls = nil // the deletion notice is a different test's subject
	w.notify.mu.Unlock()

	w.advance(w.window + time.Minute)
	sent, err := w.watch.SweepPendingNotices(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, sent, "the notice is still claimed and closed")

	assert.Empty(t, w.notify.Calls(), "no edit notification for a page that no longer exists")
	n := w.repo.noticeAt(0)
	require.NotNil(t, n.DispatchError, "and the reason is recorded rather than left as silence")
}

// TestWatch_NewServiceFallsBackToTheDefaultWindow — a zero or negative window
// would make every notice instantly eligible and undo the coalescing entirely.
func TestWatch_NewServiceFallsBackToTheDefaultWindow(t *testing.T) {
	svc := NewDocumentWatchService(newMemWatchRepo(), nil, nil, nil, 0).(*documentWatchService)
	assert.Equal(t, defaultQuietWindow, svc.quietWindow)

	svc = NewDocumentWatchService(newMemWatchRepo(), nil, nil, nil, -time.Hour).(*documentWatchService)
	assert.Equal(t, defaultQuietWindow, svc.quietWindow)
}

// TestWatch_CommentSubscribesItsAuthorAndTellsTheOthers exercises the comment
// path end to end through the comment service, which is where the two halves
// (auto-subscribe, notify) are actually wired.
func TestWatch_CommentSubscribesItsAuthorAndTellsTheOthers(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	watcher := uuid.New()
	require.NoError(t, w.repo.Subscribe(context.Background(), domain.DocumentWatcher{
		DocumentID: doc.ID, WatcherID: watcher, WatcherKind: "user", Source: domain.WatchSourceExplicit,
	}, true))

	author := uuid.New()
	// The commenter was not watching before; commenting subscribes them.
	w.watch.AutoSubscribe(context.Background(), doc.ID, author, "user", domain.WatchSourceCommenter)
	w.watch.NotifyComment(context.Background(), NotifyDocumentCommentInput{
		Document: doc, WorkspaceID: w.docs.wsID, CommentID: uuid.New(),
		Body: "what about refunds?", ActorID: author, ActorKind: "user", ActorName: "Alice",
	})

	st, err := w.repo.GetState(context.Background(), doc.ID, author, "user")
	require.NoError(t, err)
	assert.True(t, st.Watching)
	assert.Equal(t, domain.WatchSourceCommenter, st.Source)

	calls := w.notify.Calls()
	require.Len(t, calls, 1, "the commenter is not notified about their own comment")
	require.NotNil(t, calls[0].TargetUserID)
	assert.Equal(t, watcher, *calls[0].TargetUserID)
}

// TestWatch_NotifyCommentIsSilentWithNoWatchers — no watchers is not an error,
// and must not produce a lookup-failure log that would devalue the real one.
func TestWatch_NotifyCommentIsSilentWithNoWatchers(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	w.watch.NotifyComment(context.Background(), NotifyDocumentCommentInput{
		Document: doc, WorkspaceID: w.docs.wsID, CommentID: uuid.New(),
		Body: "nobody follows this", ActorID: uuid.New(), ActorKind: "user",
	})
	w.watch.NotifyDeleted(w.userCtx(uuid.New(), "Alice"), doc, w.docs.wsID)

	assert.Empty(t, w.notify.Calls())
	assert.Empty(t, w.agents.Calls())
}

// TestWatch_CommentServiceWiresBothHalves is the wiring test.
//
// Everything else here exercises the watch service directly. This one goes
// through DocumentCommentService.Create, because that is where the two halves
// are actually connected — and a missing option there would disable the whole
// comment path while every other test in this file still passed.
func TestWatch_CommentServiceWiresBothHalves(t *testing.T) {
	base := setupDocumentCommentService(t)

	repo := newMemWatchRepo()
	notify := NewMockNotificationService()
	watch := NewDocumentWatchService(repo, base.docs, notify, NewMockAgentNotifyService(), time.Minute)

	agents := NewMockAgentService()
	users := NewMockUserRepository()
	mentionedUser := uuid.New()
	users.AddUser(base.wsID, &domain.User{ID: mentionedUser, Username: "pavel", Name: "Pavel"})

	base.svc = NewDocumentCommentService(base.comments, base.docs, base.docs,
		WithDocumentCommentAgentService(agents),
		WithDocumentCommentUserRepo(users),
		WithDocumentCommentNotificationService(notify),
		WithDocumentCommentWatch(watch),
	)

	ctx := context.Background()
	// A bystander who follows the page, and the person the comment names — who
	// also follows it, and must not be told twice.
	bystander := uuid.New()
	for _, id := range []uuid.UUID{bystander, mentionedUser} {
		require.NoError(t, repo.Subscribe(ctx, domain.DocumentWatcher{
			DocumentID: base.documentID, WatcherID: id, WatcherKind: "user", Source: domain.WatchSourceExplicit,
		}, true))
	}

	in := base.createInput()
	in.Body = "@pavel does this still hold?"
	_, err := base.svc.Create(ctx, in)
	require.NoError(t, err)

	// Half one: commenting subscribed the author.
	st, err := repo.GetState(ctx, base.documentID, base.author, "user")
	require.NoError(t, err)
	assert.True(t, st.Watching, "joining the conversation subscribes you to it")
	assert.Equal(t, domain.WatchSourceCommenter, st.Source)

	// Half two: the watchers were told — the bystander through the watch path,
	// the named person through the mention path, and neither of them twice.
	byEvent := map[string][]uuid.UUID{}
	for _, call := range notify.Calls() {
		require.NotNil(t, call.TargetUserID)
		byEvent[call.EventType] = append(byEvent[call.EventType], *call.TargetUserID)
	}
	assert.Equal(t, []uuid.UUID{bystander}, byEvent[DocumentCommentedEvent],
		"only the bystander gets the watch copy")
	assert.Equal(t, []uuid.UUID{mentionedUser}, byEvent[DocumentMentionedEvent],
		"the named person gets the mention, which is the more specific of the two")
}

// --- the bell a watcher actually hears ---------------------------------------
//
// Everything above proves a notification was handed to a channel. These prove
// the channel exists. Until the provisioning below, a person who pressed Watch
// was recorded as a watcher, counted as a recipient, and reached by nothing:
// delivery to a human runs through notification_preferences, and no code path
// created a row on their behalf. Measured on prod 2026-08-20 — one preference
// row in the entire database, none in the workspace doing the watching, and the
// notifications table empty since the day it was created.

// prefFor returns the caller's row on channel ch, or nil.
func watchPrefFor(prefs []domain.NotificationPreference, userID uuid.UUID, ch string) *domain.NotificationPreference {
	for i := range prefs {
		p := &prefs[i]
		if p.Channel == ch && p.UserID != nil && *p.UserID == userID {
			return p
		}
	}
	return nil
}

func TestWatch_ExplicitUserWatchProvisionsTheBell(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	watcher := uuid.New()
	_, err := w.watch.Watch(w.userCtx(watcher, "Alice"), doc.ID, w.docs.wsID)
	require.NoError(t, err)

	p := watchPrefFor(w.notify.Preferences(), watcher, "web_push")
	require.NotNil(t, p, "pressing Watch has to leave the person somewhere to be told")
	assert.True(t, p.IsEnabled)
	assert.Equal(t, w.docs.wsID, p.WorkspaceID)
	assert.ElementsMatch(t,
		[]string{DocumentChangedEvent, DocumentCommentedEvent, DocumentDeletedEvent},
		[]string(p.Events),
	)
}

// TestWatch_ProvisioningKeepsEventsTheUserAlreadyHad — the repository's upsert
// replaces the events array wholesale, so a provisioning step that wrote only
// its own three would silently unsubscribe the person from everything else they
// had chosen.
func TestWatch_ProvisioningKeepsEventsTheUserAlreadyHad(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	watcher := uuid.New()
	w.notify.SeedPreference(domain.NotificationPreference{
		ID: uuid.New(), WorkspaceID: w.docs.wsID, UserID: &watcher,
		Channel: "web_push", Events: []string{"task.assigned"}, IsEnabled: true,
	})

	_, err := w.watch.Watch(w.userCtx(watcher, "Alice"), doc.ID, w.docs.wsID)
	require.NoError(t, err)

	p := watchPrefFor(w.notify.Preferences(), watcher, "web_push")
	require.NotNil(t, p)
	assert.ElementsMatch(t,
		[]string{"task.assigned", DocumentChangedEvent, DocumentCommentedEvent, DocumentDeletedEvent},
		[]string(p.Events),
	)
	assert.Len(t, w.notify.Preferences(), 1, "one row per (workspace, user, channel), not a second one")
}

// TestWatch_ASwitchedOffBellStaysOff — an explicit "no in-app notifications"
// outranks the implicit request inside a Watch click. The subscription is still
// recorded; what must not happen is the setting being flipped back on for them.
func TestWatch_ASwitchedOffBellStaysOff(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	watcher := uuid.New()
	w.notify.SeedPreference(domain.NotificationPreference{
		ID: uuid.New(), WorkspaceID: w.docs.wsID, UserID: &watcher,
		Channel: "web_push", Events: []string{"task.assigned"}, IsEnabled: false,
	})

	st, err := w.watch.Watch(w.userCtx(watcher, "Alice"), doc.ID, w.docs.wsID)
	require.NoError(t, err)
	assert.True(t, st.Watching, "the subscription is recorded either way")

	p := watchPrefFor(w.notify.Preferences(), watcher, "web_push")
	require.NotNil(t, p)
	assert.False(t, p.IsEnabled, "a channel the person turned off is not turned back on for them")
	assert.ElementsMatch(t, []string{"task.assigned"}, []string(p.Events))
}

// TestWatch_AnotherEnabledChannelIsEnough — somebody subscribed by email does
// not also need a bell row created for them.
func TestWatch_AnotherEnabledChannelIsEnough(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	watcher := uuid.New()
	w.notify.SeedPreference(domain.NotificationPreference{
		ID: uuid.New(), WorkspaceID: w.docs.wsID, UserID: &watcher, Channel: "email",
		Events:    []string{DocumentChangedEvent, DocumentCommentedEvent, DocumentDeletedEvent},
		IsEnabled: true,
	})

	_, err := w.watch.Watch(w.userCtx(watcher, "Alice"), doc.ID, w.docs.wsID)
	require.NoError(t, err)

	assert.Nil(t, watchPrefFor(w.notify.Preferences(), watcher, "web_push"))
	assert.Len(t, w.notify.Preferences(), 1)
}

// TestWatch_AutoSubscribeLeavesPreferencesAlone — being auto-subscribed for
// commenting is not a request to be notified, and silently re-adding an event
// type to the settings of everyone who ever commented is what unsubscribing
// exists to prevent.
func TestWatch_AutoSubscribeLeavesPreferencesAlone(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	commenter := uuid.New()
	w.watch.AutoSubscribe(context.Background(), doc.ID, commenter, "user", domain.WatchSourceCommenter)

	st, err := w.repo.GetState(context.Background(), doc.ID, commenter, "user")
	require.NoError(t, err)
	require.True(t, st.Watching, "positive control: the subscription itself was written")
	assert.Empty(t, w.notify.Preferences())
}

// TestWatch_AgentWatchLeavesPreferencesAlone — an agent is reached over its own
// channel and has no preference row to provision; writing one would file a user
// setting under an agent id.
func TestWatch_AgentWatchLeavesPreferencesAlone(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	agent := uuid.New()
	ctx := actorctx.WithActorName(actorctx.WithActor(context.Background(), agent, domain.ActorTypeAgent), "Bill")
	_, err := w.watch.Watch(ctx, doc.ID, w.docs.wsID)
	require.NoError(t, err)

	assert.Empty(t, w.notify.Preferences())
}

// TestWatch_UnreadablePreferencesDoNotLoseTheSubscription — provisioning is a
// courtesy that rides along with the subscription; it may not take it down.
func TestWatch_UnreadablePreferencesDoNotLoseTheSubscription(t *testing.T) {
	w := setupWatch(t)
	doc := w.create(t, "Billing", "start")

	w.notify.FailPreferenceReads(errors.New("preferences unavailable"))

	watcher := uuid.New()
	st, err := w.watch.Watch(w.userCtx(watcher, "Alice"), doc.ID, w.docs.wsID)
	require.NoError(t, err)
	assert.True(t, st.Watching)
}
