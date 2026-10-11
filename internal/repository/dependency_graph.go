package repository

import (
	"context"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// DependencyGraphReader provides a consistent bulk snapshot without task-by-task reads.
// Focus may be an accessible task in another project of the same workspace.
type DependencyGraphReader interface {
	ReadGraph(context.Context, uuid.UUID, uuid.UUID, domain.GraphScope) (*domain.GraphSnapshot, error)
}
