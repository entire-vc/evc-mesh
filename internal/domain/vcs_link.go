package domain

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// VCSProvider identifies the version control system.
type VCSProvider string

const (
	VCSProviderGitHub VCSProvider = "github"
	VCSProviderGitLab VCSProvider = "gitlab"
)

// VCSLinkType identifies what the link points to.
type VCSLinkType string

const (
	VCSLinkTypePR     VCSLinkType = "pr"
	VCSLinkTypeCommit VCSLinkType = "commit"
	VCSLinkTypeBranch VCSLinkType = "branch"
	// VCSLinkTypeReference points at someone else's PR/MR the card only reads
	// or reviews; it is never the card's own deliverable, so the done-gate
	// does not wait for it to merge.
	VCSLinkTypeReference VCSLinkType = "reference"
)

// vcsLinkTypeAliases maps accepted spellings onto the canonical link type.
// Keys are lower-cased; ParseVCSLinkType folds case before the lookup.
//
// Only the canonical value is ever stored, so every consumer (the unique
// index on (task_id, provider, link_type, external_id), reporting queries,
// downstream matchers) keeps seeing exactly one spelling per concept.
var vcsLinkTypeAliases = map[string]VCSLinkType{
	"pr":           VCSLinkTypePR,
	"pull_request": VCSLinkTypePR,
	"commit":       VCSLinkTypeCommit,
	"branch":       VCSLinkTypeBranch,
	"reference":    VCSLinkTypeReference,
}

// VCSLinkTypeNames lists the accepted link_type spellings, canonical first,
// for use in validation error messages.
var VCSLinkTypeNames = []string{"pr", "pull_request", "commit", "branch", "reference"}

// ParseVCSLinkType resolves a caller-supplied link_type to its canonical
// value. Matching ignores surrounding whitespace and case ("PR" and
// "Pull_Request" both resolve to "pr"), because link_type is a fixed
// vocabulary rather than user data — rejecting a capitalised spelling only
// ever produced a confusing 400. Returns ok=false for anything outside the
// vocabulary.
func ParseVCSLinkType(raw string) (VCSLinkType, bool) {
	lt, ok := vcsLinkTypeAliases[strings.ToLower(strings.TrimSpace(raw))]
	return lt, ok
}

// VCSLinkStatus reflects the current state of the linked object (PRs only).
type VCSLinkStatus string

const (
	VCSLinkStatusOpen   VCSLinkStatus = "open"
	VCSLinkStatusMerged VCSLinkStatus = "merged"
	VCSLinkStatusClosed VCSLinkStatus = "closed"
)

// VCSLinkStatusNames lists the accepted status values, for validation error
// messages. Callers may also send "" to mean "not specified" — the service
// layer fills that in (open, for PR links) rather than the HTTP boundary,
// since the right default depends on link_type.
var VCSLinkStatusNames = []string{"open", "merged", "closed"}

// ParseVCSLinkStatus resolves a caller-supplied status. Empty input is valid
// and means "let the service decide" — everything else must be one of the
// three known values, case- and whitespace-insensitive, so a caller who
// knows a PR is already merged (e.g. linking it after the fact, the exact
// scenario a webhook can never cover) can say so directly instead of the
// link silently sitting in an unresolvable state.
func ParseVCSLinkStatus(raw string) (VCSLinkStatus, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return "", true
	}
	switch VCSLinkStatus(trimmed) {
	case VCSLinkStatusOpen, VCSLinkStatusMerged, VCSLinkStatusClosed:
		return VCSLinkStatus(trimmed), true
	default:
		return "", false
	}
}

// VCSLink associates a task with a GitHub PR, commit, or branch.
type VCSLink struct {
	ID         uuid.UUID       `json:"id" db:"id"`
	TaskID     uuid.UUID       `json:"task_id" db:"task_id"`
	Provider   VCSProvider     `json:"provider" db:"provider"`
	LinkType   VCSLinkType     `json:"link_type" db:"link_type"`
	ExternalID string          `json:"external_id" db:"external_id"`
	URL        string          `json:"url" db:"url"`
	Title      string          `json:"title" db:"title"`
	Status     VCSLinkStatus   `json:"status" db:"status"`
	Metadata   json.RawMessage `json:"metadata" db:"metadata"`
	CreatedAt  time.Time       `json:"created_at" db:"created_at"`
	// StatusSyncedAt is when Status was last written by a status-carrying
	// source (provider webhook or an explicit-status re-link, both via
	// Upsert). Nil means the status has never been confirmed — for an open
	// PR link that is "we do not know whether it is still open".
	StatusSyncedAt *time.Time `json:"status_synced_at" db:"status_synced_at"`
	// PipelineStatus is the state of the latest GitLab pipeline of this MR
	// link (see NormalizePipelineStatus for the vocabulary); nil when no
	// pipeline has been reported, which is every GitHub link.
	PipelineStatus    *string    `json:"pipeline_status" db:"pipeline_status"`
	PipelineUpdatedAt *time.Time `json:"pipeline_updated_at" db:"pipeline_updated_at"`
	// PipelineID orders pipeline deliveries; it is storage detail, not API.
	PipelineID *int64 `json:"-" db:"pipeline_id"`
}

