package domain

import (
	"encoding/json"
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
}

// VCSLinkTypeNames lists the accepted link_type spellings, canonical first,
// for use in validation error messages.
var VCSLinkTypeNames = []string{"pr", "pull_request", "commit", "branch"}

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
