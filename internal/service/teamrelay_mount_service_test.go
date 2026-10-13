package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/integration/teamrelay"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ---------------------------------------------------------------------------
// fakeRelaySyncClient — a counting, scriptable double for RelaySyncClient.
//
// This is what makes the AC-3 counter test possible at all: production talks
// to a real HTTP relay, and the whole point of that test is to observe HOW
// MANY TIMES it was asked, which a real client has no way to report.
// ---------------------------------------------------------------------------

type fakeRelaySyncClient struct {
	mu sync.Mutex

	filesIndexCalls int
	downloadCalls   int
	downloadPaths   []string

	indexResult []teamrelay.SyncIndexEntry
	indexErr    error

	// downloadFn, when set, computes the response per call (path-dependent
	// fixtures). Otherwise downloadResult/downloadErr apply to every call.
	downloadFn     func(path string) (*teamrelay.SyncDocument, error)
	downloadResult *teamrelay.SyncDocument
	downloadErr    error

	// defaultDocs is the "the source agrees with the copy we seeded" response,
	// registered per path by seedCopy. Its only job is to keep this double
	// ANSWERING on paths a test never scripted, so that a mutation which
	// wrongly reaches the network fails on the assertion that test is about
	// rather than on a nil dereference inside the service.
	//
	// This is not a convenience. A fixture that returns (nil, nil) makes every
	// mutation on the download path die at `remote.SHA256` before the counter
	// is ever read, so the test goes red for a reason it was not written to
	// detect -- and a control that fails for the wrong reason is a broken
	// control, indistinguishable from a working one at a glance.
	defaultDocs map[string]*teamrelay.SyncDocument

	// Write-back (R8) recording. writeReqs keeps the full request rather than a
	// count, because the load-bearing assertions are about WHAT was sent — the
	// If-Match hash above all — and a counter cannot distinguish a conditional
	// write from an unconditional one.
	writeCalls int
	writeReqs  []fakeWriteRequest
	writeErr   error
}

// fakeWriteRequest is one recorded sync-write.
type fakeWriteRequest struct {
	Path    string
	IfMatch string
	Body    string
}

// fakeSHA is a stand-in content hash for the double. It is NOT sha256 and does
// not need to be: every assertion here is about whether two hashes are the
// same value, never about the algorithm. Using a distinguishable prefix keeps
// a real sha256 from being confused with a fixture one when a test fails.
func fakeSHA(body string) string {
	return fmt.Sprintf("fakesha-%d-%s", len(body), strings.ReplaceAll(strings.TrimSpace(body), " ", "_"))
}

// seedRemote registers the default response for path: a source whose content
// and hash match what the local copy already holds.
func (f *fakeRelaySyncClient) seedRemote(path, body, sourceSHA string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.defaultDocs == nil {
		f.defaultDocs = map[string]*teamrelay.SyncDocument{}
	}
	f.defaultDocs[path] = &teamrelay.SyncDocument{Content: []byte(body), SHA256: sourceSHA}
}

func (f *fakeRelaySyncClient) FilesIndex(_ context.Context, _, _, _ string) ([]teamrelay.SyncIndexEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filesIndexCalls++
	return f.indexResult, f.indexErr
}

func (f *fakeRelaySyncClient) Download(_ context.Context, _, _, path, _ string) (*teamrelay.SyncDocument, error) {
	f.mu.Lock()
	f.downloadCalls++
	f.downloadPaths = append(f.downloadPaths, path)
	f.mu.Unlock()
	if f.downloadFn != nil {
		return f.downloadFn(path)
	}
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	if f.downloadResult != nil {
		return f.downloadResult, nil
	}

	f.mu.Lock()
	remote, ok := f.defaultDocs[path]
	f.mu.Unlock()
	if ok {
		return remote, nil
	}
	// Never (nil, nil): an unscripted path is a fixture gap, and it must say so
	// by name instead of handing the caller a nil document to dereference.
	return nil, fmt.Errorf("fakeRelaySyncClient: no download fixture for path %q", path)
}

// Write simulates the relay's conditional write faithfully enough for the
// negative control to mean something: it compares If-Match against the hash it
// currently holds and, on a mismatch, returns ErrSyncConflict having changed
// NOTHING. The real server gets that property from doing the compare and the
// swap inside one row lock; this double gets it from doing the compare before
// the assignment. A fixture that mutated first and reported the conflict after
// would let a broken write-back pass the very test written to catch it.
func (f *fakeRelaySyncClient) Write(_ context.Context, _, _, path, _, ifMatchSHA256 string, body []byte) (*teamrelay.SyncWriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.writeCalls++
	f.writeReqs = append(f.writeReqs, fakeWriteRequest{Path: path, IfMatch: ifMatchSHA256, Body: string(body)})

	if f.writeErr != nil {
		return nil, f.writeErr
	}
	if f.defaultDocs == nil {
		f.defaultDocs = map[string]*teamrelay.SyncDocument{}
	}
	current, ok := f.defaultDocs[path]
	if !ok {
		return nil, fmt.Errorf("fakeRelaySyncClient: no write fixture for path %q", path)
	}
	if ifMatchSHA256 != current.SHA256 {
		// Refused. Deliberately no mutation above this line.
		return nil, teamrelay.ErrSyncConflict
	}

	newSHA := fakeSHA(string(body))
	f.defaultDocs[path] = &teamrelay.SyncDocument{Content: append([]byte(nil), body...), SHA256: newSHA}
	return &teamrelay.SyncWriteResult{Path: path, SHA256: newSHA, Size: int64(len(body))}, nil
}

// remoteBody reports what the double currently holds for path, so a test can
// assert the original is byte-for-byte unchanged after a refused write rather
// than merely trusting the error.
func (f *fakeRelaySyncClient) remoteBody(path string) (body, sourceSHA string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.defaultDocs[path]
	if !ok {
		return "", ""
	}
	return string(d.Content), d.SHA256
}

func (f *fakeRelaySyncClient) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writeCalls
}

func (f *fakeRelaySyncClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.downloadCalls
}

var _ RelaySyncClient = (*fakeRelaySyncClient)(nil)

// fakeKeyDescriber is a no-op KeyDescriber double — every test in this file
// exercises RefreshIfStale/SyncMount for reasons unrelated to #218d5847's
// AC4 key-expiry producers, so a canned "no expiry known" response is enough
// to keep recordSyncOutcome's introspection call from either reaching a real
// Team Relay server or needing per-test fixture setup it doesn't care about.
// The producer wiring itself is covered by its own dedicated tests.
type fakeKeyDescriber struct {
	mu    sync.Mutex
	calls int
	desc  *teamrelay.AgentKeyDescription
	err   error
}

func (f *fakeKeyDescriber) DescribeAgentKey(context.Context, string, string, string) (*teamrelay.AgentKeyDescription, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.desc != nil {
		return f.desc, nil
	}
	return &teamrelay.AgentKeyDescription{}, nil
}

func (f *fakeKeyDescriber) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var _ KeyDescriber = (*fakeKeyDescriber)(nil)

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

const testRelayShareID = "8c5e7efd-0000-0000-0000-000000000000"

type mountFixture struct {
	svc         *teamRelayMountService
	repo        *MockDocumentRepository
	storage     *MockStorageClient
	piRepo      *MockProjectIntegrationRepository
	client      *fakeRelaySyncClient
	attachments *MockDocumentAttachmentRepository
	projectID   uuid.UUID
	wsID        uuid.UUID
}

// setupMountFixture wires a teamRelayMountService against mocks, with a Team
// Relay integration already configured for projectID and MESH_TEAMRELAY_RELAY_URL
// set for the duration of the test.
func setupMountFixture(t *testing.T, mountPath string) *mountFixture {
	t.Helper()

	t.Setenv(teamRelayRelayURLEnvVar, "http://fake-relay.test")

	projectID := uuid.New()
	wsID := uuid.New()

	repo := NewMockDocumentRepository().WithProjectWorkspace(projectID, wsID)
	storage := NewMockStorageClient()
	piRepo := NewMockProjectIntegrationRepository()
	piService := NewProjectIntegrationService(piRepo)
	client := &fakeRelaySyncClient{}

	settings := domain.TeamRelaySettings{ShareID: testRelayShareID, DocsMountPath: mountPath}
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err)
	require.NoError(t, piRepo.Upsert(context.Background(), &domain.ProjectIntegration{
		ID:        uuid.New(),
		ProjectID: projectID,
		Type:      "team_relay",
		Enabled:   true,
		Settings:  settingsJSON,
		AgentKey:  "tr_agent_secret_at_least_20_chars",
	}))

	timeNow = func() time.Time { return frozenTime }
	t.Cleanup(func() { timeNow = time.Now })

	attachments := NewMockDocumentAttachmentRepository()
	svc := NewTeamRelayMountService(repo, storage, piService, piRepo,
		WithRelaySyncClient(client), WithKeyDescriber(&fakeKeyDescriber{}),
		WithAttachmentRepository(attachments),
	).(*teamRelayMountService)

	return &mountFixture{
		svc:         svc,
		repo:        repo,
		storage:     storage,
		piRepo:      piRepo,
		client:      client,
		attachments: attachments,
		projectID:   projectID,
		wsID:        wsID,
	}
}

// liveAttachmentsFor lists a document's live attachments — small enough a
// helper beats repeating the loop in every embed test.
func (f *mountFixture) liveAttachmentsFor(t *testing.T, documentID uuid.UUID) []domain.DocumentAttachment {
	t.Helper()
	page, err := f.attachments.ListByDocument(context.Background(), documentID, pagination.Params{})
	require.NoError(t, err)
	return page.Items
}

// realSHA is sha256-as-hex for fixture bytes. The embed importer verifies the
// downloaded content's hash against the files-index claim, so unlike the doc
// fixtures (fakeSHA — only ever compared to itself) image fixtures need the
// real digest.
func realSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// pngFixture is minimal real-PNG-header bytes: enough for
// http.DetectContentType to say "image/png" (it reads magic bytes only) and
// short enough to keep the test readable.
func pngFixture(label string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte(label)...)
}

