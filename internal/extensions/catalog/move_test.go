package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// MoveRepoToCatalog mirrors the postgres transaction: guarded on source,
// version and commit; repository columns cleared; token deleted; journal kept.
func (m *installMem) MoveRepoToCatalog(_ context.Context, e *domain.InstalledExtension, g domain.ExtensionStateGuard, j *domain.InstanceExtensionJournalEntry) error {
	r := m.rows[e.ExtensionID]
	if r == nil {
		return apierror.NotFound("extension")
	}
	if r.Source != g.Source || r.Version != g.Version || r.CommitSHA != g.CommitSHA {
		return apierror.Conflict("the extension changed")
	}
	r.Source, r.Version, r.Manifest, r.CommitSHA, r.ArtifactSHA256, r.TrustLevel = domain.ExtensionSourceCatalog, e.Version, e.Manifest, e.CommitSHA, e.ArtifactSHA256, e.TrustLevel
	r.RepoURL, r.RepoRef, r.PreviousVersion = "", "", ""
	r.CatalogScope = scopeName(e.CatalogScope)
	delete(m.snaps, e.ExtensionID)
	delete(m.tokens, e.ExtensionID)
	if j != nil {
		m.moves = append(m.moves, *j)
	}
	return nil
}

const repoSHA = "1111111111111111111111111111111111111111"

// moveFixture is the catalog fixture plus a repository install of the same id
// at the same version (the partner's case: 0.3.0 from GitLab, 0.3.0 in Spark).
func moveFixture(t *testing.T, mutate func(e *Entry, cat *Catalog), rowManifest []byte) *installFixture {
	t.Helper()
	f := newInstallFixture(t, mutate)
	if rowManifest == nil {
		rowManifest = f.manifest
	}
	f.store.rows[f.id()] = &domain.InstalledExtension{
		ExtensionID: f.id(), Version: "0.1.0", Manifest: rowManifest, Source: domain.ExtensionSourceRepo,
		RepoURL: "https://gitlab.example/grp/hello", RepoRef: "v0.1.0", CommitSHA: repoSHA, TrustLevel: domain.ExtensionTrustCustom,
	}
	f.store.tokens = map[string]string{f.id(): "glpat-secret-value"}
	return f
}

func (f *installFixture) moveReq() MoveRequest {
	e := f.inst.Catalog.current.Extensions[0]
	return MoveRequest{ExpectedVersion: "0.1.0", ExpectedCommitSHA: repoSHA, TargetVersion: e.Latest.Version, TargetCommitSHA: e.Latest.CommitSHA}
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	return ae.ErrorCode
}

func TestMoveToCatalog_PlanThenApplySameVersion(t *testing.T) {
	f := moveFixture(t, nil, nil)
	ctx := context.Background()

	plan, err := f.inst.PlanMoveToCatalog(ctx, f.id(), "", "")
	require.NoError(t, err)
	assert.Equal(t, "repo", plan.Current.Source)
	assert.Equal(t, "0.1.0", plan.Current.Version)
	assert.Equal(t, repoSHA, plan.Current.CommitSHA)
	assert.Equal(t, "v0.1.0", plan.Current.RepoRef)
	assert.Equal(t, "catalog", plan.Target.Source)
	assert.Equal(t, "0.1.0", plan.Target.Version, "a move to the same version is allowed")
	assert.Equal(t, "abc123", plan.Target.CommitSHA)
	assert.NotEmpty(t, plan.Target.ArtifactSHA256)
	assert.JSONEq(t, string(f.manifest), string(plan.Target.Manifest), "the target manifest is the one inside the verified artifact")
	assert.Empty(t, plan.AddedCapabilities)
	assert.False(t, plan.NeedsConsent)
	assert.True(t, plan.OneWay)
	assert.Equal(t, domain.ExtensionSourceRepo, f.store.rows[f.id()].Source, "a plan writes nothing")

	res, err := f.inst.MoveToCatalog(ctx, uuid.New(), f.id(), f.moveReq())
	require.NoError(t, err)
	assert.Equal(t, OutcomeMoved, res.Outcome)
	row := f.store.rows[f.id()]
	assert.Equal(t, domain.ExtensionSourceCatalog, row.Source)
	assert.Equal(t, domain.ExtensionTrustLevel("verified"), row.TrustLevel)
	assert.Equal(t, "abc123", row.CommitSHA)
	assert.Equal(t, plan.Target.ArtifactSHA256, row.ArtifactSHA256)
	assert.Empty(t, row.RepoURL)
	assert.Empty(t, row.RepoRef)
	assert.NotContains(t, f.store.tokens, f.id(), "the repository token is deleted")
	require.Len(t, f.store.moves, 1)
	assert.Equal(t, domain.InstanceJournalMovedToCatalog, f.store.moves[0].Action)
	assert.NotContains(t, string(f.store.moves[0].Details), "glpat-secret-value")
	out, _ := json.Marshal(res)
	assert.NotContains(t, string(out), "glpat-secret-value")
	assert.Equal(t, 1, f.changes)

	// A retry after a lost answer acts once and says what happened.
	again, err := f.inst.MoveToCatalog(ctx, uuid.New(), f.id(), f.moveReq())
	require.NoError(t, err)
	assert.Equal(t, OutcomeAlreadyMoved, again.Outcome)
	assert.Len(t, f.store.moves, 1, "no second journal entry")
	assert.Equal(t, 1, f.changes)

	// The row is now on the catalog path: rollback has nothing (one way), and
	// update finds nothing newer.
	_, err = f.inst.Rollback(ctx, nil, f.id())
	assert.Equal(t, CodeRollbackUnavailable, codeOf(t, err))
	up, err := f.inst.Update(ctx, nil, f.id(), "")
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeUpToDate, up.Outcome)
}

