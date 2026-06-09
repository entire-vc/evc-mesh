-- Add thread_id to tasks for fiddler continuity (Ф4 / #83a1bc1c).
-- thread_id is an optional opaque string that groups tasks into a continuation
-- thread. The fiddler uses it to detect when successive tasks belong to the same
-- work stream and prepend a continuity hint to the agent prompt so it doesn't
-- re-derive context from scratch.
--
-- Stored as TEXT (not UUID) so callers can use any stable identifier: a task
-- UUID, a parent task UUID, an arbitrary slug, etc.

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS thread_id TEXT DEFAULT NULL;