// pdfFixture likewise: DetectContentType wants a %PDF- prefix.
func pdfFixture() []byte {
	return []byte("%PDF-1.4 fake-but-valid-magic body")
}

// setTTLSeconds rewrites the project's Team Relay settings with an explicit
// sync TTL, so a test can assert the CONFIGURED value is what gates the
// refresh rather than the compiled-in default.
func (f *mountFixture) setTTLSeconds(t *testing.T, seconds int) {
	t.Helper()
	pi, err := f.piRepo.Get(context.Background(), f.projectID, "team_relay")
	require.NoError(t, err)
	settingsJSON, err := json.Marshal(domain.TeamRelaySettings{
		ShareID:        testRelayShareID,
		SyncTTLSeconds: seconds,
	})
	require.NoError(t, err)
	pi.Settings = settingsJSON
	require.NoError(t, f.piRepo.Upsert(context.Background(), pi))
}

// seedCopy inserts an already-mounted Team Relay copy document, synced at
// syncedAt with body and sha256 as given.
func (f *mountFixture) seedCopy(t *testing.T, sourcePath, body, sourceSHA string, syncedAt time.Time) *domain.Document {
	t.Helper()
	id := uuid.New()
	key := documentStorageKey(f.projectID, id)
	require.NoError(t, f.storage.Upload(context.Background(), key, strings.NewReader(body), int64(len(body)), documentContentType))

	// The source starts out AGREEING with the copy. A test that wants a
	// diverged source overrides this (downloadResult/downloadFn); a test that
	// asserts "we never asked" gets a live, counting double either way, so the
	// counter assertion is what fails when the gate is removed.
	f.client.seedRemote(sourcePath, body, sourceSHA)

	share := testRelayShareID
	path := sourcePath
	sha := sourceSHA
	doc := &domain.Document{
		ID:            id,
		ProjectID:     f.projectID,
		Slug:          "seeded",
		Title:         "Seeded",
		StorageKey:    key,
		Version:       1,
		CreatedBy:     systemActorID,
		CreatedByType: domain.ActorTypeSystem,
		CreatedAt:     frozenTime,
		UpdatedAt:     frozenTime,
		SourceKind:    domain.DocumentSourceTeamRelay,
		SourceShare:   &share,
		SourcePath:    &path,
		SourceSHA256:  &sha,
		SyncedAt:      &syncedAt,
	}
	f.repo.Seed(doc)
	return doc
}

// ---------------------------------------------------------------------------
// AC-3: freshness is TTL-gated — zero calls inside the window, exactly one
// once it elapses. Both directions asserted; a code path missing either half
// would still pass the OTHER half.
// ---------------------------------------------------------------------------

func TestRefreshIfStale_WithinTTL_MakesNoNetworkCall(t *testing.T) {
	f := setupMountFixture(t, "")
	doc := f.seedCopy(t, "Notes/Welcome.md", "old body", "sha-old", frozenTime)

	// Open it twice, back to back, well inside the default TTL.
	require.NoError(t, f.svc.RefreshIfStale(context.Background(), doc))
	require.NoError(t, f.svc.RefreshIfStale(context.Background(), doc))

	assert.Equal(t, 0, f.client.callCount(), "a fresh copy must not touch the relay at all — that is the whole point of the TTL gate")
}

func TestRefreshIfStale_TTLElapsed_MakesExactlyOneCall(t *testing.T) {
	f := setupMountFixture(t, "")
	doc := f.seedCopy(t, "Notes/Welcome.md", "old body", "sha-old", frozenTime)

	f.client.downloadResult = &teamrelay.SyncDocument{Content: []byte("old body"), SHA256: "sha-old"}

	// Move the clock past the default TTL.
	timeNow = func() time.Time { return frozenTime.Add(DefaultTeamRelaySyncTTL + time.Minute) }

	require.NoError(t, f.svc.RefreshIfStale(context.Background(), doc))

	assert.Equal(t, 1, f.client.callCount(), "TTL elapsed once — the check must run exactly once, not zero and not more than once")

	// And opening it again immediately afterwards (synced_at just advanced)
	// must NOT make a second call.
	require.NoError(t, f.svc.RefreshIfStale(context.Background(), doc))
	assert.Equal(t, 1, f.client.callCount(), "synced_at advanced on the first refresh — a second open right after must find it fresh again")
}

// ---------------------------------------------------------------------------
// AC-2: the body actually changes, asserted on content — not merely that a
// request happened.
// ---------------------------------------------------------------------------

func TestRefreshIfStale_HashChanged_RewritesBody(t *testing.T) {
	f := setupMountFixture(t, "")
	doc := f.seedCopy(t, "Notes/Welcome.md", "old body", "sha-old", frozenTime)

	f.client.downloadResult = &teamrelay.SyncDocument{Content: []byte("NEW body from the source"), SHA256: "sha-new"}
	timeNow = func() time.Time { return frozenTime.Add(DefaultTeamRelaySyncTTL + time.Minute) }

	require.NoError(t, f.svc.RefreshIfStale(context.Background(), doc))

	rc, err := f.storage.Download(context.Background(), doc.StorageKey)
	require.NoError(t, err)
	buf := make([]byte, 64)
	n, _ := rc.Read(buf)
	assert.Equal(t, "NEW body from the source", string(buf[:n]), "the stored body must be the NEW content, not merely have received a write")

	require.NotNil(t, doc.SourceSHA256)
	assert.Equal(t, "sha-new", *doc.SourceSHA256)
	assert.Equal(t, 2, doc.Version, "a body rewrite bumps version — same rule Update's own bumpVersion follows")
}

// ---------------------------------------------------------------------------
// Same-hash: TTL expired, hash unchanged → body NOT rewritten, synced_at
// advances anyway. Also the mutation-control fixture for (b): the fake here
// deliberately returns a DIFFERENT UpdatedAt for identical content, which is
// exactly the shape that breaks if the comparison is ever done on UpdatedAt
// instead of sha256.
// ---------------------------------------------------------------------------

func TestRefreshIfStale_HashUnchanged_BodyNotRewritten_ButSyncedAtAdvances(t *testing.T) {
	f := setupMountFixture(t, "")
	doc := f.seedCopy(t, "Notes/Welcome.md", "unchanged body", "sha-same", frozenTime)

	// Same hash, but a DIFFERENT UpdatedAt from the source — content untouched,
	// only its mtime moved (e.g. a re-save with no actual edit). A comparison
	// keyed on UpdatedAt would see this as "changed"; one keyed on sha256 must
	// not.
	f.client.downloadResult = &teamrelay.SyncDocument{
		Content:   []byte("SOMETHING ELSE ENTIRELY"), // if this ever lands in storage, the test below catches it
		SHA256:    "sha-same",
		UpdatedAt: "2099-01-01T00:00:00Z",
	}
	newNow := frozenTime.Add(DefaultTeamRelaySyncTTL + time.Minute)
	timeNow = func() time.Time { return newNow }

	require.NoError(t, f.svc.RefreshIfStale(context.Background(), doc))

	rc, err := f.storage.Download(context.Background(), doc.StorageKey)
	require.NoError(t, err)
	buf := make([]byte, 64)
	n, _ := rc.Read(buf)
	assert.Equal(t, "unchanged body", string(buf[:n]), "hash matched — the stored body must be untouched")

	require.NotNil(t, doc.SyncedAt)
	assert.True(t, doc.SyncedAt.Equal(newNow), "synced_at must still advance — the check DID happen, it just found nothing to change")
	assert.Equal(t, 1, f.client.callCount(), "exactly one check, whether or not anything changed")
}

// ---------------------------------------------------------------------------
// AC-4: each relay-side failure surfaces as a DISTINCT, identifiable
// MountStatus — never a generic empty success.
// ---------------------------------------------------------------------------

func TestSyncMount_SentinelErrorsAreDistinct(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		expect MountStatus
	}{
		{"key rejected", teamrelay.ErrKeyRejected, MountStatusKeyRejected},
		{"key expired", teamrelay.ErrKeyExpired, MountStatusKeyExpired},
		{"foreign share", teamrelay.ErrForeignShare, MountStatusForeignShare},
		{"unreachable", teamrelay.ErrUnreachable, MountStatusUnreachable},
		{"not found", teamrelay.ErrNotFound, MountStatusShareNotFound},
	}

	seen := map[MountStatus]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setupMountFixture(t, "")
			f.client.indexErr = tc.err

			result, err := f.svc.SyncMount(context.Background(), f.projectID)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tc.expect, result.Status)
			assert.Equal(t, 0, result.Mounted, "a failed run must not silently report having mounted anything")
			seen[result.Status] = true
		})
	}

	assert.Len(t, seen, len(cases), "all sentinel outcomes must be pairwise distinct — a shared status defeats AC-4")
	assert.NotContains(t, seen, MountStatusOK, "none of these failures may present as ok")
}

func TestSyncMount_NotConfigured_IsItsOwnDistinctState(t *testing.T) {
	// No Team Relay integration for this project at all — an ordinary state,
	// not a failure, and NOT the same status as "mounted zero documents".
	projectID := uuid.New()
	repo := NewMockDocumentRepository()
	storage := NewMockStorageClient()
	piRepo := NewMockProjectIntegrationRepository()
	piService := NewProjectIntegrationService(piRepo)
	svc := NewTeamRelayMountService(repo, storage, piService, piRepo, WithRelaySyncClient(&fakeRelaySyncClient{}), WithKeyDescriber(&fakeKeyDescriber{}))

	result, err := svc.SyncMount(context.Background(), projectID)
	require.NoError(t, err)
	assert.Equal(t, MountStatusNotConfigured, result.Status)
}

// ---------------------------------------------------------------------------
// AC-1 + AC-5: mounting materializes the share as a subtree, and the SAME
// function handles both mount modes — root (DocsMountPath empty) and a
// configured subtree. The two runs below use identical share contents; the
// only difference in the resulting tree is the extra ancestor the subtree
// mode adds, which is exactly what one shared code path predicts.
// ---------------------------------------------------------------------------

