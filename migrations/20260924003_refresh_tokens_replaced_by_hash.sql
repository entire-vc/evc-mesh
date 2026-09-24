-- +goose Up

-- Links a revoked refresh token to whatever replaced it in the same
-- rotation. Populated only by RefreshTokenRepo.LinkSuccessor, called right
-- after the ONE conditional UPDATE that successfully rotates a still-live
-- token; a row revoked by RevokeByUserID (an explicit logout or a theft
-- detection) never gets this set. That distinction is what lets a later
-- replay of an already-revoked token be told apart as "died in an ordinary
-- rotation" vs "died because the whole session was declared compromised" —
-- see internal/auth/service.go, RefreshTokens / handleTokenReuse (#cfb14ad6).
ALTER TABLE refresh_tokens ADD COLUMN replaced_by_hash VARCHAR(128);

-- +goose Down
ALTER TABLE refresh_tokens DROP COLUMN replaced_by_hash;