// Every refusal leaves the repository row exactly as it was.
func TestMoveToCatalog_RefusalsChangeNothing(t *testing.T) {
	cases := map[string]struct {
		mutate func(e *Entry, cat *Catalog)
		prep   func(f *installFixture, req *MoveRequest)
		code   string
	}{
		"blocked release": {mutate: func(e *Entry, c *Catalog) { c.Blocklist = []Block{{ID: e.ID, Reason: "malware"}} }, code: CodeBlocked},
		"digest mismatch": {prep: func(f *installFixture, _ *MoveRequest) { f.artifact = []byte("tampered") }, code: CodeDigest},
		"no digest":       {mutate: func(e *Entry, _ *Catalog) { e.Latest.ArtifactSHA256 = nil }, code: CodeDigest},
		"yanked":          {mutate: func(e *Entry, _ *Catalog) { e.Latest.Yanked = true }, code: CodeReleaseRevoked},
		"incompatible":    {mutate: func(e *Entry, _ *Catalog) { e.Latest.MeshAPI = ">=7 <9" }, code: repoinstall.CodeIncompatible},
		"not in catalog":  {mutate: func(e *Entry, _ *Catalog) { e.ID = "someone-else" }, code: CodeNotInCatalog},
		"catalog unknown": {prep: func(f *installFixture, _ *MoveRequest) { f.inst.Catalog.current = nil }, code: CodeSparkDown},
		"spark down": {prep: func(f *installFixture, _ *MoveRequest) {
			f.inst.SparkURL = func() string { return "http://127.0.0.1:1" }
		}, code: CodeSparkDown},
		"stale version":    {prep: func(_ *installFixture, r *MoveRequest) { r.ExpectedVersion = "0.0.9" }, code: repoinstall.CodeStateChanged},
		"stale commit":     {prep: func(_ *installFixture, r *MoveRequest) { r.ExpectedCommitSHA = "2222" }, code: repoinstall.CodeStateChanged},
		"wrong target sha": {prep: func(_ *installFixture, r *MoveRequest) { r.TargetCommitSHA = "not-the-release" }, code: repoinstall.CodeStateChanged},
		"unknown version":  {prep: func(_ *installFixture, r *MoveRequest) { r.TargetVersion = "0.1.1" }, code: CodeReleaseRevoked},
		"artifact of another id": {prep: func(f *installFixture, _ *MoveRequest) {
			raw := withManifest(f.t, f, func(m map[string]any) { m["id"] = "other" })
			f.artifact = tarGz(f.t, map[string][]byte{"mesh-extension.json": raw})
			sum := sha256Hex(f.artifact)
			f.inst.Catalog.current.Extensions[0].Latest.ArtifactSHA256 = &sum
		}, code: repoinstall.CodeManifestInvalid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := moveFixture(t, tc.mutate, nil)
			id := f.id()
			before := *f.store.rows[id]
			req := f.moveReq()
			if tc.prep != nil {
				tc.prep(f, &req)
			}
			_, err := f.inst.MoveToCatalog(context.Background(), uuid.New(), id, req)
			require.Error(t, err)
			assert.Equal(t, tc.code, codeOf(t, err))
			assert.Equal(t, before, *f.store.rows[id], "the row is untouched")
			assert.Contains(t, f.store.tokens, id, "the token is kept")
			assert.Empty(t, f.store.moves)
			assert.Zero(t, f.changes)
		})
	}
}

// A version change between the read and the write (another admin, the
// auto-update pass) is caught by the guarded write itself.
type racingStore struct{ *installMem }

func (s racingStore) MoveRepoToCatalog(ctx context.Context, e *domain.InstalledExtension, g domain.ExtensionStateGuard, j *domain.InstanceExtensionJournalEntry) error {
	s.rows[e.ExtensionID].Version, s.rows[e.ExtensionID].CommitSHA = "0.1.1", "3333333333333333333333333333333333333333"
	return s.installMem.MoveRepoToCatalog(ctx, e, g, j)
}