func shareFixtureEntries() []teamrelay.SyncIndexEntry {
	return []teamrelay.SyncIndexEntry{
		{Path: "Welcome.md", SHA256: "sha-welcome", Size: 10, Type: "doc"},
		{Path: "Notes/Plan.md", SHA256: "sha-plan", Size: 20, Type: "doc"},
	}
}

func TestSyncMount_RootMode_MaterializesSubtreeAtProjectRoot(t *testing.T) {
	f := setupMountFixture(t, "")
	f.client.indexResult = shareFixtureEntries()
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
	}

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 2, result.Mounted)

	welcome, err := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Welcome.md")
	require.NoError(t, err)
	require.NotNil(t, welcome)
	assert.Nil(t, welcome.ParentID, "a top-level share file with no mount path must land at the project root")

	plan, err := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Notes/Plan.md")
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.NotNil(t, plan.ParentID)
	notesFolder, err := f.repo.GetByID(context.Background(), *plan.ParentID)
	require.NoError(t, err)
	require.NotNil(t, notesFolder)
	assert.Equal(t, "Notes", notesFolder.Title)
	assert.Nil(t, notesFolder.ParentID, "the Notes folder itself sits at the project root in root mode")
	assert.Equal(t, domain.DocumentSourceOwn, notesFolder.SourceKind, "a directory placeholder is not a copy of anything")
}

func TestSyncMount_SubtreeMode_GraftsUnderConfiguredMountPoint(t *testing.T) {
	f := setupMountFixture(t, "Team Relay")
	f.client.indexResult = shareFixtureEntries()
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
	}

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 2, result.Mounted)

	welcome, err := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Welcome.md")
	require.NoError(t, err)
	require.NotNil(t, welcome)
	require.NotNil(t, welcome.ParentID, "in subtree mode even a top-level share file must sit under the mount point, not at the project root")

	mountRoot, err := f.repo.GetByID(context.Background(), *welcome.ParentID)
	require.NoError(t, err)
	require.NotNil(t, mountRoot)
	assert.Equal(t, "Team Relay", mountRoot.Title)
	assert.Nil(t, mountRoot.ParentID)

	plan, err := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Notes/Plan.md")
	require.NoError(t, err)
	require.NotNil(t, plan)
	notesFolder, err := f.repo.GetByID(context.Background(), *plan.ParentID)
	require.NoError(t, err)
	require.NotNil(t, notesFolder)
	assert.Equal(t, "Notes", notesFolder.Title)
	require.NotNil(t, notesFolder.ParentID, "Notes must sit under the mount root, not the project root, in subtree mode")
	assert.Equal(t, mountRoot.ID, *notesFolder.ParentID)
}

// SyncMount is re-runnable: an already-mounted entry is skipped, never
// duplicated, and the relay is not asked to download it again.
func TestSyncMount_ReRun_IsIdempotentAndDoesNotRedownload(t *testing.T) {
	f := setupMountFixture(t, "")
	f.client.indexResult = shareFixtureEntries()
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		// The download reports the hash the index advertised, as a real relay
		// does — a copy whose stored hash differs from the index's is, by
		// design, refreshed on the next sync.
		sha := ""
		for _, e := range shareFixtureEntries() {
			if e.Path == path {
				sha = e.SHA256
			}
		}
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: sha}, nil
	}

	first, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, 2, first.Mounted)
	callsAfterFirst := f.client.callCount()

	second, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	assert.Equal(t, 0, second.Mounted, "nothing new to mount on a re-run")
	assert.Equal(t, 2, second.Skipped)
	assert.Equal(t, callsAfterFirst, f.client.callCount(), "an already-mounted file must not be downloaded again")
}

// Red control for task 3e6f8029-88e2-41b4-8dfc-e60a19ca78dc: SyncMount is the
// actual source of the 140 corrupt documents found on prod — it downloaded
// whatever bytes a files-index entry pointed at and stored them as a document
// body with no check that they were text. A share with a PNG entry among
// ordinary markdown files must skip that one entry (counted as Rejected) and
// still mount the rest — not abort the whole sync on the first bad file,
// which would trade "140 corrupt documents" for "sync looks completely
// broken" and is exactly the overcorrection this fix must not make.
func TestSyncMount_BinaryEntry_SkippedNotMountedAndSyncContinues(t *testing.T) {
	f := setupMountFixture(t, "")
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Welcome.md", SHA256: "sha-welcome", Size: 10, Type: "doc"},
		{Path: "screenshots/hero-1440.png", SHA256: "sha-png", Size: 12345, Type: "doc"},
		{Path: "Notes/Plan.md", SHA256: "sha-plan", Size: 20, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		if strings.HasSuffix(path, ".png") {
			return &teamrelay.SyncDocument{Content: []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 40)), SHA256: "sha-for-" + path}, nil
		}
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
	}

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	assert.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 2, result.Mounted, "the two markdown entries must still mount")
	assert.Equal(t, 1, result.Rejected, "the PNG entry must be counted as rejected, not silently dropped")
	assert.Equal(t, 0, result.Skipped)

	welcome, err := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Welcome.md")
	require.NoError(t, err)
	require.NotNil(t, welcome, "an entry after the rejected one in sorted order must still be reached")

	png, err := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "screenshots/hero-1440.png")
	require.NoError(t, err)
	assert.Nil(t, png, "the binary entry must never become a document row")
}

// RefreshIfStale's write path shares the same gap: if a source that was
// legitimately text ever returns binary content on refresh, the existing
// (good) body must be left alone rather than overwritten — the failure mode
// this guards is silent corruption of a document that used to be fine.
func TestRefreshIfStale_SourceTurnedBinary_RefusesAndKeepsOldBody(t *testing.T) {
	f := setupMountFixture(t, "")
	share, path, sha := testRelayShareID, "Notes/Plan.md", "sha-old"
	syncedAt := frozenTime.Add(-time.Hour)
	doc := &domain.Document{
		ID:           uuid.New(),
		ProjectID:    f.projectID,
		StorageKey:   "documents/" + f.projectID.String() + "/copy.md",
		SourceKind:   domain.DocumentSourceTeamRelay,
		SourceShare:  &share,
		SourcePath:   &path,
		SourceSHA256: &sha,
		SyncedAt:     &syncedAt,
	}
	require.NoError(t, f.storage.Upload(context.Background(), doc.StorageKey, strings.NewReader("old good markdown"), 18, "text/markdown"))

	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		return &teamrelay.SyncDocument{Content: []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 40)), SHA256: "sha-new-binary"}, nil
	}

	err := f.svc.RefreshIfStale(context.Background(), doc)
	require.Error(t, err)
	assert.Equal(t, "sha-old", *doc.SourceSHA256, "must not adopt the new hash when the refresh is refused")
	assert.Equal(t, "old good markdown", string(f.storage.objects[doc.StorageKey]), "the existing body must survive a refused refresh")
}

// ---------------------------------------------------------------------------
// #218d5847 AC4 — the producers Bill's 2026-08-26 correction found unwired:
// R5-A built the schema and the UI, but nothing ever called RecordSyncCheck
// or SetKeyExpiry, so key_expires_at/last_sync_status stayed NULL forever
// for every real integration. These tests are the positive/negative pair
// Bill's own acceptance text demanded: "интеграция с заведомо истекающим
// ключом показывает предупреждение, интеграция со свежим — не показывает" —
// proven here at the layer that actually produces the data the handler's
// existing KeyExpiringSoon computation reads, not by seeding SQL by hand.
// ---------------------------------------------------------------------------

// TestSyncMount_Success_RecordsOkAndFetchesKeyExpiry is the POSITIVE case:
// a successful sync records last_sync_status="ok" AND asks Team Relay's own
// self-describe endpoint for the key's real expiry, storing it with
// key_expiry_source="source" — a fact from the relay, not a manual claim.
func TestSyncMount_Success_RecordsOkAndFetchesKeyExpiry(t *testing.T) {
	f := setupMountFixture(t, "")
	f.client.indexResult = nil // empty share is fine; only the accounting matters here

	soon := timeNow().Add(3 * 24 * time.Hour) // "заведомо истекающий" — inside any reasonable warning window
	describer := &fakeKeyDescriber{desc: &teamrelay.AgentKeyDescription{ExpiresAt: &soon, Scopes: []string{"read", "write"}}}
	f.svc.keyDescriber = describer

	_, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)

	pi, getErr := f.piRepo.Get(context.Background(), f.projectID, "team_relay")
	require.NoError(t, getErr)
	require.NotNil(t, pi.LastSyncCheckedAt, "a real attempt was made — this must not stay unset")
	require.NotNil(t, pi.LastSyncStatus)
	assert.Equal(t, "ok", *pi.LastSyncStatus)
	assert.Nil(t, pi.LastSyncError)
	require.NotNil(t, pi.KeyExpiresAt, "the fact fetched from Team Relay must be stored, not discarded")
	assert.WithinDuration(t, soon, *pi.KeyExpiresAt, time.Second)
	require.NotNil(t, pi.KeyExpirySource)
	assert.Equal(t, "source", *pi.KeyExpirySource, "must be the schema's own 'source' value, not an invented string that would violate the DB CHECK constraint")
	assert.Equal(t, 1, describer.callCount())
}

