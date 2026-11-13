package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// CodeMoveDowngrade: the chosen catalog release is older than the installed one.
const CodeMoveDowngrade = "extension_move_downgrade"

// OutcomeMoved and OutcomeAlreadyMoved are the answers of MoveToCatalog.
const (
	OutcomeMoved        = "moved"
	OutcomeAlreadyMoved = "already_moved"
)

// MoveStore is the write a move needs: the guarded, one-transaction switch of
// a source=repo row to a catalog release (token deleted, journal written).
type MoveStore interface {
	MoveRepoToCatalog(ctx context.Context, ext *domain.InstalledExtension, g domain.ExtensionStateGuard, journal *domain.InstanceExtensionJournalEntry) error
}

// MoveSide is one side of a move plan. It never carries a token.
type MoveSide struct {
	Source         string `json:"source"`
	Version        string `json:"version"`
	CommitSHA      string `json:"commit_sha"`
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
	TrustLevel     string `json:"trust_level"`
	RepoURL        string `json:"repo_url,omitempty"`
	RepoRef        string `json:"repo_ref,omitempty"`
	// CatalogScope is "scoped" when the target came from an exact receipt:
	// the install is then pinned to that version.
	CatalogScope string          `json:"catalog_scope,omitempty"`
	Manifest     json.RawMessage `json:"manifest"`
}

// MovePlan is what the admin sees before moving a repository install to the
// catalog, and what the apply answers.
type MovePlan struct {
	ExtensionID         string   `json:"extension_id"`
	Current             MoveSide `json:"current"`
	Target              MoveSide `json:"target"`
	AddedCapabilities   []string `json:"added_capabilities"`
	RemovedCapabilities []string `json:"removed_capabilities"`
	NeedsConsent        bool     `json:"needs_consent"`
	// OneWay is always true: catalog rollback refuses a non-catalog snapshot
	// and none is kept, so there is no way back to the repository install.
	OneWay bool `json:"one_way"`
}

// MoveRequest is the apply body: the state the plan was made against, the
// chosen catalog release, and the consent for added permissions.
type MoveRequest struct {
	ExpectedVersion   string
	ExpectedCommitSHA string
	TargetVersion     string
	TargetCommitSHA   string
	ConsentCommitSHA  string
	// TargetScope "scoped" reads the target from its exact receipt.
	TargetScope string
}

// MoveResult is the apply answer.
type MoveResult struct {
	Outcome string   `json:"outcome"`
	Plan    MovePlan `json:"plan"`
}

type movePrepared struct {
	plan MovePlan
	row  *domain.InstalledExtension
	e    *Entry // Latest is the chosen release
	m    extensions.Manifest
}

