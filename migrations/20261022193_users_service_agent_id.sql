-- +goose Up
-- A service user is a users row that acts on behalf of an agent (the QA
-- accounts such as "Hana (QA Mesh)"). Until now consumers told them apart
-- from people by display-name shape, which misroutes a real person who happens
-- to be called "Hana (QA X)". Record the link explicitly instead.
-- Additive and nullable: existing rows and API clients are unaffected.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS service_agent_id UUID REFERENCES agents(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_users_service_agent_id
    ON users(service_agent_id) WHERE service_agent_id IS NOT NULL;

-- Link the two service accounts that exist today by explicit id, checked
-- against the live data. Deliberately not a name match: a display name cannot
-- tell a service account from a person called "Hana (QA X)". No-op wherever
-- these ids do not exist (dev, CI, other installs).
UPDATE users u
   SET service_agent_id = m.agent_id
  FROM (VALUES
        ('5be89b13-f281-4ee4-9af5-fddd3e865939'::uuid, '5bb903e9-f7ba-46f4-95b4-a32aa8de20b5'::uuid),
        ('e1a900bc-385e-48af-a983-d7620fd58baa'::uuid, 'bbc8adcc-5592-463c-bf6a-0a7bf6078efe'::uuid)
       ) AS m(user_id, agent_id)
 WHERE u.id = m.user_id
   AND EXISTS (SELECT 1 FROM agents a WHERE a.id = m.agent_id);

-- +goose Down
DROP INDEX IF EXISTS idx_users_service_agent_id;
ALTER TABLE users DROP COLUMN IF EXISTS service_agent_id;