// TestSyncMount_KeyExpired_RecordsKeyExpiredStatusAndSkipsDescribe is the
// NEGATIVE case: when the sync call itself fails because the key expired,
// last_sync_status must read "key_expired" — not the generic "error" that
// left every real integration looking identical to a misconfigured one before
// this fix — and the introspection endpoint must NOT be called (the same
// auth check would just reject it too; calling it anyway would double-log an
// identical failure and gives nothing to store).
func TestSyncMount_KeyExpired_RecordsKeyExpiredStatusAndSkipsDescribe(t *testing.T) {
	f := setupMountFixture(t, "")
	f.client.indexErr = teamrelay.ErrKeyExpired
	describer := &fakeKeyDescriber{}
	f.svc.keyDescriber = describer

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	assert.Equal(t, MountStatusKeyExpired, result.Status)

	pi, getErr := f.piRepo.Get(context.Background(), f.projectID, "team_relay")
	require.NoError(t, getErr)
	require.NotNil(t, pi.LastSyncStatus)
	assert.Equal(t, "key_expired", *pi.LastSyncStatus, "must be distinguishable from a generic error — this is the whole point of AC4")
	assert.Nil(t, pi.KeyExpiresAt, "no fact was obtained — nothing must be written to key_expires_at")
	assert.Equal(t, 0, describer.callCount(), "a key that just failed must not be asked to describe itself in the same breath")
}

// TestRefreshIfStale_Success_RecordsOkAndFetchesKeyExpiry mirrors the SyncMount
// positive case above, on the OTHER producer path — the one that actually
// fires on ordinary document opens, which is what makes AC4's "заранее, а не
// в момент, когда дерево уже пустое" achievable without anyone clicking
// "Sync now".
func TestRefreshIfStale_Success_RecordsOkAndFetchesKeyExpiry(t *testing.T) {
	f := setupMountFixture(t, "")
	stale := timeNow().Add(-2 * DefaultTeamRelaySyncTTL)
	doc := f.seedCopy(t, "notes/a.md", "body-a", "sha-a", stale)

	soon := timeNow().Add(3 * 24 * time.Hour)
	describer := &fakeKeyDescriber{desc: &teamrelay.AgentKeyDescription{ExpiresAt: &soon}}
	f.svc.keyDescriber = describer

	require.NoError(t, f.svc.RefreshIfStale(context.Background(), doc))

	pi, getErr := f.piRepo.Get(context.Background(), f.projectID, "team_relay")
	require.NoError(t, getErr)
	require.NotNil(t, pi.LastSyncStatus)
	assert.Equal(t, "ok", *pi.LastSyncStatus)
	require.NotNil(t, pi.KeyExpiresAt)
	assert.WithinDuration(t, soon, *pi.KeyExpiresAt, time.Second)
	require.NotNil(t, pi.KeyExpirySource)
	assert.Equal(t, "source", *pi.KeyExpirySource)
}

// TestRefreshIfStale_KeyExpired_RecordsKeyExpiredStatus mirrors the SyncMount
// negative case above on the RefreshIfStale path.
func TestRefreshIfStale_KeyExpired_RecordsKeyExpiredStatus(t *testing.T) {
	f := setupMountFixture(t, "")
	stale := timeNow().Add(-2 * DefaultTeamRelaySyncTTL)
	doc := f.seedCopy(t, "notes/a.md", "body-a", "sha-a", stale)
	f.client.downloadErr = teamrelay.ErrKeyExpired
	describer := &fakeKeyDescriber{}
	f.svc.keyDescriber = describer

	err := f.svc.RefreshIfStale(context.Background(), doc)
	require.Error(t, err, "the document refresh itself must still surface the failure to its caller")

	pi, getErr := f.piRepo.Get(context.Background(), f.projectID, "team_relay")
	require.NoError(t, getErr)
	require.NotNil(t, pi.LastSyncStatus)
	assert.Equal(t, "key_expired", *pi.LastSyncStatus)
	assert.Equal(t, 0, describer.callCount())
}

// ---------------------------------------------------------------------------
// RefreshSleepingKeyExpiries (#bab2e6be)
// ---------------------------------------------------------------------------

// TestRefreshSleepingKeyExpiries_NeverTouched_GetsKeyExpiryWithoutAnyUserAction
// is the task's AC1: an integration that has NEVER gone through RefreshIfStale
// or SyncMount — nobody opened a document, nobody clicked "Sync now" — still
// gets key_expires_at populated by this sweep alone. LastSyncCheckedAt/Status
// staying nil throughout is the proof this path is independent of both
// existing producers, not a redundant third caller of the same code.
func TestRefreshSleepingKeyExpiries_NeverTouched_GetsKeyExpiryWithoutAnyUserAction(t *testing.T) {
	f := setupMountFixture(t, "")

	pre, getErr := f.piRepo.Get(context.Background(), f.projectID, "team_relay")
	require.NoError(t, getErr)
	require.Nil(t, pre.KeyExpiresAt, "fixture precondition: a genuinely untouched integration")
	require.Nil(t, pre.LastSyncCheckedAt, "fixture precondition: never synced")

	soon := timeNow().Add(3 * 24 * time.Hour)
	describer := &fakeKeyDescriber{desc: &teamrelay.AgentKeyDescription{ExpiresAt: &soon}}
	f.svc.keyDescriber = describer

	checked, updated, err := f.svc.RefreshSleepingKeyExpiries(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, checked)
	assert.Equal(t, 1, updated)

	pi, getErr := f.piRepo.Get(context.Background(), f.projectID, "team_relay")
	require.NoError(t, getErr)
	require.NotNil(t, pi.KeyExpiresAt, "the fact fetched from Team Relay must be stored — this is the whole point of the task")
	assert.WithinDuration(t, soon, *pi.KeyExpiresAt, time.Second)
	require.NotNil(t, pi.KeyExpirySource)
	assert.Equal(t, "source", *pi.KeyExpirySource)
	assert.Nil(t, pi.LastSyncCheckedAt, "this sweep must not masquerade as a sync attempt — it never touched a document or the relay's file-index")
	assert.Nil(t, pi.LastSyncStatus)
}

// TestRefreshSleepingKeyExpiries_Unreachable_DoesNotFlagHealthyIntegration is
// the task's AC2, the red control: Team Relay being unreachable is "we could
// not ask", not "we asked and the key is bad". A healthy integration must
// come out of a failed sweep exactly as healthy as it went in — nil stays
// nil, not flipped to an "error" status that would read as a real problem to
// anyone looking at the integrations page.
func TestRefreshSleepingKeyExpiries_Unreachable_DoesNotFlagHealthyIntegration(t *testing.T) {
	f := setupMountFixture(t, "")
	describer := &fakeKeyDescriber{err: teamrelay.ErrUnreachable}
	f.svc.keyDescriber = describer

	checked, updated, err := f.svc.RefreshSleepingKeyExpiries(context.Background())
	require.NoError(t, err, "one integration's failure must not fail the whole sweep")
	assert.Equal(t, 1, checked)
	assert.Equal(t, 0, updated)

	pi, getErr := f.piRepo.Get(context.Background(), f.projectID, "team_relay")
	require.NoError(t, getErr)
	assert.Nil(t, pi.KeyExpiresAt, "no fact was obtained — nothing must be written")
	assert.Nil(t, pi.LastSyncStatus, "unreachable must not be recorded as a status at all — RecordSyncCheck is never called from this path")
	assert.Nil(t, pi.LastSyncError)
}

// TestRefreshSleepingKeyExpiries_DisabledIntegration_IsNeverChecked confirms
// the sweep only ever asks about integrations someone has actually turned on
// — a disabled row (someone unplugged Team Relay for this project) must not
// generate outbound traffic or a describe-key call on its behalf.
func TestRefreshSleepingKeyExpiries_DisabledIntegration_IsNeverChecked(t *testing.T) {
	f := setupMountFixture(t, "")
	require.NoError(t, f.piRepo.Upsert(context.Background(), &domain.ProjectIntegration{
		ID:        uuid.New(),
		ProjectID: uuid.New(),
		Type:      "team_relay",
		Enabled:   false,
		Settings:  []byte(`{"share_id":"` + testRelayShareID + `"}`),
		AgentKey:  "tr_agent_secret_at_least_20_chars",
	}))
	describer := &fakeKeyDescriber{desc: &teamrelay.AgentKeyDescription{}}
	f.svc.keyDescriber = describer

	checked, updated, err := f.svc.RefreshSleepingKeyExpiries(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, checked, "only the ONE enabled integration from the fixture, not the disabled one just added")
	assert.Equal(t, 1, updated)
	assert.Equal(t, 1, describer.callCount())
}

// ---------------------------------------------------------------------------
// Excluded paths (card #4e97f465 A1): SyncMount must not materialize entries
// whose FIRST path segment is in the exclusion list. The default is
// ["artifacts"] — card artifacts already live on the card's artifacts tab.
// ---------------------------------------------------------------------------

// setExcludedPaths rewrites the project's settings with an explicit exclusion
// list, preserving the fixture's share id. nil would mean "never configured"
// on the read side, so tests that want "no exclusions" pass []string{}.
func (f *mountFixture) setExcludedPaths(t *testing.T, paths []string) {
	t.Helper()
	pi, err := f.piRepo.Get(context.Background(), f.projectID, "team_relay")
	require.NoError(t, err)
	settingsJSON, err := json.Marshal(domain.TeamRelaySettings{
		ShareID:       testRelayShareID,
		ExcludedPaths: paths,
	})
	require.NoError(t, err)
	pi.Settings = settingsJSON
	require.NoError(t, f.piRepo.Upsert(context.Background(), pi))
}

// setSyncIntervalMinutes rewrites the project's scheduled-sync interval.
// nil means "never configured" (default cadence); 0 means "off".
func (f *mountFixture) setSyncIntervalMinutes(t *testing.T, minutes *int) {
	t.Helper()
	pi, err := f.piRepo.Get(context.Background(), f.projectID, "team_relay")
	require.NoError(t, err)
	settingsJSON, err := json.Marshal(domain.TeamRelaySettings{
		ShareID:             testRelayShareID,
		SyncIntervalMinutes: minutes,
	})
	require.NoError(t, err)
	pi.Settings = settingsJSON
	require.NoError(t, f.piRepo.Upsert(context.Background(), pi))
}

// advanceClock moves the service's clock forward by d for the remainder of
// the test. Registered AFTER the fixture froze timeNow, so cleanup unwinds in
// the right order (the fixture's own restore runs last).
func (f *mountFixture) advanceClock(t *testing.T, d time.Duration) {
	t.Helper()
	prev := timeNow
	timeNow = func() time.Time { return frozenTime.Add(d) }
	t.Cleanup(func() { timeNow = prev })
}

