-- +goose Up

-- Semantic category for task statuses (what does this status mean?)
CREATE TYPE task_status_category AS ENUM (
    'backlog',
    'todo',
    'in_progress',
    'review',
    'done',
    'cancelled'
);

-- Default assignee type for project-level setting
CREATE TYPE default_assignee_type AS ENUM (
    'user',
    'agent',
    'none'
);

-- Assignee type for tasks
CREATE TYPE assignee_type AS ENUM (
    'user',
    'agent',
    'unassigned'
);

-- Task priority levels
CREATE TYPE task_priority AS ENUM (
    'urgent',
    'high',
    'medium',
    'low',
    'none'
);

-- Dependency relationship between tasks
CREATE TYPE dependency_type AS ENUM (
    'blocks',
    'relates_to',
    'is_child_of'
);

-- Actor who performed an action (user, agent, or system)
CREATE TYPE actor_type AS ENUM (
    'user',
    'agent',
    'system'
);

-- Custom field data types
CREATE TYPE custom_field_type AS ENUM (
    'text',
    'number',
    'date',
    'datetime',
    'select',
    'multiselect',
    'url',
    'email',
    'checkbox',
    'user_ref',
    'agent_ref',
    'json'
);

-- AI agent type
CREATE TYPE agent_type AS ENUM (
    'claude_code',
    'openclaw',
    'cline',
    'aider',
    'custom'
);

-- Agent operational status
CREATE TYPE agent_status AS ENUM (
    'online',
    'offline',
    'busy',
    'error'
);

-- Artifact classification
CREATE TYPE artifact_type AS ENUM (
    'file',
    'code',
    'log',
    'report',
    'link',
    'image',
    'data'
);

-- Who uploaded an artifact
CREATE TYPE uploader_type AS ENUM (
    'user',
    'agent'
);

-- Event bus message types
CREATE TYPE event_type AS ENUM (
    'summary',
    'status_change',
    'context_update',
    'error',
    'dependency_resolved',
    'custom'
);

-- Workspace membership roles
CREATE TYPE workspace_role AS ENUM (
    'owner',
    'admin',
    'member',
    'viewer'
);

-- Project membership roles
CREATE TYPE project_role AS ENUM (
    'admin',
    'member',
    'viewer'
);

-- +goose Down
DROP TYPE IF EXISTS project_role;
DROP TYPE IF EXISTS workspace_role;
DROP TYPE IF EXISTS event_type;
DROP TYPE IF EXISTS uploader_type;
DROP TYPE IF EXISTS artifact_type;
DROP TYPE IF EXISTS agent_status;
DROP TYPE IF EXISTS agent_type;
DROP TYPE IF EXISTS custom_field_type;
DROP TYPE IF EXISTS actor_type;
DROP TYPE IF EXISTS dependency_type;
DROP TYPE IF EXISTS task_priority;
DROP TYPE IF EXISTS assignee_type;
DROP TYPE IF EXISTS default_assignee_type;
DROP TYPE IF EXISTS task_status_category;
