package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// WorkspaceMembershipReader answers one question: may this human act inside this
// workspace?
//
// It implements service.WorkspaceMembershipReader and runs the same rule as
// middleware.UserIsWorkspaceMember — a workspace_members row, OR ownership of the
// workspace, because some legacy workspaces were created without an explicit
// owner membership row and dropping the fallback would lock those owners out of
// their own tenant.
//
// It is a separate one-method type rather than a method on WorkspaceMemberRepo
// because the assignee tenancy guard is the only caller and a narrow port keeps
// its test double to a single function.
type WorkspaceMembershipReader struct {
	db *sqlx.DB
}

// NewWorkspaceMembershipReader builds the reader over an existing pool.
func NewWorkspaceMembershipReader(db *sqlx.DB) *WorkspaceMembershipReader {
	return &WorkspaceMembershipReader{db: db}
}

// IsWorkspaceMember reports membership, distinguishing "no" from "could not
// tell": the boolean is only meaningful when the error is nil. The caller
// (service.assertAssigneeInProjectWorkspace) refuses on either, but conflating
// the two here would deny it the ability to log which one happened.
func (r *WorkspaceMembershipReader) IsWorkspaceMember(ctx context.Context, workspaceID, userID uuid.UUID) (bool, error) {
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM workspace_members
			WHERE workspace_id = $1 AND user_id = $2
		) OR EXISTS (
			SELECT 1 FROM workspaces
			WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL
		)`
	var ok bool
	if err := r.db.QueryRowContext(ctx, q, workspaceID, userID).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}