func TestSyncMount_DefaultExcludesArtifacts(t *testing.T) {
	f := setupMountFixture(t, "")
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Welcome.md", SHA256: "sha-welcome", Size: 10, Type: "doc"},
		{Path: "artifacts/card1/log.md", SHA256: "sha-log", Size: 10, Type: "doc"},
		{Path: "artifacts/card2/report.md", SHA256: "sha-report", Size: 10, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
	}

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 1, result.Mounted, "only the non-excluded entry mounts")
	assert.Equal(t, 2, result.Excluded, "both artifacts entries are counted as excluded")

	for _, p := range []string{"artifacts/card1/log.md", "artifacts/card2/report.md"} {
		cp, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, p)
		require.NoError(t, gerr)
		assert.Nil(t, cp, "excluded entry %q must not materialize as a document", p)
	}
}

// TestSyncMount_EmptyExclusionList_MountsArtifacts is the RED CONTROL for A1:
// with an explicitly empty exclusion list, artifacts/ mounts like any other
// folder. This is what proves the exclusion is a setting the read side
// applies, not a hard-coded filter — delete the nil-vs-empty distinction in
// effectiveExcludedPaths and this test goes red while
// TestSyncMount_DefaultExcludesArtifacts stays green.
func TestSyncMount_EmptyExclusionList_MountsArtifacts(t *testing.T) {
	f := setupMountFixture(t, "")
	f.setExcludedPaths(t, []string{})
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Welcome.md", SHA256: "sha-welcome", Size: 10, Type: "doc"},
		{Path: "artifacts/card1/log.md", SHA256: "sha-log", Size: 10, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
	}

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 2, result.Mounted, "an empty exclusion list must mount artifacts/ too — this is the red control")
	assert.Equal(t, 0, result.Excluded)
}

// TestSyncMount_CustomExclusionReplacesDefault pins that a configured list is
// used AS GIVEN, not unioned with the default: excluding "Brandkit" stops
// excluding "artifacts".
func TestSyncMount_CustomExclusionReplacesDefault(t *testing.T) {
	f := setupMountFixture(t, "")
	f.setExcludedPaths(t, []string{"Brandkit"})
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Welcome.md", SHA256: "sha-welcome", Size: 10, Type: "doc"},
		{Path: "artifacts/card1/log.md", SHA256: "sha-log", Size: 10, Type: "doc"},
		{Path: "Brandkit/logo.png", SHA256: "sha-logo", Size: 10, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
	}

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 2, result.Mounted, "artifacts mounts: the custom list replaced the default, it did not extend it")
	assert.Equal(t, 1, result.Excluded, "only Brandkit is excluded")
}

// TestSyncMount_ExclusionMatchesFirstSegmentNotSubstring: "artifacts" must
// not eat "my-artifacts/x.md" or "docs/artifacts-intro.md" — first segment,
// never substring.
func TestSyncMount_ExclusionMatchesFirstSegmentNotSubstring(t *testing.T) {
	f := setupMountFixture(t, "")
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "my-artifacts/x.md", SHA256: "sha-x", Size: 10, Type: "doc"},
		{Path: "docs/artifacts-intro.md", SHA256: "sha-intro", Size: 10, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
	}

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 2, result.Mounted)
	assert.Equal(t, 0, result.Excluded)
}

// ---------------------------------------------------------------------------
// Archive of vanished copies (card #4e97f465 A2): a copy whose source file is
// gone from the share (deleted or moved there) is soft-deleted on sync — the
// reversible штатный mechanism, not a row drop — so Docs stops listing a page
// backed by nothing. Guards: an EMPTY index or an index error never archives.
// ---------------------------------------------------------------------------

func TestSyncMount_ArchivesCopyWhoseSourceVanished(t *testing.T) {
	f := setupMountFixture(t, "")
	gone := f.seedCopy(t, "Old Path/a.md", "old body", "sha-a", frozenTime)
	kept := f.seedCopy(t, "New Path/b.md", "kept body", "sha-b", frozenTime)
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "New Path/b.md", SHA256: "sha-b", Size: 9, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		if path == "Old Path/a.md" {
			return nil, teamrelay.ErrNotFound
		}
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
	}

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 1, result.Skipped, "the surviving entry is an existing copy")
	assert.Equal(t, 1, result.Archived, "the vanished entry's copy is archived")

	// Soft delete, not a row drop: the row stays with deleted_at stamped —
	// reversible by design, and the uq_documents_source slot it held is freed.
	stored, ok := f.repo.items[gone.ID]
	require.True(t, ok, "archived copy must remain in storage — archive, not delete")
	require.NotNil(t, stored.DeletedAt, "archived copy must carry deleted_at")

	live, gerr := f.repo.GetByID(context.Background(), kept.ID)
	require.NoError(t, gerr)
	require.NotNil(t, live, "the copy whose source still exists stays live")
}

// TestSyncMount_MovedFilesArchiveOldCopies is the live Mesh-dev scenario this
// card was written around: the share's folders move, the old copies (78 on
// stale paths at the time) must not linger as live documents while the new
// paths mount fresh.
func TestSyncMount_MovedFilesArchiveOldCopies(t *testing.T) {
	f := setupMountFixture(t, "")
	for i := 0; i < 78; i++ {
		p := fmt.Sprintf("Mesh/Old Home/card-%d.md", i)
		f.seedCopy(t, p, "old body "+p, "sha-old-"+strconv.Itoa(i), frozenTime)
	}
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Mesh/Mesh dev/card-0.md", SHA256: "sha-n0", Size: 10, Type: "doc"},
		{Path: "Mesh/Mesh dev/card-1.md", SHA256: "sha-n1", Size: 10, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		if strings.Contains(path, "Old Home") {
			return nil, teamrelay.ErrNotFound
		}
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
	}

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 2, result.Mounted, "files at the new paths mount")
	assert.Equal(t, 78, result.Archived, "every copy on an old path is archived")

	live, err := f.repo.ListLiveCopiesByShare(context.Background(), f.projectID, testRelayShareID)
	require.NoError(t, err)
	assert.Len(t, live, 2, "only the two new-path copies remain live")
}

// TestSyncMount_EmptyIndexArchivesNothing is the guard: a files-index that
// comes back EMPTY must not read as "every file was deleted". No index, no
// verdict — even though live copies exist that no entry represents.
func TestSyncMount_EmptyIndexArchivesNothing(t *testing.T) {
	f := setupMountFixture(t, "")
	doc := f.seedCopy(t, "Notes/a.md", "body", "sha-a", frozenTime)
	f.client.indexResult = nil // relay answered, with nothing

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 0, result.Archived, "an empty index must never archive")

	live, gerr := f.repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, gerr)
	require.NotNil(t, live, "copy stays live despite representing no index entry")
}

// TestSyncMount_IndexErrorArchivesNothing is the guard's other half: an
// unreachable relay (or a rejected key) must not archive the project's Docs
// subtree — SyncMount returns the classified status before the archive pass.
func TestSyncMount_IndexErrorArchivesNothing(t *testing.T) {
	f := setupMountFixture(t, "")
	doc := f.seedCopy(t, "Notes/a.md", "body", "sha-a", frozenTime)
	f.client.indexResult = nil
	f.client.indexErr = teamrelay.ErrUnreachable

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusUnreachable, result.Status)
	assert.Equal(t, 0, result.Archived)

	live, gerr := f.repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, gerr)
	require.NotNil(t, live)
}

// TestSyncMount_ArchivesCopiesUnderNewlyExcludedFolder: adding a folder to
// the exclusion list archives copies mounted before the exclusion existed —
// otherwise they would linger as zombies the setting was written to remove.
func TestSyncMount_ArchivesCopiesUnderNewlyExcludedFolder(t *testing.T) {
	f := setupMountFixture(t, "")
	// Mounted back when the exclusion list was explicitly empty.
	art1 := f.seedCopy(t, "artifacts/card1/log.md", "log", "sha-log", frozenTime)
	_ = f.seedCopy(t, "artifacts/card2/report.md", "report", "sha-report", frozenTime)
	// Now the default exclusion (["artifacts"]) is in force and the share still
	// lists the files — they are excluded, not gone.
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "artifacts/card1/log.md", SHA256: "sha-log", Size: 10, Type: "doc"},
		{Path: "artifacts/card2/report.md", SHA256: "sha-report", Size: 10, Type: "doc"},
	}

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 2, result.Excluded)
	assert.Equal(t, 2, result.Archived, "previously-mounted excluded copies are archived, not left as zombies")

	stored, ok := f.repo.items[art1.ID]
	require.True(t, ok)
	require.NotNil(t, stored.DeletedAt)
}

// TestSyncMount_VanishedCopyRemountsWhenFileReturns pins the reversibility
// that made SoftDelete the right archive mechanism: soft-deleting the copy
// freed its uq_documents_source slot, so the same path returning to the share
// mounts a fresh copy instead of colliding with the archived row.
func TestSyncMount_VanishedCopyRemountsWhenFileReturns(t *testing.T) {
	f := setupMountFixture(t, "")
	f.seedCopy(t, "Notes/back.md", "old body", "sha-old", frozenTime)

	// Sync 1: the file is gone (another file remains, so the index is
	// legitimately non-empty — the empty-index guard must not swallow this).
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Notes/other.md", SHA256: "sha-other", Size: 10, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		if path == "Notes/back.md" {
			return nil, teamrelay.ErrNotFound
		}
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
	}
	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Archived)

	// Sync 2: the file is back — a fresh copy mounts on the freed source slot.
	// (other.md stays in the index; dropping it here would archive it.)
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Notes/other.md", SHA256: "sha-other", Size: 10, Type: "doc"},
		{Path: "Notes/back.md", SHA256: "sha-new", Size: 8, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		return &teamrelay.SyncDocument{Content: []byte("new body"), SHA256: "sha-new"}, nil
	}
	result, err = f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 1, result.Mounted, "the returned file mounts fresh")
	assert.Equal(t, 0, result.Archived)

	fresh, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Notes/back.md")
	require.NoError(t, gerr)
	require.NotNil(t, fresh, "a live copy exists for the returned path")
}

