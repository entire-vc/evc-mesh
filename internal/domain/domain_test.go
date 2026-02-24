package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Enum string value tests
// ---------------------------------------------------------------------------

func TestAssigneeType_StringValues(t *testing.T) {
	tests := []struct {
		name     string
		value    AssigneeType
		expected string
	}{
		{"user", AssigneeTypeUser, "user"},
		{"agent", AssigneeTypeAgent, "agent"},
		{"unassigned", AssigneeTypeUnassigned, "unassigned"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestPriority_StringValues(t *testing.T) {
	tests := []struct {
		name     string
		value    Priority
		expected string
	}{
		{"urgent", PriorityUrgent, "urgent"},
		{"high", PriorityHigh, "high"},
		{"medium", PriorityMedium, "medium"},
		{"low", PriorityLow, "low"},
		{"none", PriorityNone, "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestActorType_StringValues(t *testing.T) {
	tests := []struct {
		name     string
		value    ActorType
		expected string
	}{
		{"user", ActorTypeUser, "user"},
		{"agent", ActorTypeAgent, "agent"},
		{"system", ActorTypeSystem, "system"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestStatusCategory_StringValues(t *testing.T) {
	tests := []struct {
		name     string
		value    StatusCategory
		expected string
	}{
		{"backlog", StatusCategoryBacklog, "backlog"},
		{"todo", StatusCategoryTodo, "todo"},
		{"in_progress", StatusCategoryInProgress, "in_progress"},
		{"review", StatusCategoryReview, "review"},
		{"done", StatusCategoryDone, "done"},
		{"cancelled", StatusCategoryCancelled, "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestDependencyType_StringValues(t *testing.T) {
	tests := []struct {
		name     string
		value    DependencyType
		expected string
	}{
		{"blocks", DependencyTypeBlocks, "blocks"},
		{"relates_to", DependencyTypeRelatesTo, "relates_to"},
		{"is_child_of", DependencyTypeIsChildOf, "is_child_of"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestAgentType_StringValues(t *testing.T) {
	tests := []struct {
		name     string
		value    AgentType
		expected string
	}{
		{"claude_code", AgentTypeClaudeCode, "claude_code"},
		{"openclaw", AgentTypeOpenClaw, "openclaw"},
		{"cline", AgentTypeCline, "cline"},
		{"aider", AgentTypeAider, "aider"},
		{"custom", AgentTypeCustom, "custom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestAgentStatus_StringValues(t *testing.T) {
	tests := []struct {
		name     string
		value    AgentStatus
		expected string
	}{
		{"online", AgentStatusOnline, "online"},
		{"offline", AgentStatusOffline, "offline"},
		{"busy", AgentStatusBusy, "busy"},
		{"error", AgentStatusError, "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestEventType_StringValues(t *testing.T) {
	tests := []struct {
		name     string
		value    EventType
		expected string
	}{
		{"summary", EventTypeSummary, "summary"},
		{"status_change", EventTypeStatusChange, "status_change"},
		{"context_update", EventTypeContextUpdate, "context_update"},
		{"error", EventTypeError, "error"},
		{"dependency_resolved", EventTypeDependencyResolved, "dependency_resolved"},
		{"custom", EventTypeCustom, "custom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestFieldType_StringValues(t *testing.T) {
	tests := []struct {
		name     string
		value    FieldType
		expected string
	}{
		{"text", FieldTypeText, "text"},
		{"number", FieldTypeNumber, "number"},
		{"date", FieldTypeDate, "date"},
		{"datetime", FieldTypeDatetime, "datetime"},
		{"select", FieldTypeSelect, "select"},
		{"multiselect", FieldTypeMultiselect, "multiselect"},
		{"url", FieldTypeURL, "url"},
		{"email", FieldTypeEmail, "email"},
		{"checkbox", FieldTypeCheckbox, "checkbox"},
		{"user_ref", FieldTypeUserRef, "user_ref"},
		{"agent_ref", FieldTypeAgentRef, "agent_ref"},
		{"json", FieldTypeJSON, "json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestArtifactType_StringValues(t *testing.T) {
	tests := []struct {
		name     string
		value    ArtifactType
		expected string
	}{
		{"file", ArtifactTypeFile, "file"},
		{"code", ArtifactTypeCode, "code"},
		{"log", ArtifactTypeLog, "log"},
		{"report", ArtifactTypeReport, "report"},
		{"link", ArtifactTypeLink, "link"},
		{"image", ArtifactTypeImage, "image"},
		{"data", ArtifactTypeData, "data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestUploaderType_StringValues(t *testing.T) {
	tests := []struct {
		name     string
		value    UploaderType
		expected string
	}{
		{"user", UploaderTypeUser, "user"},
		{"agent", UploaderTypeAgent, "agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestDefaultAssigneeType_StringValues(t *testing.T) {
	tests := []struct {
		name     string
		value    DefaultAssigneeType
		expected string
	}{
		{"user", DefaultAssigneeUser, "user"},
		{"agent", DefaultAssigneeAgent, "agent"},
		{"none", DefaultAssigneeNone, "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

// ---------------------------------------------------------------------------
// JSON serialization/deserialization tests for structs
// ---------------------------------------------------------------------------

func TestTask_JSONSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	assigneeID := uuid.New()
	parentID := uuid.New()
	estimatedHours := 8.5
	completedAt := now.Add(24 * time.Hour)

	task := Task{
		ID:             uuid.New(),
		ProjectID:      uuid.New(),
		StatusID:       uuid.New(),
		Title:          "Implement feature X",
		Description:    "Full description here",
		AssigneeID:     &assigneeID,
		AssigneeType:   AssigneeTypeUser,
		Priority:       PriorityHigh,
		ParentTaskID:   &parentID,
		Position:       1.5,
		DueDate:        &now,
		EstimatedHours: &estimatedHours,
		CustomFields:   json.RawMessage(`{"complexity":"high"}`),
		Labels:         pq.StringArray{"bug", "frontend"},
		CreatedBy:      uuid.New(),
		CreatedByType:  ActorTypeUser,
		CreatedAt:      now,
		UpdatedAt:      now,
		CompletedAt:    &completedAt,
	}

	data, err := json.Marshal(task)
	require.NoError(t, err)

	var decoded Task
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, task.ID, decoded.ID)
	assert.Equal(t, task.ProjectID, decoded.ProjectID)
	assert.Equal(t, task.StatusID, decoded.StatusID)
	assert.Equal(t, task.Title, decoded.Title)
	assert.Equal(t, task.Description, decoded.Description)
	assert.Equal(t, task.AssigneeID, decoded.AssigneeID)
	assert.Equal(t, task.AssigneeType, decoded.AssigneeType)
	assert.Equal(t, task.Priority, decoded.Priority)
	assert.Equal(t, task.ParentTaskID, decoded.ParentTaskID)
	assert.Equal(t, task.Position, decoded.Position)
	assert.Equal(t, task.CreatedBy, decoded.CreatedBy)
	assert.Equal(t, task.CreatedByType, decoded.CreatedByType)
	assert.JSONEq(t, `{"complexity":"high"}`, string(decoded.CustomFields))

	// Verify JSON keys exist
	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)
	assert.Contains(t, raw, "id")
	assert.Contains(t, raw, "project_id")
	assert.Contains(t, raw, "status_id")
	assert.Contains(t, raw, "title")
	assert.Contains(t, raw, "assignee_id")
	assert.Contains(t, raw, "assignee_type")
	assert.Contains(t, raw, "priority")
	assert.Contains(t, raw, "parent_task_id")
	assert.Contains(t, raw, "due_date")
	assert.Contains(t, raw, "estimated_hours")
	assert.Contains(t, raw, "custom_fields")
	assert.Contains(t, raw, "labels")
	assert.Contains(t, raw, "created_by")
	assert.Contains(t, raw, "created_by_type")
	assert.Contains(t, raw, "created_at")
	assert.Contains(t, raw, "updated_at")
	assert.Contains(t, raw, "completed_at")
}

func TestTask_NullableFields_SerializeAsNull(t *testing.T) {
	task := Task{
		ID:           uuid.New(),
		ProjectID:    uuid.New(),
		StatusID:     uuid.New(),
		Title:        "Task without optionals",
		AssigneeType: AssigneeTypeUnassigned,
		Priority:     PriorityNone,
		CreatedBy:    uuid.New(),
	}

	data, err := json.Marshal(task)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)

	// Pointer fields should serialize as null when nil
	assert.Equal(t, "null", string(raw["assignee_id"]))
	assert.Equal(t, "null", string(raw["parent_task_id"]))
	assert.Equal(t, "null", string(raw["due_date"]))
	assert.Equal(t, "null", string(raw["estimated_hours"]))
	assert.Equal(t, "null", string(raw["completed_at"]))
}

func TestTask_NullableFields_SerializeWithValue(t *testing.T) {
	assigneeID := uuid.New()
	dueDate := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
	hours := 4.0
	completedAt := time.Date(2025, 6, 16, 12, 0, 0, 0, time.UTC)

	task := Task{
		ID:             uuid.New(),
		ProjectID:      uuid.New(),
		StatusID:       uuid.New(),
		Title:          "Task with optionals",
		AssigneeID:     &assigneeID,
		AssigneeType:   AssigneeTypeUser,
		Priority:       PriorityMedium,
		DueDate:        &dueDate,
		EstimatedHours: &hours,
		CreatedBy:      uuid.New(),
		CompletedAt:    &completedAt,
	}

	data, err := json.Marshal(task)
	require.NoError(t, err)

	var decoded Task
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	require.NotNil(t, decoded.AssigneeID)
	assert.Equal(t, assigneeID, *decoded.AssigneeID)
	require.NotNil(t, decoded.DueDate)
	assert.True(t, dueDate.Equal(*decoded.DueDate))
	require.NotNil(t, decoded.EstimatedHours)
	assert.Equal(t, 4.0, *decoded.EstimatedHours)
	require.NotNil(t, decoded.CompletedAt)
	assert.True(t, completedAt.Equal(*decoded.CompletedAt))
}

func TestTask_CustomFields_JSONB(t *testing.T) {
	tests := []struct {
		name  string
		input json.RawMessage
	}{
		{"object", json.RawMessage(`{"key":"value","nested":{"a":1}}`)},
		{"array", json.RawMessage(`[1,2,3]`)},
		{"null", json.RawMessage(`null`)},
		{"empty_object", json.RawMessage(`{}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := Task{
				ID:           uuid.New(),
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Custom fields test",
				CustomFields: tt.input,
				CreatedBy:    uuid.New(),
			}
			data, err := json.Marshal(task)
			require.NoError(t, err)

			var decoded Task
			err = json.Unmarshal(data, &decoded)
			require.NoError(t, err)

			assert.JSONEq(t, string(tt.input), string(decoded.CustomFields))
		})
	}
}

func TestTask_Labels_PqStringArray(t *testing.T) {
	tests := []struct {
		name   string
		labels pq.StringArray
	}{
		{"multiple_labels", pq.StringArray{"bug", "frontend", "urgent"}},
		{"single_label", pq.StringArray{"solo"}},
		{"empty_labels", pq.StringArray{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := Task{
				ID:        uuid.New(),
				ProjectID: uuid.New(),
				StatusID:  uuid.New(),
				Title:     "Labels test",
				Labels:    tt.labels,
				CreatedBy: uuid.New(),
			}
			data, err := json.Marshal(task)
			require.NoError(t, err)

			var decoded Task
			err = json.Unmarshal(data, &decoded)
			require.NoError(t, err)

			assert.Equal(t, tt.labels, decoded.Labels)
		})
	}
}

func TestTask_ZeroValue(t *testing.T) {
	var task Task
	data, err := json.Marshal(task)
	require.NoError(t, err)

	var decoded Task
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, uuid.Nil, decoded.ID)
	assert.Equal(t, "", decoded.Title)
	assert.Equal(t, AssigneeType(""), decoded.AssigneeType)
	assert.Equal(t, Priority(""), decoded.Priority)
	assert.Nil(t, decoded.AssigneeID)
	assert.Nil(t, decoded.ParentTaskID)
	assert.Nil(t, decoded.DueDate)
	assert.Nil(t, decoded.EstimatedHours)
	assert.Nil(t, decoded.CompletedAt)
	assert.Equal(t, float64(0), decoded.Position)
}

func TestWorkspace_JSONSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	ws := Workspace{
		ID:                uuid.New(),
		Name:              "Acme Corp",
		Slug:              "acme-corp",
		OwnerID:           uuid.New(),
		Settings:          json.RawMessage(`{"theme":"dark","lang":"en"}`),
		BillingPlanID:     "plan_pro",
		BillingCustomerID: "cust_123",
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	data, err := json.Marshal(ws)
	require.NoError(t, err)

	var decoded Workspace
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, ws.ID, decoded.ID)
	assert.Equal(t, ws.Name, decoded.Name)
	assert.Equal(t, ws.Slug, decoded.Slug)
	assert.Equal(t, ws.OwnerID, decoded.OwnerID)
	assert.Equal(t, ws.BillingPlanID, decoded.BillingPlanID)
	assert.Equal(t, ws.BillingCustomerID, decoded.BillingCustomerID)
	assert.JSONEq(t, `{"theme":"dark","lang":"en"}`, string(decoded.Settings))

	// Verify JSON keys
	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)
	assert.Contains(t, raw, "id")
	assert.Contains(t, raw, "name")
	assert.Contains(t, raw, "slug")
	assert.Contains(t, raw, "owner_id")
	assert.Contains(t, raw, "settings")
	assert.Contains(t, raw, "billing_plan_id")
	assert.Contains(t, raw, "billing_customer_id")
	assert.Contains(t, raw, "created_at")
	assert.Contains(t, raw, "updated_at")
}

func TestWorkspace_ZeroValue(t *testing.T) {
	var ws Workspace
	data, err := json.Marshal(ws)
	require.NoError(t, err)

	var decoded Workspace
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, uuid.Nil, decoded.ID)
	assert.Equal(t, "", decoded.Name)
	assert.Equal(t, "", decoded.Slug)
}

func TestProject_JSONSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	proj := Project{
		ID:                  uuid.New(),
		WorkspaceID:         uuid.New(),
		Name:                "Backend API",
		Description:         "Core backend services",
		Slug:                "backend-api",
		Icon:                "rocket",
		Settings:            json.RawMessage(`{"visibility":"private"}`),
		DefaultAssigneeType: DefaultAssigneeAgent,
		IsArchived:          false,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	data, err := json.Marshal(proj)
	require.NoError(t, err)

	var decoded Project
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, proj.ID, decoded.ID)
	assert.Equal(t, proj.WorkspaceID, decoded.WorkspaceID)
	assert.Equal(t, proj.Name, decoded.Name)
	assert.Equal(t, proj.Description, decoded.Description)
	assert.Equal(t, proj.Slug, decoded.Slug)
	assert.Equal(t, proj.Icon, decoded.Icon)
	assert.Equal(t, proj.DefaultAssigneeType, decoded.DefaultAssigneeType)
	assert.Equal(t, proj.IsArchived, decoded.IsArchived)
	assert.JSONEq(t, `{"visibility":"private"}`, string(decoded.Settings))

	// Verify JSON keys
	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)
	assert.Contains(t, raw, "default_assignee_type")
	assert.Contains(t, raw, "is_archived")
}

func TestProject_ZeroValue(t *testing.T) {
	var proj Project
	data, err := json.Marshal(proj)
	require.NoError(t, err)

	var decoded Project
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, uuid.Nil, decoded.ID)
	assert.Equal(t, "", decoded.Name)
	assert.Equal(t, DefaultAssigneeType(""), decoded.DefaultAssigneeType)
	assert.False(t, decoded.IsArchived)
}

func TestTaskStatus_JSONSerialization(t *testing.T) {
	ts := TaskStatus{
		ID:             uuid.New(),
		ProjectID:      uuid.New(),
		Name:           "In Review",
		Slug:           "in-review",
		Color:          "#FFA500",
		Position:       3,
		Category:       StatusCategoryReview,
		IsDefault:      false,
		AutoTransition: json.RawMessage(`{"on_approve":"done"}`),
	}

	data, err := json.Marshal(ts)
	require.NoError(t, err)

	var decoded TaskStatus
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, ts.ID, decoded.ID)
	assert.Equal(t, ts.ProjectID, decoded.ProjectID)
	assert.Equal(t, ts.Name, decoded.Name)
	assert.Equal(t, ts.Slug, decoded.Slug)
	assert.Equal(t, ts.Color, decoded.Color)
	assert.Equal(t, ts.Position, decoded.Position)
	assert.Equal(t, ts.Category, decoded.Category)
	assert.Equal(t, ts.IsDefault, decoded.IsDefault)
	assert.JSONEq(t, `{"on_approve":"done"}`, string(decoded.AutoTransition))
}

func TestTaskStatus_ZeroValue(t *testing.T) {
	var ts TaskStatus
	data, err := json.Marshal(ts)
	require.NoError(t, err)

	var decoded TaskStatus
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, uuid.Nil, decoded.ID)
	assert.Equal(t, 0, decoded.Position)
	assert.False(t, decoded.IsDefault)
	assert.Equal(t, StatusCategory(""), decoded.Category)
}

func TestComment_JSONSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	parentID := uuid.New()
	c := Comment{
		ID:              uuid.New(),
		TaskID:          uuid.New(),
		ParentCommentID: &parentID,
		AuthorID:        uuid.New(),
		AuthorType:      ActorTypeAgent,
		Body:            "This looks correct, merging.",
		Metadata:        json.RawMessage(`{"model":"claude-3"}`),
		IsInternal:      true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	data, err := json.Marshal(c)
	require.NoError(t, err)

	var decoded Comment
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, c.ID, decoded.ID)
	assert.Equal(t, c.TaskID, decoded.TaskID)
	require.NotNil(t, decoded.ParentCommentID)
	assert.Equal(t, parentID, *decoded.ParentCommentID)
	assert.Equal(t, c.AuthorID, decoded.AuthorID)
	assert.Equal(t, c.AuthorType, decoded.AuthorType)
	assert.Equal(t, c.Body, decoded.Body)
	assert.Equal(t, c.IsInternal, decoded.IsInternal)
	assert.JSONEq(t, `{"model":"claude-3"}`, string(decoded.Metadata))
}

func TestComment_NullParent(t *testing.T) {
	c := Comment{
		ID:         uuid.New(),
		TaskID:     uuid.New(),
		AuthorID:   uuid.New(),
		AuthorType: ActorTypeUser,
		Body:       "Top-level comment",
	}

	data, err := json.Marshal(c)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)
	assert.Equal(t, "null", string(raw["parent_comment_id"]))
}

func TestComment_ZeroValue(t *testing.T) {
	var c Comment
	data, err := json.Marshal(c)
	require.NoError(t, err)

	var decoded Comment
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, uuid.Nil, decoded.ID)
	assert.Nil(t, decoded.ParentCommentID)
	assert.False(t, decoded.IsInternal)
}

func TestArtifact_JSONSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	a := Artifact{
		ID:             uuid.New(),
		TaskID:         uuid.New(),
		Name:           "screenshot.png",
		ArtifactType:   ArtifactTypeImage,
		MimeType:       "image/png",
		StorageKey:     "artifacts/2025/06/abc123.png",
		StorageURL:     "https://cdn.example.com/artifacts/2025/06/abc123.png",
		SizeBytes:      1048576,
		ChecksumSHA256: "e3b0c44298fc1c149afbf4c8996fb924",
		Metadata:       json.RawMessage(`{"width":1920,"height":1080}`),
		UploadedBy:     uuid.New(),
		UploadedByType: UploaderTypeUser,
		CreatedAt:      now,
	}

	data, err := json.Marshal(a)
	require.NoError(t, err)

	var decoded Artifact
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, a.ID, decoded.ID)
	assert.Equal(t, a.TaskID, decoded.TaskID)
	assert.Equal(t, a.Name, decoded.Name)
	assert.Equal(t, a.ArtifactType, decoded.ArtifactType)
	assert.Equal(t, a.MimeType, decoded.MimeType)
	assert.Equal(t, a.StorageKey, decoded.StorageKey)
	assert.Equal(t, a.StorageURL, decoded.StorageURL)
	assert.Equal(t, a.SizeBytes, decoded.SizeBytes)
	assert.Equal(t, a.ChecksumSHA256, decoded.ChecksumSHA256)
	assert.Equal(t, a.UploadedBy, decoded.UploadedBy)
	assert.Equal(t, a.UploadedByType, decoded.UploadedByType)
	assert.JSONEq(t, `{"width":1920,"height":1080}`, string(decoded.Metadata))

	// Verify JSON keys
	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)
	assert.Contains(t, raw, "artifact_type")
	assert.Contains(t, raw, "mime_type")
	assert.Contains(t, raw, "storage_key")
	assert.Contains(t, raw, "storage_url")
	assert.Contains(t, raw, "size_bytes")
	assert.Contains(t, raw, "checksum_sha256")
	assert.Contains(t, raw, "uploaded_by")
	assert.Contains(t, raw, "uploaded_by_type")
}

func TestArtifact_ZeroValue(t *testing.T) {
	var a Artifact
	data, err := json.Marshal(a)
	require.NoError(t, err)

	var decoded Artifact
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, uuid.Nil, decoded.ID)
	assert.Equal(t, int64(0), decoded.SizeBytes)
	assert.Equal(t, ArtifactType(""), decoded.ArtifactType)
}

func TestAgent_JSONSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	heartbeat := now.Add(-5 * time.Minute)
	currentTaskID := uuid.New()

	agent := Agent{
		ID:                  uuid.New(),
		WorkspaceID:         uuid.New(),
		Name:                "Claude Dev Agent",
		Slug:                "claude-dev",
		AgentType:           AgentTypeClaudeCode,
		APIKeyHash:          "sha256_secret_hash_value",
		APIKeyPrefix:        "evc_ak_abc",
		Capabilities:        json.RawMessage(`["code_review","testing","refactor"]`),
		Status:              AgentStatusOnline,
		LastHeartbeat:       &heartbeat,
		CurrentTaskID:       &currentTaskID,
		Settings:            json.RawMessage(`{"max_concurrent":3}`),
		TotalTasksCompleted: 42,
		TotalErrors:         2,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	data, err := json.Marshal(agent)
	require.NoError(t, err)

	var decoded Agent
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, agent.ID, decoded.ID)
	assert.Equal(t, agent.WorkspaceID, decoded.WorkspaceID)
	assert.Equal(t, agent.Name, decoded.Name)
	assert.Equal(t, agent.Slug, decoded.Slug)
	assert.Equal(t, agent.AgentType, decoded.AgentType)
	assert.Equal(t, agent.APIKeyPrefix, decoded.APIKeyPrefix)
	assert.Equal(t, agent.Status, decoded.Status)
	require.NotNil(t, decoded.LastHeartbeat)
	require.NotNil(t, decoded.CurrentTaskID)
	assert.Equal(t, currentTaskID, *decoded.CurrentTaskID)
	assert.Equal(t, agent.TotalTasksCompleted, decoded.TotalTasksCompleted)
	assert.Equal(t, agent.TotalErrors, decoded.TotalErrors)
	assert.JSONEq(t, `["code_review","testing","refactor"]`, string(decoded.Capabilities))
	assert.JSONEq(t, `{"max_concurrent":3}`, string(decoded.Settings))
}

func TestAgent_APIKeyHash_ExcludedFromJSON(t *testing.T) {
	agent := Agent{
		ID:           uuid.New(),
		WorkspaceID:  uuid.New(),
		Name:         "Test Agent",
		Slug:         "test-agent",
		AgentType:    AgentTypeCustom,
		APIKeyHash:   "super_secret_hash_must_not_appear",
		APIKeyPrefix: "evc_ak_xyz",
		Status:       AgentStatusOffline,
	}

	data, err := json.Marshal(agent)
	require.NoError(t, err)

	// APIKeyHash must NOT appear in JSON output
	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)

	assert.NotContains(t, raw, "api_key_hash")
	assert.NotContains(t, string(data), "super_secret_hash_must_not_appear")

	// APIKeyPrefix SHOULD appear
	assert.Contains(t, raw, "api_key_prefix")
}

func TestAgent_APIKeyHash_NotRestoredFromJSON(t *testing.T) {
	// Even if someone crafts JSON with api_key_hash, it should not be unmarshaled
	jsonData := `{
		"id": "00000000-0000-0000-0000-000000000001",
		"workspace_id": "00000000-0000-0000-0000-000000000002",
		"name": "Test",
		"slug": "test",
		"agent_type": "custom",
		"api_key_hash": "injected_hash",
		"api_key_prefix": "evc_ak_",
		"status": "offline"
	}`

	var agent Agent
	err := json.Unmarshal([]byte(jsonData), &agent)
	require.NoError(t, err)

	// APIKeyHash should remain empty because json:"-" tag prevents unmarshaling
	assert.Equal(t, "", agent.APIKeyHash)
}

func TestAgent_NullableFields(t *testing.T) {
	agent := Agent{
		ID:          uuid.New(),
		WorkspaceID: uuid.New(),
		Name:        "No task agent",
		AgentType:   AgentTypeAider,
		Status:      AgentStatusOffline,
	}

	data, err := json.Marshal(agent)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)

	assert.Equal(t, "null", string(raw["last_heartbeat"]))
	assert.Equal(t, "null", string(raw["current_task_id"]))
}

func TestAgent_ZeroValue(t *testing.T) {
	var agent Agent
	data, err := json.Marshal(agent)
	require.NoError(t, err)

	var decoded Agent
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, uuid.Nil, decoded.ID)
	assert.Equal(t, "", decoded.Name)
	assert.Equal(t, AgentType(""), decoded.AgentType)
	assert.Equal(t, AgentStatus(""), decoded.Status)
	assert.Equal(t, 0, decoded.TotalTasksCompleted)
	assert.Equal(t, 0, decoded.TotalErrors)
	assert.Nil(t, decoded.LastHeartbeat)
	assert.Nil(t, decoded.CurrentTaskID)
	assert.Equal(t, "", decoded.APIKeyHash)
}

func TestEventBusMessage_JSONSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	taskID := uuid.New()
	agentID := uuid.New()
	expiresAt := now.Add(24 * time.Hour)

	msg := EventBusMessage{
		ID:          uuid.New(),
		WorkspaceID: uuid.New(),
		ProjectID:   uuid.New(),
		TaskID:      &taskID,
		AgentID:     &agentID,
		EventType:   EventTypeStatusChange,
		Subject:     "task.status.changed",
		Payload:     json.RawMessage(`{"old":"todo","new":"in_progress"}`),
		Tags:        pq.StringArray{"status", "transition"},
		TTL:         "24h",
		CreatedAt:   now,
		ExpiresAt:   &expiresAt,
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded EventBusMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.ID, decoded.ID)
	assert.Equal(t, msg.WorkspaceID, decoded.WorkspaceID)
	assert.Equal(t, msg.ProjectID, decoded.ProjectID)
	require.NotNil(t, decoded.TaskID)
	assert.Equal(t, taskID, *decoded.TaskID)
	require.NotNil(t, decoded.AgentID)
	assert.Equal(t, agentID, *decoded.AgentID)
	assert.Equal(t, msg.EventType, decoded.EventType)
	assert.Equal(t, msg.Subject, decoded.Subject)
	assert.Equal(t, msg.TTL, decoded.TTL)
	assert.JSONEq(t, `{"old":"todo","new":"in_progress"}`, string(decoded.Payload))
	assert.Equal(t, pq.StringArray{"status", "transition"}, decoded.Tags)
}

func TestEventBusMessage_Tags_PqStringArray(t *testing.T) {
	tests := []struct {
		name string
		tags pq.StringArray
	}{
		{"multiple_tags", pq.StringArray{"a", "b", "c"}},
		{"single_tag", pq.StringArray{"only"}},
		{"empty_tags", pq.StringArray{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := EventBusMessage{
				ID:        uuid.New(),
				EventType: EventTypeCustom,
				Tags:      tt.tags,
			}
			data, err := json.Marshal(msg)
			require.NoError(t, err)

			var decoded EventBusMessage
			err = json.Unmarshal(data, &decoded)
			require.NoError(t, err)

			assert.Equal(t, tt.tags, decoded.Tags)
		})
	}
}

func TestEventBusMessage_NullableFields(t *testing.T) {
	msg := EventBusMessage{
		ID:        uuid.New(),
		EventType: EventTypeSummary,
		Subject:   "daily.summary",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)

	assert.Equal(t, "null", string(raw["task_id"]))
	assert.Equal(t, "null", string(raw["agent_id"]))
	assert.Equal(t, "null", string(raw["expires_at"]))
}

func TestEventBusMessage_ZeroValue(t *testing.T) {
	var msg EventBusMessage
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded EventBusMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, uuid.Nil, decoded.ID)
	assert.Equal(t, EventType(""), decoded.EventType)
	assert.Nil(t, decoded.TaskID)
	assert.Nil(t, decoded.AgentID)
	assert.Nil(t, decoded.ExpiresAt)
}

func TestActivityLog_JSONSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	al := ActivityLog{
		ID:          uuid.New(),
		WorkspaceID: uuid.New(),
		EntityType:  "task",
		EntityID:    uuid.New(),
		Action:      "status_changed",
		ActorID:     uuid.New(),
		ActorType:   ActorTypeAgent,
		Changes:     json.RawMessage(`{"status_id":{"old":"uuid1","new":"uuid2"}}`),
		CreatedAt:   now,
	}

	data, err := json.Marshal(al)
	require.NoError(t, err)

	var decoded ActivityLog
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, al.ID, decoded.ID)
	assert.Equal(t, al.WorkspaceID, decoded.WorkspaceID)
	assert.Equal(t, al.EntityType, decoded.EntityType)
	assert.Equal(t, al.EntityID, decoded.EntityID)
	assert.Equal(t, al.Action, decoded.Action)
	assert.Equal(t, al.ActorID, decoded.ActorID)
	assert.Equal(t, al.ActorType, decoded.ActorType)
	assert.JSONEq(t, `{"status_id":{"old":"uuid1","new":"uuid2"}}`, string(decoded.Changes))

	// Verify JSON keys
	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)
	assert.Contains(t, raw, "entity_type")
	assert.Contains(t, raw, "entity_id")
	assert.Contains(t, raw, "actor_id")
	assert.Contains(t, raw, "actor_type")
	assert.Contains(t, raw, "changes")
}

func TestActivityLog_ZeroValue(t *testing.T) {
	var al ActivityLog
	data, err := json.Marshal(al)
	require.NoError(t, err)

	var decoded ActivityLog
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, uuid.Nil, decoded.ID)
	assert.Equal(t, "", decoded.EntityType)
	assert.Equal(t, "", decoded.Action)
	assert.Equal(t, ActorType(""), decoded.ActorType)
}

func TestCustomFieldDefinition_JSONSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfd := CustomFieldDefinition{
		ID:                uuid.New(),
		ProjectID:         uuid.New(),
		Name:              "Sprint",
		Slug:              "sprint",
		FieldType:         FieldTypeSelect,
		Description:       "Sprint assignment",
		Options:           json.RawMessage(`["Sprint 1","Sprint 2","Sprint 3"]`),
		DefaultValue:      json.RawMessage(`"Sprint 1"`),
		IsRequired:        true,
		IsVisibleToAgents: true,
		Position:          1,
		CreatedAt:         now,
	}

	data, err := json.Marshal(cfd)
	require.NoError(t, err)

	var decoded CustomFieldDefinition
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, cfd.ID, decoded.ID)
	assert.Equal(t, cfd.ProjectID, decoded.ProjectID)
	assert.Equal(t, cfd.Name, decoded.Name)
	assert.Equal(t, cfd.Slug, decoded.Slug)
	assert.Equal(t, cfd.FieldType, decoded.FieldType)
	assert.Equal(t, cfd.Description, decoded.Description)
	assert.Equal(t, cfd.IsRequired, decoded.IsRequired)
	assert.Equal(t, cfd.IsVisibleToAgents, decoded.IsVisibleToAgents)
	assert.Equal(t, cfd.Position, decoded.Position)
	assert.JSONEq(t, `["Sprint 1","Sprint 2","Sprint 3"]`, string(decoded.Options))
	assert.JSONEq(t, `"Sprint 1"`, string(decoded.DefaultValue))

	// Verify JSON keys
	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)
	assert.Contains(t, raw, "field_type")
	assert.Contains(t, raw, "is_required")
	assert.Contains(t, raw, "is_visible_to_agents")
	assert.Contains(t, raw, "default_value")
	assert.Contains(t, raw, "options")
}

func TestCustomFieldDefinition_ZeroValue(t *testing.T) {
	var cfd CustomFieldDefinition
	data, err := json.Marshal(cfd)
	require.NoError(t, err)

	var decoded CustomFieldDefinition
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, uuid.Nil, decoded.ID)
	assert.Equal(t, FieldType(""), decoded.FieldType)
	assert.False(t, decoded.IsRequired)
	assert.False(t, decoded.IsVisibleToAgents)
	assert.Equal(t, 0, decoded.Position)
}

func TestTaskDependency_JSONSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	td := TaskDependency{
		ID:              uuid.New(),
		TaskID:          uuid.New(),
		DependsOnTaskID: uuid.New(),
		DependencyType:  DependencyTypeBlocks,
		CreatedAt:       now,
	}

	data, err := json.Marshal(td)
	require.NoError(t, err)

	var decoded TaskDependency
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, td.ID, decoded.ID)
	assert.Equal(t, td.TaskID, decoded.TaskID)
	assert.Equal(t, td.DependsOnTaskID, decoded.DependsOnTaskID)
	assert.Equal(t, td.DependencyType, decoded.DependencyType)

	// Verify JSON keys
	var raw map[string]json.RawMessage
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)
	assert.Contains(t, raw, "task_id")
	assert.Contains(t, raw, "depends_on_task_id")
	assert.Contains(t, raw, "dependency_type")
	assert.Contains(t, raw, "created_at")
}

func TestTaskDependency_ZeroValue(t *testing.T) {
	var td TaskDependency
	data, err := json.Marshal(td)
	require.NoError(t, err)

	var decoded TaskDependency
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, uuid.Nil, decoded.ID)
	assert.Equal(t, uuid.Nil, decoded.TaskID)
	assert.Equal(t, uuid.Nil, decoded.DependsOnTaskID)
	assert.Equal(t, DependencyType(""), decoded.DependencyType)
}

// ---------------------------------------------------------------------------
// JSON deserialization from raw JSON strings
// ---------------------------------------------------------------------------

func TestTask_DeserializeFromJSON(t *testing.T) {
	id := uuid.New()
	projectID := uuid.New()
	statusID := uuid.New()
	createdBy := uuid.New()

	jsonStr := `{
		"id": "` + id.String() + `",
		"project_id": "` + projectID.String() + `",
		"status_id": "` + statusID.String() + `",
		"title": "Test task",
		"description": "A test task",
		"assignee_id": null,
		"assignee_type": "unassigned",
		"priority": "medium",
		"parent_task_id": null,
		"position": 2.5,
		"due_date": null,
		"estimated_hours": null,
		"custom_fields": {"key": "value"},
		"labels": ["label1", "label2"],
		"created_by": "` + createdBy.String() + `",
		"created_by_type": "user",
		"created_at": "2025-01-15T10:30:00Z",
		"updated_at": "2025-01-15T10:30:00Z",
		"completed_at": null
	}`

	var task Task
	err := json.Unmarshal([]byte(jsonStr), &task)
	require.NoError(t, err)

	assert.Equal(t, id, task.ID)
	assert.Equal(t, projectID, task.ProjectID)
	assert.Equal(t, statusID, task.StatusID)
	assert.Equal(t, "Test task", task.Title)
	assert.Equal(t, "A test task", task.Description)
	assert.Nil(t, task.AssigneeID)
	assert.Equal(t, AssigneeTypeUnassigned, task.AssigneeType)
	assert.Equal(t, PriorityMedium, task.Priority)
	assert.Nil(t, task.ParentTaskID)
	assert.Equal(t, 2.5, task.Position)
	assert.Nil(t, task.DueDate)
	assert.Nil(t, task.EstimatedHours)
	assert.JSONEq(t, `{"key":"value"}`, string(task.CustomFields))
	assert.Equal(t, pq.StringArray{"label1", "label2"}, task.Labels)
	assert.Equal(t, createdBy, task.CreatedBy)
	assert.Equal(t, ActorTypeUser, task.CreatedByType)
	assert.Nil(t, task.CompletedAt)
}

func TestAgent_DeserializeFromJSON(t *testing.T) {
	id := uuid.New()
	wsID := uuid.New()

	jsonStr := `{
		"id": "` + id.String() + `",
		"workspace_id": "` + wsID.String() + `",
		"name": "Test Agent",
		"slug": "test-agent",
		"agent_type": "claude_code",
		"api_key_prefix": "evc_ak_test",
		"capabilities": ["code"],
		"status": "online",
		"last_heartbeat": "2025-06-01T12:00:00Z",
		"current_task_id": null,
		"settings": {},
		"total_tasks_completed": 10,
		"total_errors": 1,
		"created_at": "2025-01-01T00:00:00Z",
		"updated_at": "2025-06-01T12:00:00Z"
	}`

	var agent Agent
	err := json.Unmarshal([]byte(jsonStr), &agent)
	require.NoError(t, err)

	assert.Equal(t, id, agent.ID)
	assert.Equal(t, wsID, agent.WorkspaceID)
	assert.Equal(t, "Test Agent", agent.Name)
	assert.Equal(t, AgentTypeClaudeCode, agent.AgentType)
	assert.Equal(t, "evc_ak_test", agent.APIKeyPrefix)
	assert.Equal(t, AgentStatusOnline, agent.Status)
	require.NotNil(t, agent.LastHeartbeat)
	assert.Nil(t, agent.CurrentTaskID)
	assert.Equal(t, 10, agent.TotalTasksCompleted)
	assert.Equal(t, 1, agent.TotalErrors)
	// APIKeyHash should not be populated from JSON
	assert.Equal(t, "", agent.APIKeyHash)
}