func capsOf(raw json.RawMessage) []string {
	m, _ := extensions.ParseManifest(raw)
	return repoinstall.Capabilities(m)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// prepareMove runs every check of a move and verifies the artifact of the
// chosen release exactly as a catalog install does; it writes nothing.
// targetVersion "" chooses the latest release. scope ScopeScoped reads the
// target version from its exact receipt (the id need not be in the default
// catalogue) and the moved install is pinned there.
func (in *Installer) prepareMove(ctx context.Context, id, targetVersion, scope string) (*movePrepared, error) {
	row, err := in.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, apierror.NotFound("extension")
	}
	if row.Source != domain.ExtensionSourceRepo {
		return nil, apiErr(http.StatusConflict, repoinstall.CodeNotRepoInstall, fmt.Sprintf("%s is installed from %s; only a repository install moves to the catalog", id, row.Source))
	}
	var e *Entry
	rel := (*Release)(nil)
	if scope == ScopeScoped {
		if targetVersion == "" {
			return nil, apierror.BadRequest("target_scope scoped needs target_version")
		}
		if e, err = in.exactTarget(ctx, id, targetVersion); err != nil {
			return nil, err
		}
		rel = &e.Latest
	} else {
		cat, cerr := in.Catalog.Current()
		if cerr != nil {
			return nil, apiErr(http.StatusServiceUnavailable, CodeSparkDown, "the catalog has not been verified yet")
		}
		var ok bool
		if e, ok = cat.Entry(id); !ok {
			return nil, apiErr(http.StatusNotFound, CodeNotInCatalog, "extension is not in the catalog")
		}
		rel = &e.Latest
		if targetVersion != "" {
			if rel, err = releaseByVersion(e, targetVersion); err != nil {
				return nil, err
			}
		}
		if b, hit := cat.Blocks(id, rel.Version); hit {
			return nil, apiErr(http.StatusConflict, CodeBlocked, "blocked by the catalog: "+b.Reason)
		}
	}
	if e.Runtime == "builtin" {
		return nil, apiErr(http.StatusConflict, repoinstall.CodeBuiltinRuntime, "this extension ships inside Mesh")
	}
	if rel.Yanked {
		return nil, apiErr(http.StatusConflict, CodeReleaseRevoked, "the catalog no longer offers that release")
	}
	if rel.ArtifactSHA256 == nil || *rel.ArtifactSHA256 == "" {
		return nil, apiErr(http.StatusUnprocessableEntity, CodeDigest, "the catalog gives no artifact digest for this version")
	}
	if !repoinstall.MeshAPICompatible(rel.MeshAPI, extensions.ExtensionsAPIVersion) {
		return nil, apiErr(http.StatusUnprocessableEntity, repoinstall.CodeIncompatible, fmt.Sprintf("needs Mesh API %s", rel.MeshAPI))
	}
	// The same version is a valid target (the usual case: the release the
	// repository install already runs); an older one is not.
	if semverNewer(row.Version, rel.Version) {
		return nil, apiErr(http.StatusConflict, CodeMoveDowngrade, fmt.Sprintf("the catalog release %s is older than the installed %s", rel.Version, row.Version))
	}
	chosen := *e
	chosen.Latest = *rel
	m, err := in.fetchVerified(ctx, &chosen, scope == ScopeScoped)
	if err != nil {
		return nil, err
	}
	cur := capsOf(row.Manifest)
	next := repoinstall.Capabilities(m)
	p := &movePrepared{row: row, e: &chosen, m: m}
	p.plan = MovePlan{
		ExtensionID: id,
		Current: MoveSide{Source: string(row.Source), Version: row.Version, CommitSHA: row.CommitSHA, TrustLevel: string(row.TrustLevel),
			RepoURL: row.RepoURL, RepoRef: row.RepoRef, Manifest: row.Manifest},
		Target: MoveSide{Source: string(domain.ExtensionSourceCatalog), Version: rel.Version, CommitSHA: rel.CommitSHA,
			ArtifactSHA256: *rel.ArtifactSHA256, TrustLevel: e.Trust, CatalogScope: scopeName(scope), Manifest: m.Raw},
		AddedCapabilities:   nonNil(repoinstall.NewCapabilities(cur, next)),
		RemovedCapabilities: nonNil(repoinstall.NewCapabilities(next, cur)),
		OneWay:              true,
	}
	p.plan.NeedsConsent = len(p.plan.AddedCapabilities) > 0
	return p, nil
}

// PlanMoveToCatalog shows what moving repository install id to the catalog
// would change. It writes nothing.
func (in *Installer) PlanMoveToCatalog(ctx context.Context, id, targetVersion, targetScope string) (*MovePlan, error) {
	scope, err := moveScope(targetScope)
	if err != nil {
		return nil, err
	}
	p, err := in.prepareMove(ctx, id, strings.TrimSpace(targetVersion), scope)
	if err != nil {
		return nil, err
	}
	return &p.plan, nil
}