func TestMoveToCatalog_ConcurrentChangeIs409(t *testing.T) {
	f := moveFixture(t, nil, nil)
	f.inst.Store = racingStore{f.store}
	_, err := f.inst.MoveToCatalog(context.Background(), uuid.New(), f.id(), f.moveReq())
	assert.Equal(t, repoinstall.CodeStateChanged, codeOf(t, err))
	row := f.store.rows[f.id()]
	assert.Equal(t, domain.ExtensionSourceRepo, row.Source)
	assert.Empty(t, f.store.moves)
	assert.Zero(t, f.changes)
}

func TestMoveToCatalog_AddedPermissionsNeedConsent(t *testing.T) {
	// The repository install lacks the events permission the catalog release declares.
	f := moveFixture(t, nil, nil)
	f.store.rows[f.id()].Manifest = withManifest(t, f, func(m map[string]any) { delete(m["capabilities"].(map[string]any), "events") })

	plan, err := f.inst.PlanMoveToCatalog(context.Background(), f.id(), "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"events:task.created"}, plan.AddedCapabilities)
	assert.True(t, plan.NeedsConsent)

	req := f.moveReq()
	_, err = f.inst.MoveToCatalog(context.Background(), uuid.New(), f.id(), req)
	assert.Equal(t, repoinstall.CodeConsentRequired, codeOf(t, err))
	assert.Equal(t, domain.ExtensionSourceRepo, f.store.rows[f.id()].Source, "no rights without consent")

	req.ConsentCommitSHA = req.TargetCommitSHA
	res, err := f.inst.MoveToCatalog(context.Background(), uuid.New(), f.id(), req)
	require.NoError(t, err)
	assert.Equal(t, OutcomeMoved, res.Outcome)
	assert.Contains(t, string(f.store.moves[0].Details), `"consented_capabilities":["events:task.created"]`)
}

func TestMoveToCatalog_OnlyRepoRowsAndNoDowngrade(t *testing.T) {
	ctx := context.Background()
	f := moveFixture(t, nil, nil)
	f.store.rows[f.id()].Version = "0.2.0"
	req := f.moveReq()
	req.ExpectedVersion = "0.2.0"
	_, err := f.inst.MoveToCatalog(ctx, uuid.New(), f.id(), req)
	assert.Equal(t, CodeMoveDowngrade, codeOf(t, err))

	f = moveFixture(t, nil, nil)
	f.store.rows[f.id()].Source = domain.ExtensionSourceBuiltin
	_, err = f.inst.PlanMoveToCatalog(ctx, f.id(), "", "")
	assert.Equal(t, repoinstall.CodeNotRepoInstall, codeOf(t, err))

	_, err = f.inst.PlanMoveToCatalog(ctx, "nobody", "", "")
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusNotFound, ae.Code)

	_, err = f.inst.MoveToCatalog(ctx, uuid.New(), f.id(), MoveRequest{})
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusBadRequest, ae.Code)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type noMoveStore struct{ InstallStore }

type brokenMoveStore struct{ *installMem }

func (brokenMoveStore) MoveRepoToCatalog(context.Context, *domain.InstalledExtension, domain.ExtensionStateGuard, *domain.InstanceExtensionJournalEntry) error {
	return errors.New("db down")
}

// Input, wiring and store failures: each refused, nothing written.
func TestMoveToCatalog_InputAndStoreRefusals(t *testing.T) {
	ctx := context.Background()
	f := moveFixture(t, nil, nil)
	_, err := f.inst.MoveToCatalog(ctx, uuid.New(), f.id(), MoveRequest{TargetVersion: "0.1.0"})
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusBadRequest, ae.Code, "the plan's four fields are required")

	_, err = f.inst.PlanMoveToCatalog(ctx, "missing", "", "")
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusNotFound, ae.Code)
	_, err = f.inst.MoveToCatalog(ctx, uuid.New(), "missing", f.moveReq())
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusNotFound, ae.Code)

	builtin := moveFixture(t, func(e *Entry, _ *Catalog) { e.Runtime = "builtin" }, nil)
	_, err = builtin.inst.PlanMoveToCatalog(ctx, builtin.id(), "", "")
	assert.Equal(t, repoinstall.CodeBuiltinRuntime, codeOf(t, err))

	req := f.moveReq()
	f.inst.Store = noMoveStore{f.store}
	_, err = f.inst.MoveToCatalog(ctx, uuid.New(), f.id(), req)
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusNotImplemented, ae.Code, "a store without the move")

	f.inst.Store = brokenMoveStore{f.store}
	_, err = f.inst.MoveToCatalog(ctx, uuid.New(), f.id(), req)
	assert.EqualError(t, err, "db down", "a store failure is not dressed up as a conflict")
	assert.Equal(t, domain.ExtensionSourceRepo, f.store.rows[f.id()].Source)
	assert.Empty(t, f.store.moves)
	assert.Zero(t, f.changes)
}