// ---------------------------------------------------------------------------
// Scheduled sync (card #4e97f465 A3): SyncDueMounts re-runs the mount for
// enabled integrations whose interval elapsed, without anyone clicking
// "Sync now" or opening a document. Per-project locks keep it from ever
// overlapping a manual sync; interval 0 turns the schedule off.
// ---------------------------------------------------------------------------

func TestSyncDueMounts_NeverSyncedProject_SyncsImmediately(t *testing.T) {
	f := setupMountFixture(t, "")
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Welcome.md", SHA256: "sha-welcome", Size: 10, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
	}

	synced, skipped, err := f.svc.SyncDueMounts(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, synced, "an integration with no last-sync stamp is due immediately — an unmounted share is what the schedule exists to end")
	assert.Equal(t, 0, skipped)
	assert.Equal(t, 1, f.client.filesIndexCalls)

	doc, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Welcome.md")
	require.NoError(t, gerr)
	require.NotNil(t, doc, "the scheduled sync actually mounted the file")
}

func TestSyncDueMounts_RecentlySyncedProject_WaitsForInterval(t *testing.T) {
	f := setupMountFixture(t, "")
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Welcome.md", SHA256: "sha-welcome", Size: 10, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
	}

	// A manual sync just ran (at frozenTime): both scheduling anchors —
	// LastSyncCheckedAt in the DB and nextSyncDue in memory — are fresh.
	_, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)

	synced, skipped, err := f.svc.SyncDueMounts(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, synced, "a project synced a moment ago is not due for a full interval")
	assert.Equal(t, 1, skipped)
	assert.Equal(t, 1, f.client.filesIndexCalls, "no second walk of the share")

	// Fifteen minutes and a second later: due, and the sweep syncs it.
	f.advanceClock(t, DefaultTeamRelaySyncInterval+time.Second)
	synced, skipped, err = f.svc.SyncDueMounts(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, synced)
	assert.Equal(t, 0, skipped)
	assert.Equal(t, 2, f.client.filesIndexCalls)
}

func TestSyncDueMounts_ConfiguredIntervalIsTheCadence(t *testing.T) {
	f := setupMountFixture(t, "")
	minutes := 30
	f.setSyncIntervalMinutes(t, &minutes)
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Welcome.md", SHA256: "sha-welcome", Size: 10, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
	}

	_, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)

	// At +15m the DEFAULT cadence would fire; the configured 30m must not.
	f.advanceClock(t, 16*time.Minute)
	synced, _, err := f.svc.SyncDueMounts(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, synced, "the configured interval, not the default, is the cadence")

	f.advanceClock(t, 31*time.Minute)
	synced, _, err = f.svc.SyncDueMounts(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, synced, "past the configured interval the project is due")
}

func TestSyncDueMounts_IntervalZero_DisablesScheduledSync(t *testing.T) {
	f := setupMountFixture(t, "")
	zero := 0
	f.setSyncIntervalMinutes(t, &zero)
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Welcome.md", SHA256: "sha-welcome", Size: 10, Type: "doc"},
	}

	synced, skipped, err := f.svc.SyncDueMounts(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, synced, "interval 0 is 'scheduled sync off', even for a never-synced project")
	assert.Equal(t, 1, skipped)
	assert.Equal(t, 0, f.client.filesIndexCalls, "the relay is never asked")
}

func TestSyncDueMounts_InFlightSync_IsSkippedNotOverlapped(t *testing.T) {
	f := setupMountFixture(t, "")
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Welcome.md", SHA256: "sha-welcome", Size: 10, Type: "doc"},
	}

	// Hold the project's mount lock, as an in-flight "Sync now" would.
	mu := f.svc.projectMountLock(f.projectID)
	mu.Lock()
	synced, skipped, err := f.svc.SyncDueMounts(context.Background())
	mu.Unlock()

	require.NoError(t, err)
	assert.Equal(t, 0, synced, "a project already syncing is skipped, never queued behind or overlapped")
	assert.Equal(t, 1, skipped)
	assert.Equal(t, 0, f.client.filesIndexCalls, "the sweep must not block on or duplicate the in-flight sync")
}

func TestSyncDueMounts_FailurePacesNextAttemptAtTheInterval(t *testing.T) {
	f := setupMountFixture(t, "")
	f.client.indexResult = nil
	f.client.indexErr = teamrelay.ErrUnreachable

	// First sweep: the attempt runs (and fails) — counted as synced, not
	// skipped: the scheduler DID something, the relay just wasn't there.
	synced, _, err := f.svc.SyncDueMounts(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, synced)
	assert.Equal(t, 1, f.client.filesIndexCalls)

	// An immediate second sweep must NOT retry: a dead relay retried every
	// tick would be a tight loop against a service we don't own.
	synced, skipped, err := f.svc.SyncDueMounts(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, synced)
	assert.Equal(t, 1, skipped)
	assert.Equal(t, 1, f.client.filesIndexCalls)

	// One full interval later it tries again.
	f.advanceClock(t, DefaultTeamRelaySyncInterval+time.Second)
	synced, _, err = f.svc.SyncDueMounts(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, synced)
	assert.Equal(t, 2, f.client.filesIndexCalls)
}

func TestSyncDueMounts_ChangedSourceRefreshesExistingCopy(t *testing.T) {
	f := setupMountFixture(t, "")
	doc := f.seedCopy(t, "Welcome.md", "old body", "sha-old", frozenTime)
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Welcome.md", SHA256: "sha-new", Size: 8, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		return &teamrelay.SyncDocument{Content: []byte("new body"), SHA256: "sha-new"}, nil
	}

	synced, _, err := f.svc.SyncDueMounts(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, synced)

	assert.Equal(t, "new body", string(f.storage.objects[doc.StorageKey]), "the scheduled sync rewrites a copy whose source changed, without anyone opening it")
	got, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Welcome.md")
	require.NoError(t, gerr)
	require.NotNil(t, got.SourceSHA256)
	assert.Equal(t, "sha-new", *got.SourceSHA256)
}

func TestSyncDueMounts_UnchangedSourceIsNotRedownloaded(t *testing.T) {
	f := setupMountFixture(t, "")
	f.seedCopy(t, "Welcome.md", "same body", "sha-same", frozenTime)
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Welcome.md", SHA256: "sha-same", Size: 9, Type: "doc"},
	}

	_, _, err := f.svc.SyncDueMounts(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, f.client.downloadCalls, "a matching hash costs no download — the sweep runs every interval")
}

func TestSyncMount_FailedManualSyncPacesTheScheduler(t *testing.T) {
	f := setupMountFixture(t, "")
	f.client.indexResult = shareFixtureEntries()
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		sha := "sha-welcome"
		if path == "Notes/Plan.md" {
			sha = "sha-plan"
		}
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: sha}, nil
	}

	// A good sync leaves an in-memory deadline one interval out; let it pass,
	// so the project is overdue when the failing manual run comes in.
	_, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	f.advanceClock(t, DefaultTeamRelaySyncInterval+time.Second)

	f.client.indexErr = teamrelay.ErrUnreachable
	_, err = f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	callsAfterManual := f.client.filesIndexCalls

	// The very next sweep must not retry: the failed manual run counts as the
	// attempt, and the next one is a full interval out — not the stale, already
	// elapsed deadline the earlier success left behind.
	synced, skipped, err := f.svc.SyncDueMounts(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, synced)
	assert.Equal(t, 1, skipped)
	assert.Equal(t, callsAfterManual, f.client.filesIndexCalls)
}

// ---------------------------------------------------------------------------
// Image embeds (card #4e97f465 A4): a mounted body's ![[pic.png]] / ![alt](pic.png)
// references resolve against the share, and each referenced image/PDF is copied
// into Mesh attachment storage as a document attachment. The stored body is
// never rewritten — the serve side renders from the attachment records, so
// WriteBack can never push a rewritten link to the original.
// ---------------------------------------------------------------------------

// seedShareWithImage wires a two-file share — one markdown doc, one image next
// to it — whose downloadFn returns real bytes and real hashes, the shape every
// embed test below starts from. Returns the image bytes and their digest.
func seedShareWithImage(f *mountFixture, body string) (img []byte, imgSHA string) {
	img = pngFixture("embed-fixture")
	imgSHA = realSHA(img)
	docSHA := realSHA([]byte(body))

	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Notes/Plan.md", SHA256: docSHA, Size: int64(len(body)), Type: "doc"},
		{Path: "Notes/pic.png", SHA256: imgSHA, Size: int64(len(img)), Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		switch path {
		case "Notes/Plan.md":
			return &teamrelay.SyncDocument{Content: []byte(body), SHA256: docSHA}, nil
		case "Notes/pic.png":
			return &teamrelay.SyncDocument{Content: img, SHA256: imgSHA}, nil
		}
		return nil, fmt.Errorf("no fixture for %q", path)
	}
	return img, imgSHA
}

func TestSyncMount_ImageEmbed_ImportedAsAttachment(t *testing.T) {
	f := setupMountFixture(t, "")
	img, imgSHA := seedShareWithImage(f, "Plan:\n\n![[pic.png|300]]\n\n![shot](pic.png)\n\n![[Plan.md]]\n\n![[https://evil.example/x.png]]\n")

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 1, result.Mounted)

	doc, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Notes/Plan.md")
	require.NoError(t, gerr)
	require.NotNil(t, doc)

	// Both spellings resolve to the SAME share file, so ONE attachment — the
	// .md embed is not an embeddable file, the https URL is not a share path,
	// and neither produces anything.
	atts := f.liveAttachmentsFor(t, doc.ID)
	require.Len(t, atts, 1, "one embedded file, one attachment — syntax repetition and non-file refs change nothing")
	att := atts[0]
	require.NotNil(t, att.SourcePath)
	assert.Equal(t, "Notes/pic.png", *att.SourcePath, "a bare target resolves next to the referencing document first")
	assert.Equal(t, "image/png", att.MimeType)
	assert.Equal(t, int64(len(img)), att.SizeBytes)
	require.NotNil(t, att.SourceSHA256)
	assert.Equal(t, imgSHA, *att.SourceSHA256)

	// The marker is stamped: this body's embed pass ran, a re-sync must not
	// repeat it.
	require.NotNil(t, doc.EmbedSyncedSHA256)
	assert.Equal(t, doc.SourceSHA256, doc.EmbedSyncedSHA256)
}

