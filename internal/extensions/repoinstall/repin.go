package repoinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Error codes of the re-pin and move routes.
const (
	// CodeStateChanged: the installed row is not the version and commit the
	// admin's plan was made against; nothing was written.
	CodeStateChanged = "extension_state_changed"
	// CodeNotRepoInstall: the operation needs an install from a repository.
	CodeNotRepoInstall = "extension_not_repo_install"
	// CodeTokenChoice: the repository URL changes and a token is stored; the
	// admin must say whether to send a new token or none.
	CodeTokenChoice = "extension_repo_token_choice_required"
)

var commitPinPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// IsCommitPin reports whether ref names a full commit SHA. Such a pin is
// explicit: neither the manual update nor the automatic pass ever moves it.
func IsCommitPin(ref string) bool { return commitPinPattern.MatchString(ref) }

// Pin kinds a re-pin plan reports.
const (
	PinCommit = "commit"
	PinTag    = "tag"
	PinBranch = "branch"
)

// RepinRequest is the body of the re-pin plan and apply routes. RepoURL ""
// keeps the current URL. AuthToken, when set, replaces the stored token;
// RemoveAuthToken deletes it. Neither keeps it, which is refused when the URL
// changes and a token is stored: a credential is never sent to another
// repository without the admin saying so.
type RepinRequest struct {
	RepoURL         string
	Ref             string
	AuthToken       string
	RemoveAuthToken bool
	// Apply only: the state the plan was made against, and the plan's answer.
	ExpectedVersion         string
	ExpectedCommitSHA       string
	ExpectedTargetCommitSHA string
	ConsentCommitSHA        string
}

// RepoPin is one side of a re-pin plan. It never carries a token.
type RepoPin struct {
	RepoURL   string          `json:"repo_url"`
	Ref       string          `json:"ref"`
	PinKind   string          `json:"pin_kind,omitempty"`
	CommitSHA string          `json:"commit_sha"`
	Version   string          `json:"version"`
	Manifest  json.RawMessage `json:"manifest,omitempty"`
}

// RepinPlan is what the admin sees before a re-pin, and what apply answers.
type RepinPlan struct {
	ExtensionID         string   `json:"extension_id"`
	Current             RepoPin  `json:"current"`
	Target              RepoPin  `json:"target"`
	AddedCapabilities   []string `json:"added_capabilities"`
	RemovedCapabilities []string `json:"removed_capabilities"`
	NeedsConsent        bool     `json:"needs_consent"`
	// Token is what happens to the stored repository token: keep, replace,
	// remove, or none (nothing stored and nothing given).
	Token string `json:"token"`
	// AutoUpdate says how the automatic pass treats the target pin.
	AutoUpdate string `json:"auto_update"`
	// Changed is false when the target equals the current install.
	Changed bool `json:"changed"`
}

// RepinResult is the apply answer.
type RepinResult struct {
	Outcome string    `json:"outcome"` // repinned | up_to_date
	Plan    RepinPlan `json:"plan"`
}

type repinPrepared struct {
	plan  RepinPlan
	row   *domain.InstalledExtension
	f     *fetched
	tok   domain.RepoTokenChange
	token string
}

func autoUpdateOf(kind string) string {
	switch kind {
	case PinCommit:
		return "never: a commit pin stays where it is"
	case PinTag:
		return "moves to a newer semver tag when it adds no permissions"
	default:
		return "follows the branch head when it adds no permissions"
	}
}

func (s *Service) pinKind(ctx context.Context, src Source, ref, token string) string {
	if IsCommitPin(ref) {
		return PinCommit
	}
	if tags, err := s.Client.Tags(ctx, src, token); err == nil {
		for _, t := range tags {
			if t.Name == ref {
				return PinTag
			}
		}
	}
	return PinBranch
}

