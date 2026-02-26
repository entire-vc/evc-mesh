-- +goose Up

ALTER TABLE agents ADD COLUMN IF NOT EXISTS role VARCHAR(50) NOT NULL DEFAULT 'developer';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS responsibility_zone TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS escalation_to JSONB DEFAULT NULL;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS accepts_from JSONB NOT NULL DEFAULT '["*"]';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS max_concurrent_tasks INT NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS working_hours VARCHAR(100) NOT NULL DEFAULT '24/7';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS profile_description TEXT NOT NULL DEFAULT '';

-- NOTE: agents.capabilities column already exists (json.RawMessage in domain)

-- Extend workspace_members with profile fields
ALTER TABLE workspace_members ADD COLUMN IF NOT EXISTS capabilities JSONB NOT NULL DEFAULT '[]';
ALTER TABLE workspace_members ADD COLUMN IF NOT EXISTS responsibility_zone TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_members ADD COLUMN IF NOT EXISTS availability VARCHAR(50) NOT NULL DEFAULT 'business-hours';

-- +goose Down

ALTER TABLE agents DROP COLUMN IF EXISTS role;
ALTER TABLE agents DROP COLUMN IF EXISTS responsibility_zone;
ALTER TABLE agents DROP COLUMN IF EXISTS escalation_to;
ALTER TABLE agents DROP COLUMN IF EXISTS accepts_from;
ALTER TABLE agents DROP COLUMN IF EXISTS max_concurrent_tasks;
ALTER TABLE agents DROP COLUMN IF EXISTS working_hours;
ALTER TABLE agents DROP COLUMN IF EXISTS profile_description;

ALTER TABLE workspace_members DROP COLUMN IF EXISTS capabilities;
ALTER TABLE workspace_members DROP COLUMN IF EXISTS responsibility_zone;
ALTER TABLE workspace_members DROP COLUMN IF EXISTS availability;