// MoveToCatalog moves repository install id to the chosen catalog release.
//
// The request names the version and commit the plan was made against and the
// catalog version and commit chosen; anything else answers 409 and changes
// nothing. A retry after the move landed (the row already is that catalog
// release) answers already_moved without writing. Added permissions need
// consent_commit_sha equal to the target commit.
func (in *Installer) MoveToCatalog(ctx context.Context, actor uuid.UUID, id string, req MoveRequest) (*MoveResult, error) {
	if req.ExpectedVersion == "" || req.ExpectedCommitSHA == "" || req.TargetVersion == "" || req.TargetCommitSHA == "" {
		return nil, apierror.BadRequest("expected_version, expected_commit_sha, target_version and target_commit_sha (from the plan) are required")
	}
	scope, err := moveScope(req.TargetScope)
	if err != nil {
		return nil, err
	}
	ms, ok := in.Store.(MoveStore)
	if !ok {
		return nil, apiErr(http.StatusNotImplemented, CodeSparkDown, "moving to the catalog is not enabled on this instance")
	}
	row, err := in.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, apierror.NotFound("extension")
	}
	if row.Source == domain.ExtensionSourceCatalog && row.Version == req.TargetVersion && row.CommitSHA == req.TargetCommitSHA &&
		scopeName(row.CatalogScope) == scope {
		return &MoveResult{Outcome: OutcomeAlreadyMoved, Plan: MovePlan{ExtensionID: id, OneWay: true,
			Target: MoveSide{Source: string(row.Source), Version: row.Version, CommitSHA: row.CommitSHA, ArtifactSHA256: row.ArtifactSHA256,
				TrustLevel: string(row.TrustLevel), CatalogScope: scope, Manifest: row.Manifest},
			AddedCapabilities: []string{}, RemovedCapabilities: []string{}}}, nil
	}
	if row.Source == domain.ExtensionSourceRepo && (row.Version != req.ExpectedVersion || row.CommitSHA != req.ExpectedCommitSHA) {
		return nil, apiErr(http.StatusConflict, repoinstall.CodeStateChanged, fmt.Sprintf("the extension is now %s at %s; plan again", row.Version, row.CommitSHA))
	}
	p, err := in.prepareMove(ctx, id, req.TargetVersion, scope)
	if err != nil {
		return nil, err
	}
	if p.plan.Target.CommitSHA != req.TargetCommitSHA {
		return nil, apiErr(http.StatusConflict, repoinstall.CodeStateChanged, "target_commit_sha does not match the verified catalog release")
	}
	if p.plan.NeedsConsent && req.ConsentCommitSHA != p.plan.Target.CommitSHA {
		return nil, apiErr(http.StatusConflict, repoinstall.CodeConsentRequired,
			fmt.Sprintf("the catalog release adds permissions %v: send consent_commit_sha=%s", p.plan.AddedCapabilities, p.plan.Target.CommitSHA))
	}
	t := p.plan.Target
	upd := &domain.InstalledExtension{ExtensionID: id, Version: t.Version, Manifest: p.m.Raw, CommitSHA: t.CommitSHA,
		ArtifactSHA256: t.ArtifactSHA256, TrustLevel: domain.ExtensionTrustLevel(t.TrustLevel), CatalogScope: scope}
	// Both sides by version, commit and ref; the repository token is never named.
	details, _ := json.Marshal(map[string]any{
		"from_source": "repo", "from": p.row.Version, "from_commit_sha": p.row.CommitSHA, "from_ref": p.row.RepoRef, "from_repo_url": p.row.RepoURL,
		"source": "catalog", "catalog_scope": scope, "to": t.Version, "commit_sha": t.CommitSHA, "artifact_sha256": t.ArtifactSHA256, "trust": t.TrustLevel,
		"consented_capabilities": p.plan.AddedCapabilities, "removed_capabilities": p.plan.RemovedCapabilities,
	})
	journal := &domain.InstanceExtensionJournalEntry{ActorID: &actor, ExtensionID: id, Action: domain.InstanceJournalMovedToCatalog, Details: details}
	guard := domain.ExtensionStateGuard{Source: domain.ExtensionSourceRepo, Version: req.ExpectedVersion, CommitSHA: req.ExpectedCommitSHA}
	if err := ms.MoveRepoToCatalog(ctx, upd, guard, journal); err != nil {
		var ae *apierror.Error
		if errors.As(err, &ae) && ae.Code == http.StatusConflict {
			return nil, apiErr(http.StatusConflict, repoinstall.CodeStateChanged, ae.Message)
		}
		return nil, err
	}
	if in.OnChange != nil {
		in.OnChange(ctx)
	}
	return &MoveResult{Outcome: OutcomeMoved, Plan: p.plan}, nil
}

// moveScope validates a move's target_scope: "" or "default", or "scoped".
func moveScope(s string) (string, error) {
	switch strings.TrimSpace(s) {
	case "", ScopeDefault:
		return ScopeDefault, nil
	case ScopeScoped:
		return ScopeScoped, nil
	}
	return "", apierror.BadRequest(`target_scope must be "default" or "scoped"`)
}