// prepareRepin runs every check of a re-pin and reads the target; it writes nothing.
func (s *Service) prepareRepin(ctx context.Context, id string, req RepinRequest) (*repinPrepared, error) {
	req.RepoURL, req.Ref, req.AuthToken = strings.TrimSpace(req.RepoURL), strings.TrimSpace(req.Ref), strings.TrimSpace(req.AuthToken)
	if req.Ref == "" {
		return nil, apierror.BadRequest("ref (a tag, a branch or a full commit SHA) is required")
	}
	if req.AuthToken != "" && req.RemoveAuthToken {
		return nil, apierror.BadRequest("auth_token and remove_auth_token exclude each other")
	}
	row, err := s.Installed.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, apierror.NotFound("extension")
	}
	if row.Source != domain.ExtensionSourceRepo {
		return nil, apiErr(http.StatusConflict, CodeNotRepoInstall, fmt.Sprintf("%s is installed from %s, not from a repository", id, row.Source))
	}
	stored, err := s.Repo.GetToken(ctx, id)
	if err != nil {
		return nil, errors.New("the stored repository token is unreadable")
	}
	url := row.RepoURL
	if req.RepoURL != "" {
		url = req.RepoURL
	}
	p := &repinPrepared{row: row}
	switch {
	case req.AuthToken != "":
		p.tok, p.token, p.plan.Token = domain.RepoTokenReplace, req.AuthToken, string(domain.RepoTokenReplace)
	case req.RemoveAuthToken:
		p.tok, p.plan.Token = domain.RepoTokenRemove, string(domain.RepoTokenRemove)
		if stored == "" {
			p.plan.Token = "none"
		}
	case stored != "" && url != row.RepoURL:
		return nil, apiErr(http.StatusBadRequest, CodeTokenChoice,
			"the repository URL changes and a token is stored: give auth_token for the new repository or remove_auth_token; the stored one is not sent elsewhere")
	default:
		p.tok, p.token, p.plan.Token = domain.RepoTokenKeep, stored, string(domain.RepoTokenKeep)
		if stored == "" {
			p.plan.Token = "none"
		}
	}

	src, err := s.Client.ParseURL(url)
	if err != nil {
		return nil, wrapHost(err)
	}
	f, err := s.fetchSrc(ctx, src, req.Ref, p.token)
	if err != nil {
		return nil, err
	}
	if IsCommitPin(req.Ref) && f.sha != req.Ref {
		return nil, apiErr(http.StatusUnprocessableEntity, CodeManifestInvalid, "the repository resolves that commit to another one")
	}
	if f.manifest.ID != id {
		return nil, apiErr(http.StatusConflict, CodeIDConflict, fmt.Sprintf("that ref declares id %q, not %q", f.manifest.ID, id))
	}
	p.f = f
	kind := s.pinKind(ctx, src, req.Ref, p.token)
	cur, _ := extensions.ParseManifest(row.Manifest)
	p.plan.ExtensionID = id
	p.plan.Current = RepoPin{RepoURL: row.RepoURL, Ref: row.RepoRef, CommitSHA: row.CommitSHA, Version: row.Version}
	if IsCommitPin(row.RepoRef) {
		p.plan.Current.PinKind = PinCommit
	}
	p.plan.Target = RepoPin{RepoURL: url, Ref: req.Ref, PinKind: kind, CommitSHA: f.sha, Version: f.manifest.Version, Manifest: f.manifest.Raw}
	p.plan.AddedCapabilities = nonNil(NewCapabilities(Capabilities(cur), Capabilities(f.manifest)))
	p.plan.RemovedCapabilities = nonNil(NewCapabilities(Capabilities(f.manifest), Capabilities(cur)))
	p.plan.NeedsConsent = len(p.plan.AddedCapabilities) > 0
	p.plan.AutoUpdate = autoUpdateOf(kind)
	p.plan.Changed = url != row.RepoURL || req.Ref != row.RepoRef || f.sha != row.CommitSHA ||
		p.tok == domain.RepoTokenReplace || (p.tok == domain.RepoTokenRemove && stored != "")
	return p, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// PlanRepin shows what a re-pin of a repository install would change. It
// writes nothing.
func (s *Service) PlanRepin(ctx context.Context, id string, req RepinRequest) (*RepinPlan, error) {
	p, err := s.prepareRepin(ctx, id, req)
	if err != nil {
		return nil, err
	}
	return &p.plan, nil
}