// pipelineStatuses is the vocabulary a GitLab pipeline webhook can report.
var pipelineStatuses = map[string]string{
	"created": "pending", "waiting_for_resource": "pending", "preparing": "pending",
	"pending": "pending", "scheduled": "pending",
	"running": "running", "success": "success", "failed": "failed",
	"canceled": "canceled", "canceling": "canceled", "skipped": "skipped", "manual": "manual",
}

// NormalizePipelineStatus folds a GitLab pipeline status into the small set
// the badge renders: pending, running, success, failed, canceled, skipped,
// manual. Returns false for a status it does not know, so a new GitLab value
// is dropped rather than stored as something the UI cannot draw.
func NormalizePipelineStatus(raw string) (string, bool) {
	s, ok := pipelineStatuses[strings.ToLower(strings.TrimSpace(raw))]
	return s, ok
}

// NormalizeVCSURL folds cosmetic differences in a VCS object's URL — scheme
// case, http vs https, and a trailing slash — into one comparable string, so
// two calls that name the SAME PR/MR/commit are recognised as the same
// object even when they spell its URL slightly differently. This is what
// lets a re-link with a corrected provider (an internal ticket: a self-hosted GitLab
// URL first mis-recorded as provider=github) find the existing row by what
// the URL actually names, instead of by (provider, external_id) — the pair
// that changes across exactly the correction being made, which is why
// matching on it could never find the row it was trying to fix.
//
// It deliberately does NOT touch path casing, query strings, or anything
// else that could be semantically meaningful: folding too aggressively is
// the inverse failure (an internal ticket defect 2) — a GitHub PR and a GitLab MR
// that happen to share a number must NOT normalize to the same string, or
// re-linking one silently erases the other.
func NormalizeVCSURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		// Not a parseable absolute URL — still fold the one difference that
		// costs nothing (a trailing slash), so a malformed value at least
		// compares consistently against itself across calls.
		return strings.TrimRight(trimmed, "/")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme == "http" {
		u.Scheme = "https"
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.Fragment = ""
	return u.String()
}

// CreateVCSLinkInput holds the data needed to create a VCS link.
type CreateVCSLinkInput struct {
	TaskID     uuid.UUID       `json:"task_id"`
	Provider   VCSProvider     `json:"provider"`
	LinkType   VCSLinkType     `json:"link_type"`
	ExternalID string          `json:"external_id"`
	URL        string          `json:"url"`
	Title      string          `json:"title"`
	Status     VCSLinkStatus   `json:"status"`
	Metadata   json.RawMessage `json:"metadata"`
}

// VCSLinkRoleReview marks a link whose task is a review of the PR/MR rather
// than the work that produced it. The review card is closed by the merge of
// its subject and is not held back by that merge not having happened yet
// (the verdict is the deliverable), so both the webhook fan-out and the
// done-evidence gate read this marker.
const VCSLinkRoleReview = "review"

// IsReviewSubject reports whether the link was recorded as the subject of a
// review card (metadata {"role":"review"}).
func (l VCSLink) IsReviewSubject() bool {
	var m struct {
		Role string `json:"role"`
	}
	if len(l.Metadata) == 0 || json.Unmarshal(l.Metadata, &m) != nil {
		return false
	}
	return m.Role == VCSLinkRoleReview
}

// VCSLinkRelationRelatesTo marks a link the webhook recorded for a task that
// a PR/MR only mentioned (its description, or a title/branch reference from a
// repository the task's project does not use). The PR/MR is not the task's
// work: its merge moves nothing, and it neither holds back nor proves the
// task's delivery.
const VCSLinkRelationRelatesTo = "relates_to"

// IsRelatesTo reports whether the link only records a mention
// (metadata {"relation":"relates_to"}).
func (l VCSLink) IsRelatesTo() bool {
	var m struct {
		Relation string `json:"relation"`
	}
	if len(l.Metadata) == 0 || json.Unmarshal(l.Metadata, &m) != nil {
		return false
	}
	return m.Relation == VCSLinkRelationRelatesTo
}

