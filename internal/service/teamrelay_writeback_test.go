package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/integration/teamrelay"
)

// R8 — write-back to Team Relay through the sync protocol, conditional on the
// sha256 the copy was read at.
//
// The tests here are written against the acceptance criteria of the task, and
// the one they exist for is AC-2: an original edited on their side after we
// took our copy must REFUSE our write and stay byte-for-byte unchanged. An
// accepted write and a silently-overwriting one are indistinguishable by
// status code, which is why every assertion below reads the content the double
// holds rather than the error the call returned.

// writeBackFixture wires a document service whose Team Relay collaborator is a
// real teamRelayMountService (not a stub of the thing under test) backed by the
// counting relay double, so the ordering inside updateOnce is exercised end to
// end rather than asserted about.
type writeBackFixture struct {
	docs  *documentFixture
	mount *mountFixture
}

func setupWriteBack(t *testing.T) *writeBackFixture {
	t.Helper()
	mount := setupMountFixture(t, "")

	// Same repo and storage on both services: the point of these tests is what
	// is left in the store after a refusal, and two separate stores would let a
	// local write "not happen" in the store the assertions read while happening
	// in the one the service wrote to.
	projectRepo := NewMockProjectRepository()
	require.NoError(t, projectRepo.Create(context.Background(), &domain.Project{ID: mount.projectID, WorkspaceID: mount.wsID}))

	svc := NewDocumentService(mount.repo, mount.storage, projectRepo, NewMockDocumentCommentRepository(),
		WithTeamRelayRefresher(mount.svc),
		WithTeamRelayWriter(mount.svc)).(*documentService)

	return &writeBackFixture{
		docs: &documentFixture{
			svc:       svc,
			repo:      mount.repo,
			storage:   mount.storage,
			projectID: mount.projectID,
			wsID:      mount.wsID,
		},
		mount: mount,
	}
}

func (f *writeBackFixture) update(t *testing.T, doc *domain.Document, body string) (*domain.Document, error) {
	t.Helper()
	return f.docs.svc.Update(context.Background(), doc.ID, f.docs.wsID, UpdateDocumentInput{
		Body:          &body,
		UpdatedBy:     uuid.New(),
		UpdatedByType: domain.ActorTypeUser,
	})
}

// storedBody reads the markdown actually in object storage for doc — the only
// thing that settles whether a local write happened.
func (f *writeBackFixture) storedBody(t *testing.T, doc *domain.Document) string {
	t.Helper()
	body, err := f.docs.svc.downloadBody(context.Background(), doc.StorageKey)
	require.NoError(t, err)
	return body
}

// AC-1: an edit to a copy reaches the original through the sync protocol,
// conditional on the hash we read it at, and the copy records the new version.
func TestWriteBack_EditReachesOriginal(t *testing.T) {
	f := setupWriteBack(t)
	const path = "notes/spec.md"
	original := "# Spec\n\noriginal paragraph\n"
	doc := f.mount.seedCopy(t, path, original, fakeSHA(original), frozenTime)

	updated, err := f.update(t, doc, "# Spec\n\nedited paragraph\n")
	require.NoError(t, err)

	// Reached THEM, not just us.
	remoteBody, remoteSHA := f.mount.client.remoteBody(path)
	assert.Equal(t, "# Spec\n\nedited paragraph\n", remoteBody,
		"the edit must be visible in the original's body on their side, not merely acknowledged")

	// Sent conditionally, on the hash the editor was shown.
	require.Len(t, f.mount.client.writeReqs, 1)
	assert.Equal(t, fakeSHA(original), f.mount.client.writeReqs[0].IfMatch,
		"If-Match must be the sha256 the copy was synced at — an empty or wildcard precondition is a blind overwrite")
	assert.Equal(t, path, f.mount.client.writeReqs[0].Path)

	// And the copy now tracks the version it just created, so the NEXT edit
	// sends the right precondition. Without this the second save always 412s.
	require.NotNil(t, updated.SourceSHA256)
	assert.Equal(t, remoteSHA, *updated.SourceSHA256)
}

