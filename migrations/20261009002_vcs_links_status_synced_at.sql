-- +goose Up

-- vcs_links.status_synced_at: when a link's status was last written by a
-- status-carrying source (a provider webhook, or a manual re-link that states
-- an explicit status — both go through VCSLinkRepo.Upsert). A PR link sitting
-- at status=open is only as trustworthy as the last delivery that confirmed
-- it; a lost "closed"/"merged" webhook used to leave it open forever with
-- nothing to show how stale it was. NULL means "never confirmed since this
-- column existed" (every pre-existing row, and links created without a
-- status). Additive and nullable, no default rewrite: an older binary never
-- names it (vcsLinkSelectCols is an explicit list), so rolling the code back
-- alone is safe.
ALTER TABLE vcs_links ADD COLUMN IF NOT EXISTS status_synced_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE vcs_links DROP COLUMN IF EXISTS status_synced_at;
