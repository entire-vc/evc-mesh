-- +goose Up

-- Local users table for Phase 1 (built-in JWT auth).
-- In Phase 5 this syncs with Casdoor via casdoor_user_id.
CREATE TABLE users (
    id              UUID PRIMARY KEY,
    email           VARCHAR(255) NOT NULL UNIQUE,
    password_hash   VARCHAR(255) NOT NULL,
    display_name    VARCHAR(255) NOT NULL,
    avatar_url      TEXT,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    last_login_at   TIMESTAMPTZ,
    casdoor_user_id VARCHAR(255),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_email ON users(email);
CREATE UNIQUE INDEX uq_users_casdoor_id ON users(casdoor_user_id)
    WHERE casdoor_user_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS users;