// AC-2 — THE load-bearing negative control. The original is changed on their
// side after we took our copy; our write must be refused and their text must
// be byte-for-byte what it was.
func TestWriteBack_RefusedWhenOriginalChangedUnderUs(t *testing.T) {
	f := setupWriteBack(t)
	const path = "notes/spec.md"
	original := "# Spec\n\noriginal paragraph\n"
	doc := f.mount.seedCopy(t, path, original, fakeSHA(original), frozenTime)

	// Somebody edits the original in Obsidian after we read it. Our copy still
	// holds the old hash — that staleness is the whole scenario.
	theirText := "# Spec\n\ntheir paragraph, written in Obsidian\n"
	f.mount.client.seedRemote(path, theirText, fakeSHA(theirText))

	_, err := f.update(t, doc, "# Spec\n\nour paragraph\n")

	// Refused, and refused as a conflict the caller can act on.
	require.Error(t, err)
	var conflict *ExternalSourceConflictError
	require.ErrorAs(t, err, &conflict,
		"a refused write-back must surface as ExternalSourceConflictError, not a generic 500 — the recovery differs")
	assert.Equal(t, path, conflict.SourcePath)

	// Their text is untouched. Asserted by CONTENT: an overwrite that returned
	// an error anyway would pass an error-only assertion.
	remoteBody, remoteSHA := f.mount.client.remoteBody(path)
	assert.Equal(t, theirText, remoteBody, "a refused write must leave the original byte-for-byte unchanged")
	assert.Equal(t, fakeSHA(theirText), remoteSHA)
}

// AC-3: the refusal must not cost the user their text. Locally, nothing moved
// — so the copy stays consistent with the original it last saw, and the editor
// still holds the unsaved edit to re-apply.
//
// This is the assertion that pins the ORDERING. Write locally first and this
// test fails: the body in storage would be ours while the original is theirs,
// and RefreshIfStale would later resolve that divergence by discarding the
// user's work.
func TestWriteBack_RefusalWritesNothingLocally(t *testing.T) {
	f := setupWriteBack(t)
	const path = "notes/spec.md"
	original := "# Spec\n\noriginal paragraph\n"
	doc := f.mount.seedCopy(t, path, original, fakeSHA(original), frozenTime)
	versionBefore := doc.Version

	theirText := "# Spec\n\ntheir paragraph\n"
	f.mount.client.seedRemote(path, theirText, fakeSHA(theirText))

	_, err := f.update(t, doc, "# Spec\n\nour paragraph\n")
	require.Error(t, err)

	assert.Equal(t, original, f.storedBody(t, doc),
		"the local body must be untouched by a refused write — a copy that diverges here is one the refresher later overwrites, destroying the edit")

	after, err := f.docs.repo.GetByIDInWorkspace(context.Background(), doc.ID, f.docs.wsID)
	require.NoError(t, err)
	assert.Equal(t, versionBefore, after.Version, "a refused write must not bump the version")
	require.NotNil(t, after.SourceSHA256)
	assert.Equal(t, fakeSHA(original), *after.SourceSHA256,
		"source_sha256 must still name the version we actually hold; moving it here would make the next write silently overwrite theirs")
}

