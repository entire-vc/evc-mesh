package postgres

import (
	"testing"

	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Compile-time interface satisfaction checks.
// These tests verify that every concrete repo type implements
// the corresponding interface from the repository package.

func TestImplementsInterfaces(t *testing.T) {
	var _ repository.WorkspaceRepository = (*WorkspaceRepo)(nil)
	var _ repository.ProjectRepository = (*ProjectRepo)(nil)
	var _ repository.TaskRepository = (*TaskRepo)(nil)
	var _ repository.TaskStatusRepository = (*TaskStatusRepo)(nil)
	var _ repository.TaskDependencyRepository = (*TaskDependencyRepo)(nil)
	var _ repository.CustomFieldDefinitionRepository = (*CustomFieldDefinitionRepo)(nil)
	var _ repository.CommentRepository = (*CommentRepo)(nil)
	var _ repository.ArtifactRepository = (*ArtifactRepo)(nil)
	var _ repository.AgentRepository = (*AgentRepo)(nil)
	var _ repository.EventBusMessageRepository = (*EventBusMessageRepo)(nil)
	var _ repository.ActivityLogRepository = (*ActivityLogRepo)(nil)
}
