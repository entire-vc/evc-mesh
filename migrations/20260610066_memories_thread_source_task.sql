-- Amendments 2 & 3 to F1 memory KG (task #02b365b7).
--
-- thread_id: propagated from the source task's thread_id at write time by the service
--   layer. Allows cheap same-thread lookup without a JOIN to tasks.
-- source_task_id: the Mesh task UUID whose work produced this memory. Used to walk
--   the task graph (parent_task / depends_on) and create derived_from edges (Amendment 3).

ALTER TABLE memories ADD COLUMN IF NOT EXISTS thread_id TEXT DEFAULT NULL;
ALTER TABLE memories ADD COLUMN IF NOT EXISTS source_task_id UUID DEFAULT NULL;

CREATE INDEX IF NOT EXISTS idx_memories_thread_id
    ON memories(thread_id)
    WHERE thread_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_memories_source_task_id
    ON memories(source_task_id)
    WHERE source_task_id IS NOT NULL;