// The mutation control for the whole pass: with the embed import removed from
// createCopyDocument (or FindImageRefs broken), the body still mounts — but no
// attachment appears and no marker is set, and these assertions go red.
func TestSyncMount_EmbedDedup_NoRedownloadWhenHashUnchanged(t *testing.T) {
	f := setupMountFixture(t, "")
	img, imgSHA := seedShareWithImage(f, "Plan with ![[pic.png]]")

	_, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	assert.Len(t, filter(f.client.downloadPaths, "Notes/pic.png"), 1, "first sync: exactly the embed importer's download — the entry itself is rejected by extension, unfetched")

	// Second sync: doc row exists (Skipped branch), marker matches the body
	// hash and the attachment matches the index hash — nothing may be fetched.
	f.client.downloadPaths = nil
	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Skipped)
	assert.Equal(t, 0, result.Mounted)
	assert.Empty(t, f.client.downloadPaths, "marker and attachment hashes both matched — the relay is not asked for anything")
	_ = img
	_ = imgSHA
}

func TestSyncMount_EmbedSourceChanged_ReplacesAttachment(t *testing.T) {
	f := setupMountFixture(t, "")
	_, _ = seedShareWithImage(f, "Plan with ![[pic.png]]")

	_, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	doc, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Notes/Plan.md")
	require.NoError(t, gerr)
	first := f.liveAttachmentsFor(t, doc.ID)
	require.Len(t, first, 1)

	// The image changes on the share; the doc body does not.
	img2 := pngFixture("changed-on-the-share")
	img2SHA := realSHA(img2)
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Notes/Plan.md", SHA256: *doc.SourceSHA256, Size: 10, Type: "doc"},
		{Path: "Notes/pic.png", SHA256: img2SHA, Size: int64(len(img2)), Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		if path == "Notes/pic.png" {
			return &teamrelay.SyncDocument{Content: img2, SHA256: img2SHA}, nil
		}
		return &teamrelay.SyncDocument{Content: []byte("Plan with ![[pic.png]]"), SHA256: *doc.SourceSHA256}, nil
	}

	_, err = f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)

	atts := f.liveAttachmentsFor(t, doc.ID)
	require.Len(t, atts, 1, "one live attachment for the path — the old row is soft-deleted, not accumulated")
	assert.Equal(t, img2SHA, *atts[0].SourceSHA256)
	assert.NotEqual(t, first[0].ID, atts[0].ID, "the replacement is a new row")

	// The old attachment is soft-deleted, and its bytes are still in storage
	// (soft delete keeps the object; restores must come back intact).
	stored, ok := f.attachments.items[first[0].ID]
	require.True(t, ok)
	require.NotNil(t, stored.DeletedAt, "replaced attachment is soft-deleted, not dropped")
}

func TestSyncMount_EmbedReplacementUploadFails_KeepsOldAttachmentAndRetries(t *testing.T) {
	f := setupMountFixture(t, "")
	_, imgSHA := seedShareWithImage(f, "Plan with ![[pic.png]]")
	_, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	doc, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Notes/Plan.md")
	require.NoError(t, gerr)
	require.Len(t, f.liveAttachmentsFor(t, doc.ID), 1)

	img2 := pngFixture("changed-on-the-share")
	img2SHA := realSHA(img2)
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Notes/Plan.md", SHA256: *doc.SourceSHA256, Size: 10, Type: "doc"},
		{Path: "Notes/pic.png", SHA256: img2SHA, Size: int64(len(img2)), Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		if path == "Notes/pic.png" {
			return &teamrelay.SyncDocument{Content: img2, SHA256: img2SHA}, nil
		}
		return &teamrelay.SyncDocument{Content: []byte("Plan with ![[pic.png]]"), SHA256: *doc.SourceSHA256}, nil
	}

	// The storage backend is down while the replacement is uploaded.
	f.storage.errToReturn = errors.New("storage down")
	_, err = f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)

	atts := f.liveAttachmentsFor(t, doc.ID)
	require.Len(t, atts, 1, "a failed replacement must leave the old attachment live — no live row means nothing for the next sync to retry")
	assert.Equal(t, imgSHA, *atts[0].SourceSHA256, "still the OLD hash, so the next sync sees it as stale")

	// Storage recovers: the next sync replaces it.
	f.storage.errToReturn = nil
	_, err = f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	atts = f.liveAttachmentsFor(t, doc.ID)
	require.Len(t, atts, 1)
	assert.Equal(t, img2SHA, *atts[0].SourceSHA256)
}

func TestSyncMount_EmbedOversize_SkippedWithoutDownload(t *testing.T) {
	f := setupMountFixture(t, "")
	// The image sits under artifacts/ — excluded from DOCUMENT mounting (the
	// default exclusion list) but still resolvable for embeds (syncIndexByPath
	// is built from every entry). That isolation is what makes "zero downloads
	// of this path" attributable to the embed importer alone: without it, the
	// mount loop itself downloads each binary entry once just to reject it.
	img := pngFixture("oversize")
	imgSHA := realSHA(img)
	body := "Plan with ![[artifacts/pic.png]]"
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Notes/Plan.md", SHA256: realSHA([]byte(body)), Size: int64(len(body)), Type: "doc"},
		{Path: "artifacts/pic.png", SHA256: imgSHA, Size: MaxTeamRelayEmbedBytes + 1, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		if path == "artifacts/pic.png" {
			return &teamrelay.SyncDocument{Content: img, SHA256: imgSHA}, nil
		}
		return &teamrelay.SyncDocument{Content: []byte(body), SHA256: realSHA([]byte(body))}, nil
	}

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 1, result.Mounted, "the document itself mounts regardless")
	assert.Equal(t, 1, result.Excluded, "the oversized image's own entry is excluded from mounting")

	assert.Empty(t, filter(f.client.downloadPaths, "artifacts/pic.png"), "size comes from the index — an over-cap embed costs zero bytes")

	doc, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Notes/Plan.md")
	require.NoError(t, gerr)
	assert.Empty(t, f.liveAttachmentsFor(t, doc.ID))
	require.NotNil(t, doc.EmbedSyncedSHA256, "oversize is a permanent skip — the marker is still set, this body is not rescanned for it")
}

func TestSyncMount_EmbedSniffRejectsMislabeledFile(t *testing.T) {
	f := setupMountFixture(t, "")
	fake := []byte("this is markdown wearing a .png extension")
	fakeSHA := realSHA(fake)
	body := "Plan with ![[pic.png]]"
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Notes/Plan.md", SHA256: realSHA([]byte(body)), Size: int64(len(body)), Type: "doc"},
		{Path: "Notes/pic.png", SHA256: fakeSHA, Size: int64(len(fake)), Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		if path == "Notes/pic.png" {
			return &teamrelay.SyncDocument{Content: fake, SHA256: fakeSHA}, nil
		}
		return &teamrelay.SyncDocument{Content: []byte(body), SHA256: realSHA([]byte(body))}, nil
	}

	_, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)

	doc, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Notes/Plan.md")
	require.NoError(t, gerr)
	assert.Empty(t, f.liveAttachmentsFor(t, doc.ID), "the extension claimed an image; the bytes said text — nothing is stored")
	require.NotNil(t, doc.EmbedSyncedSHA256, "the rejection is permanent for this content — the marker is set")
}

func TestSyncMount_EmbedCandidates_RelativeThenRootAndShareRelative(t *testing.T) {
	f := setupMountFixture(t, "")
	imgA, imgASHA := pngFixture("root-logo"), realSHA(pngFixture("root-logo"))
	imgB, imgBSHA := pngFixture("assets-banner"), realSHA(pngFixture("assets-banner"))
	rootBody := "Root embeds ![[logo.png]]"
	notesBody := "Notes embeds ![[assets/banner.png]]"

	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Root.md", SHA256: realSHA([]byte(rootBody)), Size: int64(len(rootBody)), Type: "doc"},
		{Path: "Notes/Doc.md", SHA256: realSHA([]byte(notesBody)), Size: int64(len(notesBody)), Type: "doc"},
		{Path: "logo.png", SHA256: imgASHA, Size: int64(len(imgA)), Type: "doc"},
		{Path: "assets/banner.png", SHA256: imgBSHA, Size: int64(len(imgB)), Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		switch path {
		case "Root.md":
			return &teamrelay.SyncDocument{Content: []byte(rootBody), SHA256: realSHA([]byte(rootBody))}, nil
		case "Notes/Doc.md":
			return &teamrelay.SyncDocument{Content: []byte(notesBody), SHA256: realSHA([]byte(notesBody))}, nil
		case "logo.png":
			return &teamrelay.SyncDocument{Content: imgA, SHA256: imgASHA}, nil
		case "assets/banner.png":
			return &teamrelay.SyncDocument{Content: imgB, SHA256: imgBSHA}, nil
		}
		return nil, fmt.Errorf("no fixture for %q", path)
	}

	_, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)

	root, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Root.md")
	require.NoError(t, gerr)
	atts := f.liveAttachmentsFor(t, root.ID)
	require.Len(t, atts, 1)
	assert.Equal(t, "logo.png", *atts[0].SourcePath, "a bare target in a root document resolves at the share root")

	notes, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Notes/Doc.md")
	require.NoError(t, gerr)
	atts = f.liveAttachmentsFor(t, notes.ID)
	require.Len(t, atts, 1)
	assert.Equal(t, "assets/banner.png", *atts[0].SourcePath, "a target carrying a directory is share-relative, as written")
}

