// No //go:build integration tag — same convention as the other *_db_test.go
// files here; the coverage job has Postgres, a plain `go test` without one skips.

package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

func TestInstanceExtensionSettingRepo_EncryptsSecretsAndInsertIsIdempotent(t *testing.T) {
	t.Setenv(encryption.EnvKey, "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=") // 32 bytes, test only
	encryption.ResetForTest()
	t.Cleanup(encryption.ResetForTest)
	state, _ := encryption.Status()
	require.Equal(t, encryption.KeyOK, state)

	db := agentDigestTestDB(t)
	ctx := context.Background()
	repo := NewInstanceExtensionSettingRepo(db)
	const id = "ext-instance-test"
	t.Cleanup(func() { db.Exec(`DELETE FROM instance_extension_settings WHERE extension_id = $1`, id) })

	row := &domain.InstanceExtensionSetting{
		ExtensionID: id, Enabled: true, Source: domain.InstanceSettingSourceEnv,
		Settings: json.RawMessage(`{"base_url":"https://x","token":"s3cret"}`),
	}
	ins, err := repo.InsertIfAbsent(ctx, row, []string{"token"})
	require.NoError(t, err)
	require.True(t, ins)

	// at rest the secret is ciphertext, the plain field is not
	var raw string
	require.NoError(t, db.Get(&raw, `SELECT settings::text FROM instance_extension_settings WHERE extension_id = $1`, id))
	require.NotContains(t, raw, "s3cret")
	require.Contains(t, raw, "https://x")

	got, err := repo.Get(ctx, id)
	require.NoError(t, err)
	require.JSONEq(t, `{"base_url":"https://x","token":"s3cret"}`, string(got.Settings))

	// second insert (next start) changes nothing
	row.Settings = json.RawMessage(`{"base_url":"https://other"}`)
	ins, err = repo.InsertIfAbsent(ctx, row, []string{"token"})
	require.NoError(t, err)
	require.False(t, ins)

	// admin upsert replaces
	row.Source = domain.InstanceSettingSourceAdmin
	require.NoError(t, repo.Upsert(ctx, row, []string{"token"}))
	got, err = repo.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, domain.InstanceSettingSourceAdmin, got.Source)

	none, err := repo.Get(ctx, "no-such-ext")
	require.NoError(t, err)
	require.Nil(t, none)
}

func TestInstanceExtensionSettingRepo_IsInstanceAdminFalseForStranger(t *testing.T) {
	db := agentDigestTestDB(t)
	ok, err := NewInstanceExtensionSettingRepo(db).IsInstanceAdmin(context.Background(), uuid.New())
	require.NoError(t, err)
	require.False(t, ok, "a user with no membership is not an instance admin")
}

func TestInstanceExtensionSettingRepo_SettingsMustBeAnObject(t *testing.T) {
	_, err := encryptSettings(json.RawMessage(`[1,2]`), []string{"token"})
	require.ErrorContains(t, err, "JSON object")

	_, err = decryptSettings(json.RawMessage(`[1,2]`))
	require.Error(t, err)

	// a secret that is already ciphertext is not encrypted twice
	t.Setenv(encryption.EnvKey, "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	encryption.ResetForTest()
	t.Cleanup(encryption.ResetForTest)
	once, err := encryptSettings(json.RawMessage(`{"token":"s3cret"}`), []string{"token"})
	require.NoError(t, err)
	twice, err := encryptSettings(once, []string{"token"})
	require.NoError(t, err)
	require.JSONEq(t, string(once), string(twice))
}

// Instance admin = human owner/admin of the oldest non-deleted workspace
// (decision on card 807ac4ed). The fixture workspaces are dated in 4000 BC (the shared CI DB holds zero-time
// workspaces, 1970 is not old enough) so they are the oldest regardless of what else lives in the test database.
func TestInstanceExtensionSettingRepo_IsInstanceAdminOldestWorkspace(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	repo := NewInstanceExtensionSettingRepo(db)

	sfx := uuid.NewString()[:8]
	mkUser := func(tag string) uuid.UUID {
		id := uuid.New()
		_, err := db.Exec(`INSERT INTO users(id,email,password_hash,display_name,username) VALUES($1,$2,'x',$3,$4)`,
			id, tag+sfx+"@example.test", tag, tag+sfx)
		require.NoError(t, err)
		return id
	}
	oldOwner, oldAdmin, oldMember := mkUser("oldowner"), mkUser("oldadmin"), mkUser("oldmember")
	youngAdmin, nextAdmin := mkUser("youngadmin"), mkUser("nextadmin")

	mkWS := func(tag string, owner uuid.UUID, created string) uuid.UUID {
		id := uuid.New()
		_, err := db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id,created_at) VALUES($1,$2,$3,$4,$5)`,
			id, tag, tag+sfx, owner, created)
		require.NoError(t, err)
		return id
	}
	addMember := func(ws, user uuid.UUID, role string) {
		_, err := db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES($1,$2,$3,$4)`,
			uuid.New(), ws, user, role)
		require.NoError(t, err)
	}
	oldest := mkWS("iea-oldest", oldOwner, "4000-01-01 00:00:00+00 BC")
	next := mkWS("iea-next", nextAdmin, "4000-01-02 00:00:00+00 BC")
	young := mkWS("iea-young", youngAdmin, "4000-01-03 00:00:00+00 BC")
	addMember(oldest, oldAdmin, "admin")
	addMember(oldest, oldMember, "member")
	addMember(young, youngAdmin, "admin")
	t.Cleanup(func() {
		for _, ws := range []uuid.UUID{oldest, next, young} {
			db.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1`, ws)
			db.Exec(`DELETE FROM workspaces WHERE id = $1`, ws)
		}
		for _, u := range []uuid.UUID{oldOwner, oldAdmin, oldMember, youngAdmin, nextAdmin} {
			db.Exec(`DELETE FROM users WHERE id = $1`, u)
		}
	})

	is := func(u uuid.UUID) bool {
		ok, err := repo.IsInstanceAdmin(ctx, u)
		require.NoError(t, err)
		return ok
	}
	require.True(t, is(oldOwner), "owner of the oldest workspace")
	require.True(t, is(oldAdmin), "admin of the oldest workspace")
	require.False(t, is(oldMember), "plain member of the oldest workspace")
	require.False(t, is(youngAdmin), "admin of a younger workspace")
	require.False(t, is(nextAdmin), "owner of the second workspace while the oldest is alive")

	// soft-delete the oldest: the next non-deleted workspace becomes "oldest"
	_, err := db.Exec(`UPDATE workspaces SET deleted_at = NOW() WHERE id = $1`, oldest)
	require.NoError(t, err)
	require.False(t, is(oldOwner), "owner of a deleted workspace")
	require.False(t, is(oldAdmin), "admin of a deleted workspace")
	require.True(t, is(nextAdmin), "owner of the next non-deleted workspace")
	require.False(t, is(youngAdmin), "still younger than the next one")
}
