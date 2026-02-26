-- +goose Up

-- Add 'triage' value to task_status_category enum for the Triage Inbox feature.
ALTER TYPE task_status_category ADD VALUE IF NOT EXISTS 'triage';

-- +goose Down

-- PostgreSQL does not support removing enum values directly.
-- To reverse: recreate the type without 'triage' (requires dropping/recreating dependent columns).
