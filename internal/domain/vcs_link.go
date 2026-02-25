package domain

import (
	"encoding/json"
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

// VCSLinkStatus reflects the current state of the linked object (PRs only).
type VCSLinkStatus string

const (
	VCSLinkStatusOpen   VCSLinkStatus = "open"
	VCSLinkStatusMerged VCSLinkStatus = "merged"
	VCSLinkStatusClosed VCSLinkStatus = "closed"
)

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
