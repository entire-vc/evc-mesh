# Custom Fields

Mesh supports 12 custom field types that extend tasks with project-specific data. Custom fields are defined per project and stored as JSONB on the tasks table for efficient querying.

## Field Types

| Type | Description | Example Value |
|------|-------------|---------------|
| `text` | Free-form text | `"Fix the login page"` |
| `number` | Numeric value (integer or float) | `42`, `3.14` |
| `date` | Date (YYYY-MM-DD) | `"2026-03-15"` |
| `datetime` | Date and time (ISO 8601) | `"2026-03-15T14:30:00Z"` |
| `select` | Single choice from predefined options | `"high"` |
| `multiselect` | Multiple choices from predefined options | `["frontend", "backend"]` |
| `url` | URL | `"https://github.com/issue/123"` |
| `email` | Email address | `"dev@example.com"` |
| `checkbox` | Boolean (true/false) | `true` |
| `user_ref` | Reference to a workspace user (UUID) | `"550e8400-..."` |
| `agent_ref` | Reference to a workspace agent (UUID) | `"6ba7b810-..."` |
| `json` | Arbitrary JSON data | `{"key": "value"}` |

## Creating Custom Fields

Custom fields are defined at the project level.

### REST API

```bash
curl -X POST http://localhost:8005/api/v1/projects/{project_id}/custom-fields \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Sprint",
    "slug": "sprint",
    "field_type": "select",
    "description": "Sprint assignment",
    "required": false,
    "options": [
      {"value": "sprint-1", "label": "Sprint 1", "color": "#3B82F6"},
      {"value": "sprint-2", "label": "Sprint 2", "color": "#10B981"},
      {"value": "sprint-3", "label": "Sprint 3", "color": "#F59E0B"}
    ]
  }'
```

### MCP Tool

```json
{
  "name": "create_task",
  "arguments": {
    "project_id": "...",
    "title": "My task",
    "custom_fields": {
      "sprint": "sprint-1",
      "story_points": 5,
      "reviewed": false
    }
  }
}
```

## Field Definition Properties

| Property | Type | Required | Description |
|----------|------|----------|-------------|
| `name` | string | Yes | Display name shown in the UI |
| `slug` | string | Yes | URL-safe identifier, must match `^[a-z][a-z0-9_]*$` |
| `field_type` | string | Yes | One of the 12 types listed above |
| `description` | string | No | Help text shown to users |
| `required` | boolean | No | Whether the field must have a value (default: false) |
| `options` | array | For select/multiselect | List of allowed values |
| `default_value` | any | No | Default value for new tasks |

## Options Format (select / multiselect)

Options can be specified in two formats:

### Full format (recommended)

```json
{
  "options": [
    {"value": "low", "label": "Low", "color": "#10B981"},
    {"value": "medium", "label": "Medium", "color": "#F59E0B"},
    {"value": "high", "label": "High", "color": "#EF4444"}
  ]
}
```

### Simple format

```json
{
  "options": ["low", "medium", "high"]
}
```

The API auto-detects which format is used and normalizes to the full format internally.

## Setting Custom Field Values

When creating or updating a task, include custom fields in the `custom_fields` object:

```bash
# Create task with custom fields
curl -X POST http://localhost:8005/api/v1/projects/{project_id}/tasks \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{
    "title": "Implement auth",
    "custom_fields": {
      "sprint": "sprint-2",
      "story_points": 8,
      "reviewed": false,
      "due_review": "2026-03-20",
      "tags": ["security", "backend"]
    }
  }'

# Update custom fields on existing task
curl -X PATCH http://localhost:8005/api/v1/tasks/{task_id} \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{
    "custom_fields": {
      "reviewed": true,
      "story_points": 5
    }
  }'
```

Custom fields are merged on update — you only need to include the fields you want to change.

## Filtering Tasks by Custom Fields

Use query parameters with the `custom.` prefix:

```bash
# Tasks where sprint = "sprint-2"
GET /projects/{id}/tasks?custom.sprint=sprint-2

# Tasks where story_points > 5
GET /projects/{id}/tasks?custom.story_points_gt=5

# Tasks where reviewed = true
GET /projects/{id}/tasks?custom.reviewed=true

# Tasks that have the "sprint" field set (any value)
GET /projects/{id}/tasks?custom.sprint=*
```

## Managing Field Definitions

### List Fields

```bash
GET /api/v1/projects/{project_id}/custom-fields
```

### Update a Field

```bash
PATCH /api/v1/custom-fields/{field_id}
```

You can update `name`, `description`, `required`, `options`, and `default_value`. Changing `field_type` or `slug` after creation is not supported.

### Delete a Field

```bash
DELETE /api/v1/custom-fields/{field_id}
```

Deleting a field definition does not remove existing values from tasks. The values remain in JSONB but are no longer displayed in the UI.

## Frontend Display

Custom fields appear in the task detail panel as a compact property grid. In the list view, custom fields are shown as sortable columns.

The UI automatically renders the appropriate input for each field type:
- `text` / `url` / `email` → text input
- `number` → number input
- `date` / `datetime` → date picker
- `select` → dropdown
- `multiselect` → multi-select dropdown with badges
- `checkbox` → toggle switch
- `user_ref` / `agent_ref` → member picker dropdown
- `json` → code editor

## Storage Details

Custom field values are stored in a JSONB column (`custom_fields`) on the `tasks` table:

```sql
-- Example task row
SELECT id, title, custom_fields FROM tasks WHERE id = '...';
-- custom_fields: {"sprint": "sprint-2", "story_points": 8, "reviewed": false}
```

A GIN index on the `custom_fields` column enables efficient JSONB queries:

```sql
CREATE INDEX idx_tasks_custom_fields ON tasks USING gin (custom_fields);
```

Field definitions are stored in the `custom_field_definitions` table, one row per field per project.
