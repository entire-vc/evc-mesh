//go:build integration

package postgres

import (
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// agentChatMigration is the migration this file pins; agentChatPrev is the
// version right before it. Pinning (rather than "the newest migration", as
// TestNewestMigrationDownUpRoundTrip does) keeps the schema assertions alive
// after newer migrations land on top.
const (
	agentChatMigration int64 = 20261022001
	agentChatPrev      int64 = 20261021001
)

var agentChatTables = []string{
	"ext_agent_chat_attachments",
	"ext_agent_chat_commands",
	"ext_agent_chat_messages",
	"ext_agent_chat_runners",
	"ext_agent_chat_threads",
	"ext_agent_chat_tool_events",
	"ext_agent_chat_turns",
}

var agentChatIndexes = []string{
	"ext_agent_chat_attachments_msg",
	"ext_agent_chat_attachments_pending",
	"ext_agent_chat_commands_pending",
	"ext_agent_chat_messages_idem",
	"ext_agent_chat_messages_page",
	"ext_agent_chat_runners_agent",
	"ext_agent_chat_threads_list",
	"ext_agent_chat_threads_purge",
	"ext_agent_chat_turns_claim",
	"ext_agent_chat_turns_expiry",
	"ext_agent_chat_turns_lease",
	"ext_agent_chat_turns_one_active",
}

func agentChatStrings(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

// agentChatLeftovers lists every catalog object the migration owns, whatever
// its kind: tables, indexes, composite row types, constraints, RLS policies.
func agentChatLeftovers(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	return map[string][]string{
		"tables":      agentChatStrings(t, db, `SELECT tablename FROM pg_tables WHERE tablename LIKE 'ext\_agent\_chat\_%' ORDER BY 1`),
		"indexes":     agentChatStrings(t, db, `SELECT indexname FROM pg_indexes WHERE tablename LIKE 'ext\_agent\_chat\_%' ORDER BY 1`),
		"types":       agentChatStrings(t, db, `SELECT typname FROM pg_type WHERE typname LIKE '%ext\_agent\_chat\_%' ORDER BY 1`),
		"constraints": agentChatStrings(t, db, `SELECT conname FROM pg_constraint WHERE conname LIKE '%ext\_agent\_chat\_%' ORDER BY 1`),
		"policies":    agentChatStrings(t, db, `SELECT policyname FROM pg_policies WHERE tablename LIKE 'ext\_agent\_chat\_%' ORDER BY 1`),
	}
}

// TestAgentChatMigrationDownUpCatalog is the runtime check behind "down is
// verified": after Down not one ext_agent_chat_* object of any kind is left,
// after the second Up exactly the six tables, their named indexes and one RLS
// policy per table are back.
func TestAgentChatMigrationDownUpCatalog(t *testing.T) {
	db := testDB(t).DB
	const migrationsDir = "../../../migrations"

	require.GreaterOrEqual(t, gooseVersion(t, db), agentChatMigration, "the suite DB must be migrated past the agent-chat migration")

	require.NoError(t, goose.DownTo(db, migrationsDir, agentChatPrev))
	// Restore whatever the DB had, even when an assertion below fails.
	t.Cleanup(func() { require.NoError(t, goose.Up(db, migrationsDir)) })
	require.Equal(t, agentChatPrev, gooseVersion(t, db))
	for kind, names := range agentChatLeftovers(t, db) {
		require.Empty(t, names, "down left %s behind", kind)
	}

	require.NoError(t, goose.Up(db, migrationsDir))
	left := agentChatLeftovers(t, db)
	require.Equal(t, agentChatTables, left["tables"], "exactly the chat tables after the second up")
	require.Subset(t, left["indexes"], agentChatIndexes)
	require.Len(t, left["policies"], len(agentChatTables), "one RLS policy per table")
	var rlsOn int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM pg_class WHERE relname LIKE 'ext\_agent\_chat\_%' AND relkind='r' AND relrowsecurity`).Scan(&rlsOn))
	require.Equal(t, len(agentChatTables), rlsOn, "row level security enabled on every table")
}
