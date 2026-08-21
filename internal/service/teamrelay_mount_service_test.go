package service

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/integration/teamrelay"
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
	return f.downloadResult, f.downloadErr
}

func (f *fakeRelaySyncClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.downloadCalls
}

var _ RelaySyncClient = (*fakeRelaySyncClient)(nil)

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

const testRelayShareID = "8c5e7efd-0000-0000-0000-000000000000"

type mountFixture struct {
	svc       *teamRelayMountService
	repo      *MockDocumentRepository
	storage   *MockStorageClient
	piRepo    *MockProjectIntegrationRepository
	client    *fakeRelaySyncClient
	projectID uuid.UUID
	wsID      uuid.UUID
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

	svc := NewTeamRelayMountService(repo, storage, piService, WithRelaySyncClient(client)).(*teamRelayMountService)

	return &mountFixture{
		svc:       svc,
		repo:      repo,
		storage:   storage,
		piRepo:    piRepo,
		client:    client,
		projectID: projectID,
		wsID:      wsID,
	}
}

// seedCopy inserts an already-mounted Team Relay copy document, synced at
// syncedAt with body and sha256 as given.
func (f *mountFixture) seedCopy(t *testing.T, sourcePath, body, sha256 string, syncedAt time.Time) *domain.Document {
	t.Helper()
	id := uuid.New()
	key := documentStorageKey(f.projectID, id)
	require.NoError(t, f.storage.Upload(context.Background(), key, strings.NewReader(body), int64(len(body)), documentContentType))

	share := testRelayShareID
	path := sourcePath
	sha := sha256
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

	assert.Len(t, seen, len(cases), "all four sentinel outcomes must be pairwise distinct — a shared status defeats AC-4")
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
	svc := NewTeamRelayMountService(repo, storage, piService, WithRelaySyncClient(&fakeRelaySyncClient{}))

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
		return &teamrelay.SyncDocument{Content: []byte("body for " + path), SHA256: "sha-for-" + path}, nil
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
