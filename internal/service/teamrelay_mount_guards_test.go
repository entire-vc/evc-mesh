package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/integration/teamrelay"
)

// ---------------------------------------------------------------------------
// Guard paths on the OPEN path. Every one of these runs on a plain document
// read, so a guard that wrongly falls through does not fail loudly — it makes
// an ordinary read reach for the network. Each case therefore asserts the
// call counter, not just the returned error.
// ---------------------------------------------------------------------------

func TestRefreshIfStale_NonCopyDocumentsAreLeftAlone(t *testing.T) {
	cases := []struct {
		name string
		doc  *domain.Document
	}{
		{"nil document", nil},
		{"a document of our own", &domain.Document{ID: uuid.New(), SourceKind: domain.DocumentSourceOwn}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setupMountFixture(t, "")
			require.NoError(t, f.svc.RefreshIfStale(context.Background(), tc.doc))
			assert.Equal(t, 0, f.client.callCount(),
				"only Team Relay copies are refreshed — anything else must not cost a network call")
		})
	}
}

// A copy that cannot describe its own source is a data inconsistency the DB's
// chk_documents_source_shape is supposed to make impossible. If it ever occurs
// it must be reported, NOT silently treated as "nothing to refresh" — silence
// here would serve a stale body forever with no signal.
func TestRefreshIfStale_CopyMissingSourceMetadata_IsAnErrorNotASilentNoOp(t *testing.T) {
	share := testRelayShareID
	path := "Notes/Welcome.md"
	sha := "sha-old"
	now := frozenTime

	full := func() *domain.Document {
		return &domain.Document{
			ID: uuid.New(), SourceKind: domain.DocumentSourceTeamRelay,
			SourceShare: &share, SourcePath: &path, SourceSHA256: &sha, SyncedAt: &now,
		}
	}

	cases := map[string]func(*domain.Document){
		"no synced_at":    func(d *domain.Document) { d.SyncedAt = nil },
		"no source share": func(d *domain.Document) { d.SourceShare = nil },
		"no source path":  func(d *domain.Document) { d.SourcePath = nil },
		"no source sha":   func(d *domain.Document) { d.SourceSHA256 = nil },
	}

	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			f := setupMountFixture(t, "")
			doc := full()
			break_(doc)

			err := f.svc.RefreshIfStale(context.Background(), doc)

			require.Error(t, err, "a copy that cannot name its source must not pass silently")
			assert.Contains(t, err.Error(), "missing source metadata")
			assert.Equal(t, 0, f.client.callCount(), "there is nothing to ask the relay for")
		})
	}
}

// ---------------------------------------------------------------------------
// The TTL is settings-driven, not a constant. R5 owns the UI for it; R3 owns
// the default. If syncTTL ignored the configured value, R5 would ship a
// setting that silently does nothing.
// ---------------------------------------------------------------------------

func TestSyncTTL_ConfiguredValueIsHonoured_DefaultOnlyWhenUnset(t *testing.T) {
	assert.Equal(t, DefaultTeamRelaySyncTTL, syncTTL(domain.TeamRelaySettings{}),
		"unset means the R3 default, since R5's UI does not exist yet")
	assert.Equal(t, DefaultTeamRelaySyncTTL, syncTTL(domain.TeamRelaySettings{SyncTTLSeconds: -5}),
		"a negative TTL is nonsense, not an instruction to check on every open")
	assert.Equal(t, 90*time.Second, syncTTL(domain.TeamRelaySettings{SyncTTLSeconds: 90}),
		"a configured TTL must actually be used")
}

func TestRefreshIfStale_UsesTheConfiguredTTL_NotOnlyTheDefault(t *testing.T) {
	f := setupMountFixture(t, "")
	f.setTTLSeconds(t, 60)
	doc := f.seedCopy(t, "Notes/Welcome.md", "old body", "sha-old", frozenTime)

	// 30s in: inside a 60s TTL (and inside the default too) — no call either way.
	timeNow = func() time.Time { return frozenTime.Add(30 * time.Second) }
	require.NoError(t, f.svc.RefreshIfStale(context.Background(), doc))
	assert.Equal(t, 0, f.client.callCount())

	// 90s in: past the configured 60s, but still WELL inside the default TTL.
	// A implementation ignoring the setting would make no call here.
	timeNow = func() time.Time { return frozenTime.Add(90 * time.Second) }
	require.NoError(t, f.svc.RefreshIfStale(context.Background(), doc))
	assert.Equal(t, 1, f.client.callCount(),
		"the configured 60s TTL elapsed — hardcoding the default would have skipped this check")
}

// ---------------------------------------------------------------------------
// Failures on the open path must surface. A refresh that swallows its own
// error would serve a stale body while reporting success.
// ---------------------------------------------------------------------------

func TestRefreshIfStale_RelayFailure_IsReportedNotSwallowed(t *testing.T) {
	f := setupMountFixture(t, "")
	doc := f.seedCopy(t, "Notes/Welcome.md", "old body", "sha-old", frozenTime)
	f.client.downloadErr = teamrelay.ErrKeyRejected
	timeNow = func() time.Time { return frozenTime.Add(DefaultTeamRelaySyncTTL + time.Minute) }

	err := f.svc.RefreshIfStale(context.Background(), doc)

	require.Error(t, err)
	assert.ErrorIs(t, err, teamrelay.ErrKeyRejected,
		"the sentinel must survive wrapping — the caller classifies on it")
}

