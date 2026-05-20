-- +goose Up
ALTER TABLE users ADD COLUMN IF NOT EXISTS username TEXT;

-- Backfill: derive username from email prefix, handle collisions with numeric suffix.
DO $$
DECLARE
    rec    RECORD;
    base   TEXT;
    cand   TEXT;
    suffix INT;
BEGIN
    FOR rec IN SELECT id, email FROM users WHERE username IS NULL ORDER BY created_at LOOP
        -- strip domain, lowercase, replace non-slug chars
        base := lower(split_part(rec.email, '@', 1));
        base := regexp_replace(base, '[^a-z0-9-]', '-', 'g');
        base := regexp_replace(base, '-{2,}', '-', 'g');
        base := trim(both '-' from base);
        -- clamp to 38 chars so suffix can fit in 40
        base := left(base, 38);
        -- ensure minimum length 2
        IF length(base) < 2 THEN
            base := base || '0';
        END IF;

        cand   := base;
        suffix := 0;
        LOOP
            EXIT WHEN NOT EXISTS (SELECT 1 FROM users WHERE lower(username) = lower(cand));
            suffix := suffix + 1;
            cand   := base || suffix::text;
        END LOOP;

        UPDATE users SET username = cand WHERE id = rec.id;
    END LOOP;
END;
$$;

-- Now make it NOT NULL and add the constraint + unique index.
ALTER TABLE users ALTER COLUMN username SET NOT NULL;
ALTER TABLE users ADD CONSTRAINT chk_users_username
    CHECK (username ~ '^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$');
CREATE UNIQUE INDEX ix_users_username ON users (lower(username));

-- +goose Down
DROP INDEX IF EXISTS ix_users_username;
ALTER TABLE users DROP CONSTRAINT IF EXISTS chk_users_username;
ALTER TABLE users DROP COLUMN IF EXISTS username;
