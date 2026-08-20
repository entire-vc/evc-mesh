package domain

import (
	"time"

	"github.com/google/uuid"
)

// Watch subscription sources. How a subscription came about, kept because it is
// the only way to later explain a subscription the watcher does not remember
// making.
const (
	// WatchSourceExplicit is the Watch button.
	WatchSourceExplicit = "explicit"
	// WatchSourceAuthor is the document's creator, subscribed on create.
	WatchSourceAuthor = "author"
	// WatchSourceCommenter is anyone who commented on the document.
	WatchSourceCommenter = "commenter"
)

// DocumentWatcher is one principal's subscription to one document.
//
// WatcherID is a users.id or an agents.id depending on WatcherKind; the pair is
// the identity, never the id alone. Users and agents are separate id spaces, and
// comparing only ids is how a person ends up receiving the notification for an
// agent's edit — or, worse, not receiving their own exclusion.
type DocumentWatcher struct {
	DocumentID  uuid.UUID `json:"document_id"  db:"document_id"`
	WatcherID   uuid.UUID `json:"watcher_id"   db:"watcher_id"`
	WatcherKind string    `json:"watcher_kind" db:"watcher_kind"` // "agent" | "user"
	Source      string    `json:"source"       db:"source"`

	// Muted is an unsubscribe that survives the next automatic subscribe.
	//
	// Deleting the row instead would make unwatching a document you comment on
	// impossible: the following comment would re-create what the button just
	// removed. See migrations/20260820105_create_document_watchers.sql.
	Muted bool `json:"muted" db:"muted"`

	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// DocumentWatchState is what GET /documents/:doc_id/watch answers: whether the
// caller is subscribed, and how many principals are.
type DocumentWatchState struct {
	Watching bool `json:"watching"`
	// Source is empty when Watching is false.
	Source string `json:"source,omitempty"`
	// Muted reports an explicit unsubscribe, which is not the same as never
	// having subscribed: the UI can then say "you will not be told about this"
	// rather than offering a subscription that already exists in negative form.
	Muted bool `json:"muted"`
	// WatcherCount counts live (non-muted) watchers, the caller included.
	WatcherCount int `json:"watcher_count"`
}

// DocumentChangeNotice is a change that has happened and has not been announced
// yet — the unit of coalescing.
//
// One open notice per (document, actor): every autosave folds into the same row
// instead of producing its own notification. See the table comment in
// migrations/20260820105_create_document_watchers.sql for why this is a table
// and not a timer.
type DocumentChangeNotice struct {
	ID          uuid.UUID `json:"id"           db:"id"`
	DocumentID  uuid.UUID `json:"document_id"  db:"document_id"`
	WorkspaceID uuid.UUID `json:"workspace_id" db:"workspace_id"`

	ActorID   uuid.UUID `json:"actor_id"   db:"actor_id"`
	ActorKind string    `json:"actor_kind" db:"actor_kind"`
	ActorName string    `json:"actor_name" db:"actor_name"`

	EditCount    int  `json:"edit_count"    db:"edit_count"`
	TitleChanged bool `json:"title_changed" db:"title_changed"`
	BodyChanged  bool `json:"body_changed"  db:"body_changed"`

	FromVersion int `json:"from_version" db:"from_version"`
	ToVersion   int `json:"to_version"   db:"to_version"`

	FirstEditAt time.Time `json:"first_edit_at" db:"first_edit_at"`
	LastEditAt  time.Time `json:"last_edit_at"  db:"last_edit_at"`

	DispatchedAt  *time.Time `json:"dispatched_at,omitempty"  db:"dispatched_at"`
	DispatchError *string    `json:"dispatch_error,omitempty" db:"dispatch_error"`
	Recipients    int        `json:"recipients"               db:"recipients"`
}