func TestRefreshIfStale_StorageFailureOnRewrite_IsReported(t *testing.T) {
	f := setupMountFixture(t, "")
	doc := f.seedCopy(t, "Notes/Welcome.md", "old body", "sha-old", frozenTime)
	f.client.downloadResult = &teamrelay.SyncDocument{Content: []byte("NEW body"), SHA256: "sha-new"}
	f.storage.errToReturn = errors.New("bucket full")
	timeNow = func() time.Time { return frozenTime.Add(DefaultTeamRelaySyncTTL + time.Minute) }

	err := f.svc.RefreshIfStale(context.Background(), doc)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bucket full")
	assert.Equal(t, "sha-old", *doc.SourceSHA256,
		"a failed rewrite must not advance the recorded hash — that would make the next open skip the retry")
}

// ---------------------------------------------------------------------------
// "Not configured" must stay distinguishable from every failure. This is the
// AC-4 direction: an unconfigured project is an ordinary state; a rejected key
// is not, and neither may render as "this share has no documents".
// ---------------------------------------------------------------------------

func TestSyncMount_DisabledOrKeylessIntegration_ReadsAsNotConfigured(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*domain.ProjectIntegration)
	}{
		{"integration disabled", func(pi *domain.ProjectIntegration) { pi.Enabled = false }},
		{"no agent key", func(pi *domain.ProjectIntegration) { pi.AgentKey = "" }},
		{"no share id", func(pi *domain.ProjectIntegration) {
			b, _ := json.Marshal(domain.TeamRelaySettings{ShareID: ""})
			pi.Settings = b
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setupMountFixture(t, "")
			pi, err := f.piRepo.Get(context.Background(), f.projectID, "team_relay")
			require.NoError(t, err)
			tc.mutate(pi)
			require.NoError(t, f.piRepo.Upsert(context.Background(), pi))

			result, err := f.svc.SyncMount(context.Background(), f.projectID)

			require.NoError(t, err, "an unconfigured project is an ordinary state, not a failure")
			assert.Equal(t, MountStatusNotConfigured, result.Status)
			assert.Equal(t, 0, f.client.filesIndexCalls, "nothing is configured — nothing to walk")
		})
	}
}

func TestSyncMount_UnparseableSettings_IsAnErrorNotNotConfigured(t *testing.T) {
	f := setupMountFixture(t, "")
	pi, err := f.piRepo.Get(context.Background(), f.projectID, "team_relay")
	require.NoError(t, err)
	pi.Settings = []byte(`{"share_id": [broken`)
	require.NoError(t, f.piRepo.Upsert(context.Background(), pi))

	result, err := f.svc.SyncMount(context.Background(), f.projectID)

	// Corrupt settings are a real fault. Reporting them as "not_configured"
	// would tell an operator to configure something already configured.
	if err == nil {
		assert.NotEqual(t, MountStatusNotConfigured, result.Status,
			"malformed settings are a fault, not an absence of settings")
		assert.Equal(t, MountStatusError, result.Status)
	}
}

// ---------------------------------------------------------------------------
// §3.5: a naive ASCII slugifier collapses distinct Cyrillic folder names onto
// the same slug. Two differently-named folders sharing one slug is a silent
// merge of two subtrees.
// ---------------------------------------------------------------------------

func TestFolderSlug_DistinctNamesNeverCollapseOntoOneSlug(t *testing.T) {
	names := []string{"Заметки", "Проекты", "Архив", "Notes", "...", "!!!"}
	seen := map[string]string{}
	for _, n := range names {
		s := folderSlug(n)
		assert.NotEmpty(t, s, "a folder must always get a usable slug")
		if prev, dup := seen[s]; dup {
			t.Fatalf("folders %q and %q both slugify to %q — two distinct subtrees would merge", prev, n, s)
		}
		seen[s] = n
	}
}

func TestTitleFromFileName_StripsOnlyTheExtension(t *testing.T) {
	assert.Equal(t, "Welcome", titleFromFileName("Welcome.md"))
	assert.Equal(t, "README", titleFromFileName("README"), "no extension means the name is the title")
	assert.Equal(t, "notes.v2", titleFromFileName("notes.v2.md"), "only the final extension is dropped")
	assert.Equal(t, "Заметка", titleFromFileName("Заметка.md"))
}

// Regression: folderSlug's fallback truncated a hex encoding of the raw NAME
// to 16 characters, so any name shorter than 8 bytes with no letter or digit
// ("-", "...", "§") produced a shorter string and panicked on the slice bound.
// A single oddly-named folder anywhere in a share took down the whole mount.
//
// Recovered explicitly rather than letting the panic fail the test: a control
// that dies mid-flight reports a crash, not the property it was written to
// check, and reads identically to a dozen unrelated breakages.
func TestFolderSlug_ShortPunctuationOnlyNames_DoNotPanic(t *testing.T) {
	for _, raw := range []string{"-", "§", "...", "!!!", "()", "—"} {
		t.Run(raw, func(t *testing.T) {
			var slug string
			var panicked any
			func() {
				defer func() { panicked = recover() }()
				slug = folderSlug(raw)
			}()

			require.Nil(t, panicked, "folderSlug(%q) panicked — one odd folder name must not abort the mount", raw)
			assert.NotEmpty(t, slug)
			assert.True(t, strings.HasPrefix(slug, "folder-"), "expected the fallback form, got %q", slug)
			assert.Equal(t, slug, folderSlug(raw), "the fallback must be stable across calls, or re-runs remount the same folder twice")
		})
	}
}
