package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// This file deliberately carries no build tag, for the same reason
// user_repo_test.go does not: SearchAddableUsers is a tenant boundary expressed
// entirely in SQL, and a boundary that only ever runs behind
// `//go:build integration` is not measured by the coverage gate and not run by
// the default `go test ./...`. It skips when no Postgres is reachable, so a
// local run without docker still passes.

// searchScopeFixture builds three accounts across two unrelated workspaces:
//
//	caller    — owner of workspace A, the person doing the searching
//	colleague — also in workspace A, so already visible to caller via /members
//	stranger  — only in workspace B, which caller has nothing to do with
//
// All three share a unique token in their display name, address and username,
// so an unbounded query on it would return every one of them.
type searchScopeFixture struct {
	db                          *sqlx.DB
	repo                        *UserRepo
	caller, colleague, stranger uuid.UUID
	colleagueEmail              string
	strangerEmail               string
	token                       string
}

func newSearchScopeFixture(t *testing.T) searchScopeFixture {
	t.Helper()
	db := userRepoTestDB(t)
	ctx := context.Background()
	token := "zzscope" + strings.ReplaceAll(uuid.New().String()[:8], "-", "")

	mkWorkspace := func(name string) uuid.UUID {
		id := uuid.New()
		_, err := db.ExecContext(ctx,
			`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			id, name, "zzs-"+uuid.New().String()[:8], uuid.New(),
			json.RawMessage(`{}`), time.Now().UTC(), time.Now().UTC())
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = db.ExecContext(ctx, "DELETE FROM workspace_members WHERE workspace_id = $1", id)
			_, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", id)
		})
		return id
	}

	repo := NewUserRepo(db)
	mkUser := func(label string) (uuid.UUID, string) {
		u := &domain.User{
			ID:           uuid.New(),
			Email:        token + "-" + label + "@test.example",
			PasswordHash: "irrelevant-hash",
			Name:         strings.ToUpper(token[:1]) + token[1:] + " " + label,
			Username:     token + "-" + label,
			IsActive:     true,
			CreatedAt:    time.Now().UTC(),
			UpdatedAt:    time.Now().UTC(),
		}
		require.NoError(t, repo.Create(ctx, u))
		t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM users WHERE id = $1", u.ID) })
		return u.ID, u.Email
	}

	join := func(ws, user uuid.UUID, role string) {
		_, err := db.ExecContext(ctx,
			`INSERT INTO workspace_members (id, workspace_id, user_id, role, invited_by, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,NULL,$5,$6)`,
			uuid.New(), ws, user, role, time.Now().UTC(), time.Now().UTC())
		require.NoError(t, err)
	}

	wsA, wsB := mkWorkspace("Zzscope A"), mkWorkspace("Zzscope B")
	caller, _ := mkUser("caller")
	colleague, colleagueEmail := mkUser("colleague")
	stranger, strangerEmail := mkUser("stranger")

	join(wsA, caller, "owner")
	join(wsA, colleague, "member")
	join(wsB, stranger, "member")

	return searchScopeFixture{
		db: db, repo: repo,
		caller: caller, colleague: colleague, stranger: stranger,
		colleagueEmail: colleagueEmail, strangerEmail: strangerEmail, token: token,
	}
}

func (f searchScopeFixture) search(t *testing.T, callerID uuid.UUID, query string) map[uuid.UUID]domain.User {
	t.Helper()
	users, err := f.repo.SearchAddableUsers(context.Background(), callerID, query, 50)
	require.NoError(t, err)
	out := make(map[uuid.UUID]domain.User, len(users))
	for _, u := range users {
		out[u.ID] = u
	}
	return out
}

// The regression this change exists to prevent. A loose query must not return
// people the caller shares no workspace with — the instance-wide directory
// listing that "restrict the route to admins" did not close, because creating
// your own workspace makes you its owner and hands you that permission.
func TestSearchAddableUsers_LooseQueryCannotSeeAnotherTenant(t *testing.T) {
	f := newSearchScopeFixture(t)

	got := f.search(t, f.caller, f.token)

	assert.Contains(t, got, f.colleague,
		"somebody in the caller's own workspace is already visible to them via /members")
	assert.NotContains(t, got, f.stranger,
		"a user the caller shares no workspace with must not appear in a loose search")
}

// A single letter is the cheapest way to page a directory, and it is what the
// previous `ILIKE '%q%'` answered with every row in the users table.
func TestSearchAddableUsers_ShortQueryDoesNotPageTheInstance(t *testing.T) {
	f := newSearchScopeFixture(t)

	for _, q := range []string{"z", "zz", "@", "test.example"} {
		got := f.search(t, f.caller, q)
		assert.NotContains(t, got, f.stranger, "query %q must not enumerate other tenants", q)
	}
}

// ILIKE metacharacters are not a wildcard the caller supplies: "%" has to mean
// the literal character, or the enumeration comes back in one keystroke.
func TestSearchAddableUsers_WildcardsAreLiteral(t *testing.T) {
	f := newSearchScopeFixture(t)

	for _, q := range []string{"%", "_", "%%", "%stranger%", f.token[:4] + "%"} {
		got := f.search(t, f.caller, q)
		assert.NotContains(t, got, f.stranger, "query %q must not match another tenant's user", q)
		assert.NotContains(t, got, f.colleague,
			"query %q must be treated as literal text, so it matches nobody", q)
	}
}

// The deliberate exception: knowing somebody's exact address is the credential
// for inviting them, so it resolves across the instance. Without this, one
// account could never join a second workspace and the product would fork the
// identity instead.
func TestSearchAddableUsers_ExactAddressReachesAnyone(t *testing.T) {
	f := newSearchScopeFixture(t)

	got := f.search(t, f.caller, f.strangerEmail)
	assert.Contains(t, got, f.stranger, "an exact address must resolve — that is how cross-workspace adds work")
	assert.NotContains(t, got, f.colleague, "and only that account, not their workspace-mates")
}

// Case and padding must not defeat the exact-address rule: it has to agree with
// how the address is stored (ix_users_email_lower) and with every other lookup.
func TestSearchAddableUsers_ExactAddressIgnoresCaseAndPadding(t *testing.T) {
	f := newSearchScopeFixture(t)

	for _, spelling := range []string{
		f.strangerEmail,
		strings.ToUpper(f.strangerEmail),
		"  " + f.strangerEmail + "  ",
		"\t" + strings.ToUpper(f.strangerEmail) + "\n",
	} {
		got := f.search(t, f.caller, spelling)
		assert.Contains(t, got, f.stranger, "spelling %q must resolve to the same account", spelling)
	}
}

// A prefix of an address is not knowledge of the address, and must not fall
// through to the loose rule for somebody in another tenant.
func TestSearchAddableUsers_PartialAddressIsNotAnExactMatch(t *testing.T) {
	f := newSearchScopeFixture(t)

	partial := strings.TrimSuffix(f.strangerEmail, "e")
	got := f.search(t, f.caller, partial)
	assert.NotContains(t, got, f.stranger)
}

// An agent key authenticates a workspace, not a person, so there is no "shares
// a workspace with me" to compute. That must degrade to exact-address only,
// not to no boundary at all.
func TestSearchAddableUsers_NilCallerGetsExactMatchesOnly(t *testing.T) {
	f := newSearchScopeFixture(t)

	loose := f.search(t, uuid.Nil, f.token)
	assert.NotContains(t, loose, f.stranger)
	assert.NotContains(t, loose, f.colleague,
		"with no caller identity there is no shared-workspace set, so the loose rule matches nobody")

	exact := f.search(t, uuid.Nil, f.colleagueEmail)
	assert.Contains(t, exact, f.colleague, "the exact-address rule still applies")
}

// Deactivated accounts cannot be added, so disclosing them buys nothing.
func TestSearchAddableUsers_SkipsInactiveAccounts(t *testing.T) {
	f := newSearchScopeFixture(t)

	_, err := f.db.ExecContext(context.Background(),
		"UPDATE users SET is_active = false WHERE id = $1", f.colleague)
	require.NoError(t, err)

	assert.NotContains(t, f.search(t, f.caller, f.colleagueEmail), f.colleague)
	assert.NotContains(t, f.search(t, f.caller, f.token), f.colleague)
}

// The exact match sorts first: it is the one result the caller actually asked
// for, and it must not be pushed off the end of the limit by loose matches.
func TestSearchAddableUsers_ExactMatchSortsFirst(t *testing.T) {
	f := newSearchScopeFixture(t)

	users, err := f.repo.SearchAddableUsers(context.Background(), f.caller, f.colleagueEmail, 50)
	require.NoError(t, err)
	require.NotEmpty(t, users)
	assert.Equal(t, f.colleague, users[0].ID)
}

// The projection has to carry username and the self-set flag, because the
// service builds the member/search payload straight off this struct.
func TestSearchAddableUsers_ProjectionCarriesUsernameAndProvenance(t *testing.T) {
	f := newSearchScopeFixture(t)

	got := f.search(t, f.caller, f.colleagueEmail)
	require.Contains(t, got, f.colleague)
	u := got[f.colleague]
	assert.Equal(t, f.token+"-colleague", u.Username)
	assert.False(t, u.DisplayNameSelfSet)
	assert.False(t, u.NameIsPlaceholder(), "this fixture user has a real name")
}

// The member list is where the operator actually reads names, so its projection
// has to carry the same fields the search does — including username, which is
// what lets the UI disambiguate two people without printing an address.
func TestWorkspaceMemberRepo_ListCarriesUsername(t *testing.T) {
	f := newSearchScopeFixture(t)
	ctx := context.Background()

	// The colleague's workspace is the caller's; find it through their membership.
	var wsID uuid.UUID
	require.NoError(t, f.db.GetContext(ctx, &wsID,
		"SELECT workspace_id FROM workspace_members WHERE user_id = $1", f.colleague))

	members, err := NewWorkspaceMemberRepo(f.db).List(ctx, wsID)
	require.NoError(t, err)
	require.NotEmpty(t, members)

	found := false
	for _, m := range members {
		if m.User.ID != f.colleague {
			continue
		}
		found = true
		assert.Equal(t, f.token+"-colleague", m.User.Username, "List must project username")
		assert.NotEmpty(t, m.User.Name)
		assert.False(t, m.User.NameIsPlaceholder())
	}
	assert.True(t, found, "the colleague must appear in their own workspace's member list")
}
