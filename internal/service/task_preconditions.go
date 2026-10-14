package service

import (
	"time"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// CheckTaskPreconditions compares a caller's optional expected_updated_at /
// expected_version against the task snapshot it is about to write. Both given
// means both must match; neither given is a no-op (the historical behaviour).
//
// The snapshot must be the same row the write is conditioned on: the repository
// writes with WHERE version = snapshot.Version, so a change that lands after the
// snapshot is read makes that write fail with the same CASConflictError, and
// there is no read-compare-write window left on the server.
func CheckTaskPreconditions(task *domain.Task, expectedUpdatedAt *time.Time, expectedVersion *int64) error {
	if expectedUpdatedAt != nil && !expectedUpdatedAt.Equal(task.UpdatedAt) {
		return taskPreconditionConflict(task)
	}
	if expectedVersion != nil && *expectedVersion != task.Version {
		return taskPreconditionConflict(task)
	}
	return nil
}

func taskPreconditionConflict(task *domain.Task) error {
	return &CASConflictError{CurrentVersion: task.Version, CurrentStatusID: task.StatusID, CurrentUpdatedAt: task.UpdatedAt}
}