// AC-4: after the user re-reads and rebuilds the edit on the new original, the
// retry goes through. A conflict that cannot be resolved is a wall, not a gate.
func TestWriteBack_RetryAfterRebuildSucceeds(t *testing.T) {
	f := setupWriteBack(t)
	const path = "notes/spec.md"
	original := "# Spec\n\noriginal paragraph\n"
	// Synced an hour ago, so the re-open below is genuinely past the TTL. Seeded
	// at frozenTime it would not be, and the test would "fail to refresh" for a
	// reason that has nothing to do with write-back.
	doc := f.mount.seedCopy(t, path, original, fakeSHA(original), frozenTime.Add(-time.Hour))

	theirText := "# Spec\n\ntheir paragraph\n"
	f.mount.client.seedRemote(path, theirText, fakeSHA(theirText))

	_, err := f.update(t, doc, "# Spec\n\nour paragraph\n")
	require.Error(t, err)

	// The user re-opens the document. Past the TTL, so the refresher pulls the
	// new original down — this is the "re-read" half, and it is what supplies
	// the fresh precondition.
	f.mount.setTTLSeconds(t, 1)
	reopened, err := f.docs.svc.GetByIDInWorkspace(context.Background(), doc.ID, f.docs.wsID)
	require.NoError(t, err)
	require.NotNil(t, reopened.SourceSHA256)
	assert.Equal(t, fakeSHA(theirText), *reopened.SourceSHA256, "re-opening must load the new original before a retry can succeed")

	// They re-apply their change on top of what they can now see, and save.
	rebuilt := "# Spec\n\ntheir paragraph\n\nour paragraph\n"
	_, err = f.update(t, reopened, rebuilt)
	require.NoError(t, err, "a rebuilt edit against the current original must be accepted")

	remoteBody, _ := f.mount.client.remoteBody(path)
	assert.Equal(t, rebuilt, remoteBody)
	assert.Contains(t, remoteBody, "their paragraph", "the rebuild must preserve the other writer's text, not replace it")
}

// AC-5: a conflict must not turn into a retry loop. The autosave fires every
// couple of seconds; if one save could fan out into N relay writes, a conflict
// would become a burst against their server and, worse, a race one of those
// blind retries could win.
func TestWriteBack_ConflictIsNotRetried(t *testing.T) {
	f := setupWriteBack(t)
	const path = "notes/spec.md"
	original := "# Spec\n\noriginal\n"
	doc := f.mount.seedCopy(t, path, original, fakeSHA(original), frozenTime)

	theirText := "# Spec\n\ntheirs\n"
	f.mount.client.seedRemote(path, theirText, fakeSHA(theirText))

	_, err := f.update(t, doc, "# Spec\n\nours\n")
	require.Error(t, err)
	assert.Equal(t, 1, f.mount.client.writeCount(),
		"one save must cost exactly one relay write even when refused — internal retry would be a blind overwrite on the second attempt")

	// An append is the one input Update retries internally (a lost version is a
	// stale read to redo). That retry must not survive an external conflict
	// either: the append loop keys on DocumentVersionConflictError, and this is
	// deliberately a different type.
	f2 := setupWriteBack(t)
	doc2 := f2.mount.seedCopy(t, path, original, fakeSHA(original), frozenTime)
	f2.mount.client.seedRemote(path, theirText, fakeSHA(theirText))
	appended := "\nappended line\n"
	_, err = f2.docs.svc.Update(context.Background(), doc2.ID, f2.docs.wsID, UpdateDocumentInput{
		AppendBody:    &appended,
		UpdatedBy:     uuid.New(),
		UpdatedByType: domain.ActorTypeUser,
	})
	require.Error(t, err)
	var conflict *ExternalSourceConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, 1, f2.mount.client.writeCount(),
		"an append that conflicts externally must not re-enter the append-retry loop")
}