// Metadata keys the transition gate reads for the author_if_merged_shipped
// pattern. head_sha is written by Mesh when it syncs the MR; ship_sha by the
// fleet tool that signs an independent SHIP (code review / glab-merge).
const (
	VCSLinkMetaHeadSHA = "head_sha"
	VCSLinkMetaShipSHA = "ship_sha"
	// pipeline_sha is the commit the stored pipeline_status ran on, written by
	// the pipeline webhook.
	VCSLinkMetaPipelineSHA = "pipeline_sha"

	// Delivery-state keys: merge, deploy and acceptance are separate facts
	// about one SHA each, not one aggregated pipeline status.
	VCSLinkMetaImplementationSHA = "implementation_sha"
	VCSLinkMetaCIVerdict         = "ci_verdict"
	VCSLinkMetaMergedSHA         = "merged_sha"
	VCSLinkMetaDeployed          = "deployed"
	VCSLinkMetaAcceptance        = "acceptance"

	// role marks the link as the subject of a review card; the only value
	// the metadata PATCH accepts is VCSLinkRoleReview. role_set records who
	// set it there and when.
	VCSLinkMetaRole    = "role"
	VCSLinkMetaRoleSet = "role_set"
)

// VCSEnvProd is the environment whose deploy makes a link shipped.
const VCSEnvProd = "prod"

// VCSCIVerdict is the CI result of one pipeline on one commit.
type VCSCIVerdict struct {
	Status     string `json:"status"`
	PipelineID int64  `json:"pipeline_id,omitempty"`
	SHA        string `json:"sha,omitempty"`
}

// VCSDeployed records that SHA is running in Environment since At.
type VCSDeployed struct {
	SHA         string     `json:"sha"`
	Environment string     `json:"environment"`
	At          *time.Time `json:"at,omitempty"`
}

// VCSAcceptance is the live acceptance result, separate from CI and deploy.
type VCSAcceptance struct {
	Result      string `json:"result"` // passed | failed
	By          string `json:"by,omitempty"`
	EvidenceURL string `json:"evidence_url,omitempty"`
	SHA         string `json:"sha,omitempty"`
}

// VCSDelivery is the typed read model over vcs_links.metadata; absent facts
// are nil/empty, never guessed.
type VCSDelivery struct {
	ImplementationSHA string         `json:"implementation_sha,omitempty"`
	CIVerdict         *VCSCIVerdict  `json:"ci_verdict,omitempty"`
	MergedSHA         string         `json:"merged_sha,omitempty"`
	Deployed          *VCSDeployed   `json:"deployed,omitempty"`
	Acceptance        *VCSAcceptance `json:"acceptance,omitempty"`
	IsShipped         bool           `json:"is_shipped"`
}

func (l VCSLink) metaObject(key string, dst any) bool {
	var m map[string]json.RawMessage
	if len(l.Metadata) == 0 || json.Unmarshal(l.Metadata, &m) != nil {
		return false
	}
	raw, ok := m[key]
	return ok && json.Unmarshal(raw, dst) == nil
}

// Deployed is the recorded deploy, nil when none.
func (l VCSLink) Deployed() *VCSDeployed {
	var d VCSDeployed
	if !l.metaObject(VCSLinkMetaDeployed, &d) || d.SHA == "" {
		return nil
	}
	return &d
}

// Acceptance is the recorded live acceptance, nil when none.
func (l VCSLink) Acceptance() *VCSAcceptance {
	var a VCSAcceptance
	if !l.metaObject(VCSLinkMetaAcceptance, &a) || a.Result == "" {
		return nil
	}
	return &a
}

// CIVerdict is the recorded CI verdict, nil when none.
func (l VCSLink) CIVerdict() *VCSCIVerdict {
	var v VCSCIVerdict
	if !l.metaObject(VCSLinkMetaCIVerdict, &v) || v.Status == "" {
		return nil
	}
	return &v
}

// IsShipped reports whether the change is live in production. Once a link carries
// merged_sha or deployed it is judged only by them: deployed.sha equal to
// merged_sha in production. Merged without a matching production deploy is not shipped.
// A link with neither key predates these fields and keeps the old rule
// (merged + independent SHIP + green pipeline on the head).
func (l VCSLink) IsShipped() bool {
	merged, dep := l.metaString(VCSLinkMetaMergedSHA), l.Deployed()
	if merged == "" && dep == nil {
		return l.IsMergedShippedGreen()
	}
	return merged != "" && dep != nil && dep.SHA == merged && dep.Environment == VCSEnvProd
}

// Delivery assembles the typed delivery state of the link.
func (l VCSLink) Delivery() VCSDelivery {
	return VCSDelivery{
		ImplementationSHA: l.metaString(VCSLinkMetaImplementationSHA),
		CIVerdict:         l.CIVerdict(),
		MergedSHA:         l.metaString(VCSLinkMetaMergedSHA),
		Deployed:          l.Deployed(),
		Acceptance:        l.Acceptance(),
		IsShipped:         l.IsShipped(),
	}
}

