package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// RecallCandidatesByIDs hydrates graph candidates without counting them as reads.
// Tenant isolation belongs in the SELECT, including when an edge is malformed.
func (r *MemoryRepo) RecallCandidatesByIDs(ctx context.Context, workspaceID uuid.UUID, ids []uuid.UUID) ([]domain.Memory, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 200 {
		return nil, fmt.Errorf("recall candidate batch exceeds 200")
	}
	var rows []memoryRow
	err := r.db.SelectContext(ctx, &rows, fmt.Sprintf(`SELECT %s FROM memories
		WHERE workspace_id = $1 AND id = ANY($2) ORDER BY id`, memoryColumns), workspaceID, pq.Array(ids))
	if err != nil {
		return nil, fmt.Errorf("recall candidates: %w", err)
	}
	result := make([]domain.Memory, len(rows))
	for i, row := range rows {
		result[i] = row.toDomain()
	}
	return result, nil
}

// Zero-valued flags retain the lifecycle predicates of each legacy arm.
func recallLifecycleConditions(filter domain.MemorySearchFilter, sparse bool) []string {
	var conditions []string
	if !filter.IncludeExpired {
		conditions = append(conditions, "(expires_at IS NULL OR expires_at > NOW())")
	}
	if !filter.IncludeArchived {
		conditions = append(conditions, "archived = false")
	}
	if sparse && !filter.IncludeSuperseded {
		conditions = append(conditions, "status != 'superseded'")
	}
	return conditions
}