// A move or a rename changes no text, so it must cost no relay write at all.
// Without this, dragging a mounted folder in the tree would push every document
// under it back to Team Relay.
func TestWriteBack_NonBodyEditDoesNotWriteBack(t *testing.T) {
	f := setupWriteBack(t)
	const path = "notes/spec.md"
	original := "# Spec\n\noriginal\n"
	doc := f.mount.seedCopy(t, path, original, fakeSHA(original), frozenTime)

	title := "Renamed"
	_, err := f.docs.svc.Update(context.Background(), doc.ID, f.docs.wsID, UpdateDocumentInput{
		Title:         &title,
		UpdatedBy:     uuid.New(),
		UpdatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, f.mount.client.writeCount(), "a rename touches no text and must not write to the original")
}

// An own document must never take the write-back path, whatever else is wired.
func TestWriteBack_OwnDocumentIsNeverPushed(t *testing.T) {
	f := setupWriteBack(t)
	doc, err := f.docs.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID:     f.docs.projectID,
		Title:         "Ours",
		Body:          "local only\n",
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	_, err = f.update(t, doc, "still local\n")
	require.NoError(t, err)
	assert.Equal(t, 0, f.mount.client.writeCount(), "an own document has no original to write back to")
}

// Unwired, an edit to a copy is refused rather than kept locally. Keeping it
// would be the silent divergence again, just with the relay absent instead of
// disagreeing.
func TestWriteBack_RefusesWhenIntegrationNotWired(t *testing.T) {
	mount := setupMountFixture(t, "")
	projectRepo := NewMockProjectRepository()
	require.NoError(t, projectRepo.Create(context.Background(), &domain.Project{ID: mount.projectID, WorkspaceID: mount.wsID}))
	svc := NewDocumentService(mount.repo, mount.storage, projectRepo, NewMockDocumentCommentRepository()).(*documentService)

	const path = "notes/spec.md"
	original := "# Spec\n\noriginal\n"
	doc := mount.seedCopy(t, path, original, fakeSHA(original), frozenTime)

	body := "edited\n"
	_, err := svc.Update(context.Background(), doc.ID, mount.wsID, UpdateDocumentInput{
		Body:          &body,
		UpdatedBy:     uuid.New(),
		UpdatedByType: domain.ActorTypeUser,
	})
	require.Error(t, err, "with no writer wired, an edit to a copy must be refused, not silently kept local")

	stored, err := svc.downloadBody(context.Background(), doc.StorageKey)
	require.NoError(t, err)
	assert.Equal(t, original, stored)
}

// WriteBack refuses a copy whose recorded source hash is empty, before any
// request is made. An empty precondition is what an unconditional write looks
// like from the inside.
func TestWriteBack_RefusesEmptyPrecondition(t *testing.T) {
	f := setupWriteBack(t)
	empty := ""
	share := testRelayShareID
	path := "notes/spec.md"
	doc := &domain.Document{
		ID:           uuid.New(),
		ProjectID:    f.docs.projectID,
		SourceKind:   domain.DocumentSourceTeamRelay,
		SourceShare:  &share,
		SourcePath:   &path,
		SourceSHA256: &empty,
	}

	_, err := f.mount.svc.WriteBack(context.Background(), doc, "anything")
	require.Error(t, err)
	assert.Equal(t, 0, f.mount.client.writeCount(), "a copy with no recorded version must not reach the network at all")
}

// AC-6, the path negative control: nothing in the write path may address the
// web-publish family. Asserted against the source of the client itself, because
// the failure it guards is a future edit, not today's behaviour.
func TestWriteBack_NeverUsesWebPublishFamily(t *testing.T) {
	// Comments are stripped first, and that is not a convenience: this file's
	// header comment names /v1/web/shares/... precisely in order to disclaim
	// it, so a raw substring search reports a violation that is really the
	// documentation of the rule. A control that fires on its own rationale is
	// one somebody deletes.
	src := stripLineComments(readSourceFile(t, "../integration/teamrelay/sync_client.go"))

	require.Contains(t, src, "/v1/shares/%s/sync-write", "positive control: the write must go through the sync protocol")
	assert.NotContains(t, src, "/v1/web/",
		"the write path must never address the web-publish family — it carries no sha256, so a write through it is unconditional by construction")

	// The wildcard is a conditional-looking blind write. It must not appear.
	assert.NotContains(t, strings.ReplaceAll(src, " ", ""), `If-Match","*"`,
		"If-Match: * asserts only that some version exists — that is a blind overwrite in conditional clothing")
}

// The client's own conflict sentinel must survive being wrapped by the service,
// or the handler cannot tell a conflict from an outage.
func TestWriteBack_ConflictSentinelSurvivesWrapping(t *testing.T) {
	f := setupWriteBack(t)
	const path = "notes/spec.md"
	original := "x\n"
	doc := f.mount.seedCopy(t, path, original, fakeSHA(original), frozenTime)
	f.mount.client.writeErr = teamrelay.ErrSyncConflict

	_, err := f.mount.svc.WriteBack(context.Background(), doc, "y\n")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrExternalSourceConflict),
		"the service must translate the client's conflict into its own sentinel, not flatten it into a generic error")
}