func TestEmbedCandidatesExplicitRelativePaths(t *testing.T) {
	assert.Equal(t, []string{"Notes/images/chart.png", "images/chart.png"}, embedTargetCandidates("../images/chart.png", "Notes/sub"))
	assert.Equal(t, []string{"Notes/sub/pic.png", "pic.png"}, embedTargetCandidates("./pic.png", "Notes/sub"))
	assert.Equal(t, []string{"pic.png"}, embedTargetCandidates("./pic.png", ""))
}

func TestSyncMount_PdfEmbed_Imported(t *testing.T) {
	f := setupMountFixture(t, "")
	pdf := pdfFixture()
	pdfSHA := realSHA(pdf)
	body := "Spec: ![[spec.pdf]]"
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Spec.md", SHA256: realSHA([]byte(body)), Size: int64(len(body)), Type: "doc"},
		{Path: "spec.pdf", SHA256: pdfSHA, Size: int64(len(pdf)), Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		if path == "spec.pdf" {
			return &teamrelay.SyncDocument{Content: pdf, SHA256: pdfSHA}, nil
		}
		return &teamrelay.SyncDocument{Content: []byte(body), SHA256: realSHA([]byte(body))}, nil
	}

	_, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)

	doc, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Spec.md")
	require.NoError(t, gerr)
	atts := f.liveAttachmentsFor(t, doc.ID)
	require.Len(t, atts, 1)
	assert.Equal(t, "application/pdf", atts[0].MimeType)
}

// The skipped-branch backfill: a copy mounted BEFORE embeds existed (marker
// nil) gets its images on the next sync without remounting.
func TestSyncMount_BackfillsEmbedsForPreExistingCopy(t *testing.T) {
	f := setupMountFixture(t, "")
	body := "Plan with ![[pic.png]]"
	img := pngFixture("backfill")
	imgSHA := realSHA(img)
	docSHA := realSHA([]byte(body))
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Notes/Plan.md", SHA256: docSHA, Size: int64(len(body)), Type: "doc"},
		{Path: "Notes/pic.png", SHA256: imgSHA, Size: int64(len(img)), Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		if path == "Notes/pic.png" {
			return &teamrelay.SyncDocument{Content: img, SHA256: imgSHA}, nil
		}
		return &teamrelay.SyncDocument{Content: []byte(body), SHA256: docSHA}, nil
	}

	// A copy that predates A4: live, current hash, no embed marker, body
	// already in storage.
	seeded := f.seedCopy(t, "Notes/Plan.md", body, docSHA, frozenTime)
	seeded.EmbedSyncedSHA256 = nil
	f.repo.items[seeded.ID] = seeded

	_, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)

	atts := f.liveAttachmentsFor(t, seeded.ID)
	require.Len(t, atts, 1, "the pre-existing copy's embed is imported by the backfill pass")
	assert.Equal(t, "Notes/pic.png", *atts[0].SourcePath)
}

// RefreshIfStale's rewrite path re-imports embeds against the NEW body, with
// one files-index call per rewrite (never per freshness check).
func TestRefreshIfStale_HashChanged_ImportsEmbedsForNewBody(t *testing.T) {
	f := setupMountFixture(t, "")
	stale := timeNow().Add(-2 * DefaultTeamRelaySyncTTL)
	doc := f.seedCopy(t, "Notes/Plan.md", "old body", realSHA([]byte("old body")), stale)

	img := pngFixture("after-rewrite")
	imgSHA := realSHA(img)
	newBody := "NEW body with ![[pic.png]]"
	newSHA := realSHA([]byte(newBody))
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		if path == "Notes/pic.png" {
			return &teamrelay.SyncDocument{Content: img, SHA256: imgSHA}, nil
		}
		return &teamrelay.SyncDocument{Content: []byte(newBody), SHA256: newSHA}, nil
	}
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Notes/Plan.md", SHA256: newSHA, Size: int64(len(newBody)), Type: "doc"},
		{Path: "Notes/pic.png", SHA256: imgSHA, Size: int64(len(img)), Type: "doc"},
	}

	require.NoError(t, f.svc.RefreshIfStale(context.Background(), doc))

	atts := f.liveAttachmentsFor(t, doc.ID)
	require.Len(t, atts, 1, "the rewritten body's embed is imported")
	assert.Equal(t, imgSHA, *atts[0].SourceSHA256)
	require.NotNil(t, doc.EmbedSyncedSHA256)
	assert.Equal(t, newSHA, *doc.EmbedSyncedSHA256, "marker names the body whose embeds were just imported")
}

// A dead embed — the referenced file is simply not on the share — imports
// nothing, fails nothing, and still marks the body as processed.
func TestSyncMount_DeadEmbedReference_IsSkippedSilently(t *testing.T) {
	f := setupMountFixture(t, "")
	body := "Plan with ![[missing.png]]"
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Notes/Plan.md", SHA256: realSHA([]byte(body)), Size: int64(len(body)), Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		return &teamrelay.SyncDocument{Content: []byte(body), SHA256: realSHA([]byte(body))}, nil
	}

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	require.Equal(t, MountStatusOK, result.Status)
	assert.Equal(t, 1, result.Mounted)

	doc, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Notes/Plan.md")
	require.NoError(t, gerr)
	assert.Empty(t, f.liveAttachmentsFor(t, doc.ID))
	require.NotNil(t, doc.EmbedSyncedSHA256)
}

// The currency pass's other half: the image is deleted on the share while the
// body that references it stays byte-identical. The attachment is archived —
// the same liveness rule the copies themselves follow.
func TestSyncMount_ImageVanishedFromShare_ArchivesAttachment(t *testing.T) {
	f := setupMountFixture(t, "")
	_, _ = seedShareWithImage(f, "Plan with ![[pic.png]]")

	_, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	doc, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Notes/Plan.md")
	require.NoError(t, gerr)
	require.Len(t, f.liveAttachmentsFor(t, doc.ID), 1)

	// The image is gone; the doc's own hash did not move.
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Notes/Plan.md", SHA256: *doc.SourceSHA256, Size: 10, Type: "doc"},
	}
	_, err = f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)

	assert.Empty(t, f.liveAttachmentsFor(t, doc.ID), "an embed whose source file vanished is archived, not served stale forever")
}

// filter is the test-side of "was this path downloaded": the fake records every
// download path in order, and counting one named path across them is the whole
// question.
func filter(paths []string, want string) []string {
	var out []string
	for _, p := range paths {
		if p == want {
			out = append(out, p)
		}
	}
	return out
}

// TestSyncMount_DoesNotArchiveCopyStillDownloadable: the index lists only
// sync-artifact rows, so "absent from the index" is not "deleted". A copy whose
// file still downloads must survive, and so must one whose probe fails with
// anything but 404 — only a 404 proves the file gone.
func TestSyncMount_DoesNotArchiveCopyStillDownloadable(t *testing.T) {
	f := setupMountFixture(t, "")
	present := f.seedCopy(t, "Notes/present.md", "body", "sha-p", frozenTime)
	flaky := f.seedCopy(t, "Notes/flaky.md", "body", "sha-f", frozenTime)
	gone := f.seedCopy(t, "Notes/gone.md", "body", "sha-g", frozenTime)
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Notes/other.md", SHA256: "sha-other", Size: 10, Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		switch path {
		case "Notes/gone.md":
			return nil, teamrelay.ErrNotFound
		case "Notes/flaky.md":
			return nil, teamrelay.ErrUnreachable
		}
		return &teamrelay.SyncDocument{Content: []byte("body"), SHA256: "sha-for-" + path}, nil
	}

	result, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Archived, "only the 404 copy is archived")
	assert.NotNil(t, f.repo.items[gone.ID].DeletedAt)
	assert.Nil(t, f.repo.items[present.ID].DeletedAt, "downloadable copy stays live")
	assert.Nil(t, f.repo.items[flaky.ID].DeletedAt, "an unproven failure keeps the copy")
}

// TestSyncMount_EmbedReplacementInsertFails_OldAttachmentStaysLive covers the
// window the compensating re-insert used to leave: the retire landed, the
// insert failed, and a failing restore left no live row. Replace is one
// transaction, so a failed insert changes nothing.
func TestSyncMount_EmbedReplacementInsertFails_OldAttachmentStaysLive(t *testing.T) {
	f := setupMountFixture(t, "")
	_, imgSHA := seedShareWithImage(f, "Plan with ![[pic.png]]")
	_, err := f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)
	doc, gerr := f.repo.GetBySourceInProject(context.Background(), f.projectID, testRelayShareID, "Notes/Plan.md")
	require.NoError(t, gerr)
	before := f.liveAttachmentsFor(t, doc.ID)
	require.Len(t, before, 1)

	img2 := pngFixture("changed-on-the-share")
	img2SHA := realSHA(img2)
	f.client.indexResult = []teamrelay.SyncIndexEntry{
		{Path: "Notes/Plan.md", SHA256: *doc.SourceSHA256, Size: 10, Type: "doc"},
		{Path: "Notes/pic.png", SHA256: img2SHA, Size: int64(len(img2)), Type: "doc"},
	}
	f.client.downloadFn = func(path string) (*teamrelay.SyncDocument, error) {
		if path == "Notes/pic.png" {
			return &teamrelay.SyncDocument{Content: img2, SHA256: img2SHA}, nil
		}
		return &teamrelay.SyncDocument{Content: []byte("Plan with ![[pic.png]]"), SHA256: *doc.SourceSHA256}, nil
	}

	f.attachments.createErr = errors.New("insert failed")
	_, err = f.svc.SyncMount(context.Background(), f.projectID)
	require.NoError(t, err)

	after := f.liveAttachmentsFor(t, doc.ID)
	require.Len(t, after, 1)
	assert.Equal(t, before[0].ID, after[0].ID, "the very same old row stays live, not a re-inserted copy")
	assert.Equal(t, imgSHA, *after[0].SourceSHA256)
}