// MarshalJSON extends the stored fields with the derived delivery state; the
// existing fields are unchanged. deployed_sha, deployed_env and deployed_at
// are the flat form of delivery.deployed (null until a deploy is recorded);
// they are what the dependency condition "deployed" reads.
func (l VCSLink) MarshalJSON() ([]byte, error) {
	type plain VCSLink
	out := struct {
		plain
		Delivery    VCSDelivery `json:"delivery"`
		IsShipped   bool        `json:"is_shipped"`
		DeployedSHA *string     `json:"deployed_sha"`
		DeployedEnv *string     `json:"deployed_env"`
		DeployedAt  *time.Time  `json:"deployed_at"`
	}{plain: plain(l), Delivery: l.Delivery(), IsShipped: l.IsShipped()}
	if d := l.Deployed(); d != nil {
		out.DeployedSHA, out.DeployedEnv, out.DeployedAt = &d.SHA, &d.Environment, d.At
	}
	return json.Marshal(out)
}

// IsDeployed reports whether the link records a deploy in any environment: the
// Go twin of the vcs_links EXISTS inside DependencyReleasedSQL (sha and at set).
func (l VCSLink) IsDeployed() bool {
	d := l.Deployed()
	return d != nil && d.At != nil
}

func (l VCSLink) metaString(key string) string {
	var m map[string]interface{}
	if len(l.Metadata) == 0 || json.Unmarshal(l.Metadata, &m) != nil {
		return ""
	}
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

// HeadSHA is the MR's current head commit as last synced, "" when unknown.
func (l VCSLink) HeadSHA() string { return l.metaString(VCSLinkMetaHeadSHA) }

// PipelineSHA is the commit the stored pipeline status ran on, "" when unknown.
func (l VCSLink) PipelineSHA() string { return l.metaString(VCSLinkMetaPipelineSHA) }

// ShipSHA is the head commit an independent SHIP was given for, "" when none.
func (l VCSLink) ShipSHA() string { return l.metaString(VCSLinkMetaShipSHA) }

// IsMergedShippedGreen reports whether this MR link proves delivery: merged,
// an independent SHIP bound to the current head, and a green pipeline on that head. Any
// missing fact is a "no" (fail-closed); comment text is never consulted.
func (l VCSLink) IsMergedShippedGreen() bool {
	if l.LinkType != VCSLinkTypePR || l.Status != VCSLinkStatusMerged {
		return false
	}
	head, ship := l.HeadSHA(), l.ShipSHA()
	if head == "" || ship == "" || head != ship {
		return false
	}
	// The green pipeline must have run on that same head, not on an earlier one.
	if l.PipelineSHA() != head {
		return false
	}
	return l.PipelineStatus != nil && *l.PipelineStatus == "success"
}

var changeRequestURLRe = regexp.MustCompile(`https?://[^\s)>\]"'<]+?/(?:-/merge_requests|pull)/\d+`)

// ChangeRequestRef is a PR/MR named by a URL.
type ChangeRequestRef struct {
	Provider   VCSProvider
	ExternalID string
	URL        string
}

// ExtractChangeRequestRefs returns the distinct PR/MR URLs found in text, in
// order of appearance. github.com .../pull/N is a GitHub PR; any other host
// with .../-/merge_requests/N is a GitLab MR.
func ExtractChangeRequestRefs(text string) []ChangeRequestRef {
	var refs []ChangeRequestRef
	seen := map[string]bool{}
	for _, raw := range changeRequestURLRe.FindAllString(text, -1) {
		norm := NormalizeVCSURL(raw)
		if seen[norm] {
			continue
		}
		seen[norm] = true
		idx := strings.LastIndex(norm, "/")
		ref := ChangeRequestRef{ExternalID: norm[idx+1:], URL: norm}
		if strings.Contains(norm, "/-/merge_requests/") {
			ref.Provider = VCSProviderGitLab
		} else {
			ref.Provider = VCSProviderGitHub
		}
		refs = append(refs, ref)
	}
	return refs
}

// AgentWritableVCSMetadataKeys is the whitelist of vcs_links.metadata keys
// fleet tools may write through the metadata PATCH. head_sha is deliberately
// absent: only the MR sync owns it.
var AgentWritableVCSMetadataKeys = map[string]bool{
	VCSLinkMetaShipSHA: true, VCSLinkMetaImplementationSHA: true, VCSLinkMetaMergedSHA: true,
	VCSLinkMetaCIVerdict: true, VCSLinkMetaDeployed: true, VCSLinkMetaAcceptance: true,
	VCSLinkMetaRole: true,
}

// IsFullCommitSHA reports whether s is a 40-character lowercase hex git SHA.
func IsFullCommitSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