// readSourceFile reads a source file so a test can assert about the code
// itself. Used for the AC-6 path control: "the write path does not call
// web-publish" is a property of what is WRITTEN, and a behavioural test can
// only show that today's call happened to go elsewhere.
func readSourceFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	require.NoError(t, err, "reading %s — if this moved, fix the path rather than deleting the control", rel)
	return string(b)
}

// stripLineComments removes // comments so a source-level control asserts about
// code rather than about prose that discusses the code.
func stripLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// Regression: an append whose relay write LANDED, followed by a local version
// race, must not be retried and must not be reported as "nothing was written".
//
// Found by an independent verifier, not by me. The path: WriteBack succeeds →
// the local Update loses to a concurrent Mesh edit → Update's append-retry loop
// (which retries on DocumentVersionConflictError, by design) re-enters
// updateOnce → WriteBack fires a SECOND time carrying the stale source_sha256,
// because the stamp only runs after the local write wins. The relay's own
// compare-and-swap correctly refuses that second write, so no data was ever
// overwritten — but the user was handed external_source_conflict, whose message
// states their text is unsaved and the original untouched. Their append was in
// fact already on the original.
//
// The safety property held; the report did not. Both matter here, and a wrong
// report in the reassuring direction is the same class of defect as one in the
// alarming direction — this is the unit whose whole subject is not misleading a
// writer about their own text.
func TestWriteBack_AppendThatLandedIsNotRetriedOrReportedAsLost(t *testing.T) {
	f := setupWriteBack(t)
	const path = "notes/spec.md"
	original := "start\n"
	doc := f.mount.seedCopy(t, path, original, fakeSHA(original), frozenTime)

	// A competing local edit lands in the gap between the service reading the
	// document and writing it — a rename from another tab, say. Nothing to do
	// with Team Relay; purely a local version race.
	f.docs.repo.beforeUpdate = func() {
		stored := f.docs.repo.items[doc.ID]
		stored.Version++
	}

	appended := "appended by us\n"
	_, err := f.docs.svc.Update(context.Background(), doc.ID, f.docs.wsID, UpdateDocumentInput{
		AppendBody:    &appended,
		UpdatedBy:     uuid.New(),
		UpdatedByType: domain.ActorTypeUser,
	})
	require.Error(t, err)

	// Exactly one relay write. A second would carry a precondition that is stale
	// by construction and would append to text that already contains the append.
	assert.Equal(t, 1, f.mount.client.writeCount(),
		"an attempt that already reached the relay must not be retried — the retry re-pushes and double-appends")

	// The relay holds the user's text.
	remoteBody, _ := f.mount.client.remoteBody(path)
	assert.Equal(t, "start\nappended by us\n", remoteBody, "the append did land on the source")

	// And the error must say so. This is the assertion the old behaviour failed.
	var landedErr *ExternalSourceWriteLandedError
	require.ErrorAs(t, err, &landedErr,
		"a landed write followed by a local race must NOT be reported as ExternalSourceConflictError — that message tells the author their text is unsaved when it is on the source of truth")
	assert.Equal(t, path, landedErr.SourcePath)

	var lostErr *ExternalSourceConflictError
	assert.False(t, errors.As(err, &lostErr), "must not masquerade as the nothing-was-written conflict")
}