// Repin applies a re-pin. The request carries the version and commit the plan
// was made against and the target commit the plan showed; all three are
// required. When the row is no longer that, or the ref now resolves to another
// commit than expected_target_commit_sha, it answers 409 and writes nothing.
// Added permissions need consent_commit_sha equal to the target commit; there
// is no pending state for a re-pin, it is refused instead.
func (s *Service) Repin(ctx context.Context, actor uuid.UUID, id string, req RepinRequest) (*RepinResult, error) {
	if strings.TrimSpace(req.ExpectedVersion) == "" || strings.TrimSpace(req.ExpectedCommitSHA) == "" ||
		strings.TrimSpace(req.ExpectedTargetCommitSHA) == "" {
		return nil, apierror.BadRequest("expected_version, expected_commit_sha and expected_target_commit_sha (from the plan) are required")
	}
	if row, err := s.Installed.Get(ctx, id); err != nil {
		return nil, err
	} else if row != nil && row.Source == domain.ExtensionSourceRepo && (row.Version != req.ExpectedVersion || row.CommitSHA != req.ExpectedCommitSHA) {
		return nil, apiErr(http.StatusConflict, CodeStateChanged,
			fmt.Sprintf("the extension is now %s at %s; plan again", row.Version, row.CommitSHA))
	}
	p, err := s.prepareRepin(ctx, id, req)
	if err != nil {
		return nil, err
	}
	if req.ExpectedTargetCommitSHA != p.f.sha {
		return nil, apiErr(http.StatusConflict, CodeStateChanged, fmt.Sprintf("the ref now resolves to %s, not the planned %s; plan again", p.f.sha, req.ExpectedTargetCommitSHA))
	}
	if p.plan.NeedsConsent && req.ConsentCommitSHA != p.f.sha {
		return nil, apiErr(http.StatusConflict, CodeConsentRequired,
			fmt.Sprintf("the target adds permissions %v: send consent_commit_sha=%s", p.plan.AddedCapabilities, p.f.sha))
	}
	if !p.plan.Changed {
		return &RepinResult{Outcome: string(OutcomeUpToDate), Plan: p.plan}, nil
	}
	if err := s.prepareTheme(ctx, p.f, p.token); err != nil {
		return nil, err
	}
	next := &domain.InstalledExtension{
		ExtensionID: id, RepoURL: p.plan.Target.RepoURL, RepoRef: p.plan.Target.Ref, CommitSHA: p.f.sha,
		Version: p.f.manifest.Version, Manifest: p.f.manifest.Raw, InstalledBy: &actor,
	}
	// The journal names both pins and what happened to the token, never the token.
	details, _ := json.Marshal(map[string]any{
		"from_repo_url": p.row.RepoURL, "from_ref": p.row.RepoRef, "from_commit_sha": p.row.CommitSHA, "from": p.row.Version,
		"repo_url": next.RepoURL, "ref": next.RepoRef, "pin_kind": p.plan.Target.PinKind, "commit_sha": next.CommitSHA, "to": next.Version,
		"token": p.plan.Token, "consented_capabilities": p.plan.AddedCapabilities, "removed_capabilities": p.plan.RemovedCapabilities,
	})
	entry := &domain.InstanceExtensionJournalEntry{ActorID: &actor, ExtensionID: id, Action: domain.InstanceJournalRepinned, Details: details}
	guard := domain.ExtensionStateGuard{Source: domain.ExtensionSourceRepo, Version: req.ExpectedVersion, CommitSHA: req.ExpectedCommitSHA}
	if err := s.Repo.Repin(ctx, next, guard, p.tok, p.token, entry); err != nil {
		var ae *apierror.Error
		if errors.As(err, &ae) && ae.Code == http.StatusConflict {
			return nil, apiErr(http.StatusConflict, CodeStateChanged, ae.Message)
		}
		return nil, err
	}
	s.changed(ctx)
	return &RepinResult{Outcome: "repinned", Plan: p.plan}, nil
}
