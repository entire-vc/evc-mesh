package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// TestGateIntakeWiring pins the decisions of routes_gate_intake.go that the
// route audits cannot see: the guard is Echo-level (e.Use), the route requires
// the gate-intake principal and carries no `idempotent` answer cache (the
// receipt is the idempotency, without a TTL), the module is registered, and
// main() runs the operator subcommand before any module starts.
func TestGateIntakeWiring(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "routes_gate_intake.go", nil, 0)
	require.NoError(t, err)
	var eUse, route bool
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, _ := sel.X.(*ast.Ident)
		if recv == nil {
			return true
		}
		switch {
		case recv.Name == "e" && sel.Sel.Name == "Use":
			eUse = strings.Contains(exprString(call.Args[0]), "GateIntakeGuard")
		case recv.Name == "v1" && sel.Sel.Name == "POST":
			route = true
			var guards []string
			for _, a := range call.Args[2:] {
				guards = append(guards, exprString(a))
			}
			joined := strings.Join(guards, " ")
			assert.Contains(t, joined, "RequireGateIntakePrincipal")
			assert.NotContains(t, strings.ToLower(joined), "idempotent")
		}
		return true
	})
	assert.True(t, eUse, "GateIntakeGuard must be an Echo-level middleware (e.Use)")
	assert.True(t, route, "v1.POST(\"/gate-intake\", …) not found")

	mods, err := os.ReadFile("modules.go")
	require.NoError(t, err)
	assert.Contains(t, string(mods), "registerGateIntakeRoutes,")
	mainSrc, err := os.ReadFile("main.go")
	require.NoError(t, err)
	s := string(mainSrc)
	sub, mods1 := strings.Index(s, `os.Args[1] == "gate-intake"`), strings.Index(s, "range apiModules")
	require.True(t, sub > 0 && mods1 > 0)
	assert.Less(t, sub, mods1, "the subcommand must run before any module (no second server)")
}

// --- operator command against a real database ---

func gateIntakeCLIDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable" // pragma: allowlist secret
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestGateIntakeCLI_DB(t *testing.T) {
	db := gateIntakeCLIDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	userRepo := postgres.NewUserRepo(db)
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	authSvc := auth.NewService(userRepo, postgres.NewRefreshTokenRepo(db), workspaceRepo, postgres.NewWorkspaceMemberRepo(db), "gate-intake-cli-test-jwt-secret")
	agentSvc := service.NewAgentService(postgres.NewAgentRepo(db), postgres.NewActivityLogRepo(db), workspaceRepo, userRepo)
	register := func(tag string) uuid.UUID {
		t.Helper()
		u, _, err := authSvc.Register(ctx, fmt.Sprintf("gi-cli-%s-%s@e2e.example.com", tag, suffix), "Correct-Horse-Battery-Staple-1", "gi-cli-"+tag+"-"+suffix)
		require.NoError(t, err)
		wss, err := workspaceRepo.ListForUser(ctx, u.ID)
		require.NoError(t, err)
		require.NotEmpty(t, wss)
		return wss[0].ID
	}
	wsID, otherWS := register("a"), register("b")
	projID := uuid.New()
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, &domain.Project{ID: projID, WorkspaceID: wsID, Name: "gi-cli",
		Slug: "gi-cli-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone}))
	require.NoError(t, postgres.NewTaskStatusRepo(db).Create(ctx, &domain.TaskStatus{ID: uuid.New(), ProjectID: projID, Name: "Todo",
		Slug: "todo", Color: "#00FF00", Category: domain.StatusCategoryTodo, IsDefault: true}))
	ag, err := agentSvc.Register(ctx, service.RegisterAgentInput{WorkspaceID: wsID, Name: "gi-cli-" + suffix, AgentType: domain.AgentTypeCustom})
	require.NoError(t, err)
	agentID := ag.Agent.ID
	name := "gi-cli-" + suffix
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM gate_intake_audit WHERE principal_id IN (SELECT id FROM gate_intake_principals WHERE name = $1)`, name)
		_, _ = db.ExecContext(ctx, `DELETE FROM gate_intake_principals WHERE name = $1`, name)
	})

	repo := postgres.NewGateIntakeRepo(db)
	dir := t.TempDir()
	op := uuid.NewString()
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		cli := &gateIntakeCLI{repo: repo, stdout: &out}
		rerr := cli.run(ctx, args)
		return out.String(), rerr
	}
	issueArgs := func(ws, file string) []string {
		return []string{"issue", "--operator", op, "--name", name, "--workspace", ws, "--project", projID.String(),
			"--assignee-agent", agentID.String(), "--label", "kind:ops", "--label", "spark-gate",
			"--schema", domain.GateIntakeSchemaContentHub, "--token-file", file}
	}
	readToken := func(path string) string {
		t.Helper()
		b, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		st, serr := os.Stat(path)
		require.NoError(t, serr)
		assert.Equal(t, os.FileMode(0o600), st.Mode().Perm(), "token file mode")
		tok := string(b)
		require.True(t, domain.GateIntakeTokenWellFormed(tok), "token file holds exactly one token")
		return tok
	}
	authOK := func(tok string) bool {
		p, aerr := repo.PrincipalByTokenHash(ctx, domain.HashGateIntakeToken(tok))
		require.NoError(t, aerr)
		return p != nil
	}

	// A bad binding is refused before anything is written; the token file is gone.
	bad := filepath.Join(dir, "bad.mgi")
	_, err = run(issueArgs(otherWS.String(), bad)...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not in workspace")
	assert.NoFileExists(t, bad)

	// A project with statuses but no default one is refused at issue: every
	// request would be a 500 otherwise (the start status is the default one).
	setDefault := func(on bool) {
		t.Helper()
		_, derr := db.ExecContext(ctx, `UPDATE task_statuses SET is_default = $2 WHERE project_id = $1`, projID, on)
		require.NoError(t, derr)
	}
	noDefault := filepath.Join(dir, "nodefault.mgi")
	setDefault(false)
	_, err = run(issueArgs(wsID.String(), noDefault)...)
	setDefault(true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no default task status")
	assert.NoFileExists(t, noDefault)
	var principals int
	require.NoError(t, db.GetContext(ctx, &principals, `SELECT count(*) FROM gate_intake_principals WHERE name = $1`, name))
	assert.Zero(t, principals, "nothing is written for a refused binding")

	// issue
	f1 := filepath.Join(dir, "t1.mgi")
	out, err := run(issueArgs(wsID.String(), f1)...)
	require.NoError(t, err)
	tok1 := readToken(f1)
	assert.NotContains(t, out, tok1, "the token is never printed")
	assert.NotContains(t, out, domain.HashGateIntakeToken(tok1), "nor its hash")
	var issued struct {
		Result    string                     `json:"result"`
		Principal domain.GateIntakePrincipal `json:"principal"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &issued))
	assert.Equal(t, "issue", issued.Result)
	assert.Equal(t, []string{"kind:ops", "spark-gate"}, issued.Principal.Labels)
	assert.Equal(t, domain.PriorityHigh, issued.Principal.Priority)
	assert.True(t, authOK(tok1))
	id := issued.Principal.ID

	// A live name is refused; an existing file is refused before the database.
	f2 := filepath.Join(dir, "t2.mgi")
	_, err = run(issueArgs(wsID.String(), f2)...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is live")
	assert.NoFileExists(t, f2)
	_, err = run("rotate", "--operator", op, "--name", name, "--token-file", f1)
	require.Error(t, err)
	assert.Equal(t, tok1, readToken(f1), "an existing token file is never overwritten")
	assert.True(t, authOK(tok1))

	// rotate with a grace: both work; without: only the new one.
	f3 := filepath.Join(dir, "t3.mgi")
	_, err = run("rotate", "--operator", op, "--name", name, "--token-file", f3, "--grace", "1h")
	require.NoError(t, err)
	tok3 := readToken(f3)
	assert.True(t, authOK(tok1), "previous token inside its grace")
	assert.True(t, authOK(tok3))
	f4 := filepath.Join(dir, "t4.mgi")
	_, err = run("rotate", "--operator", op, "--name", name, "--token-file", f4, "--grace", "0s")
	require.NoError(t, err)
	tok4 := readToken(f4)
	assert.False(t, authOK(tok1))
	assert.False(t, authOK(tok3))
	assert.True(t, authOK(tok4))

	// revoke: everything at once
	_, err = run("revoke", "--operator", op, "--name", name)
	require.Error(t, err, "--reason is required")
	_, err = run("revoke", "--operator", op, "--name", name, "--reason", "test revoke")
	require.NoError(t, err)
	assert.False(t, authOK(tok4))

	// a re-issue re-arms the same principal; another binding is refused
	f5 := filepath.Join(dir, "t5.mgi")
	other := issueArgs(wsID.String(), f5)
	other[len(other)-5] = "kind:change"
	_, err = run(other...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keeps that binding")
	assert.NoFileExists(t, f5)
	setDefault(false)
	_, err = run(issueArgs(wsID.String(), f5)...)
	setDefault(true)
	require.Error(t, err, "a re-arm checks the binding again")
	assert.Contains(t, err.Error(), "has no default task status")
	assert.NoFileExists(t, f5)
	out, err = run(issueArgs(wsID.String(), f5)...)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &issued))
	assert.Equal(t, "rearm", issued.Result)
	assert.Equal(t, id, issued.Principal.ID, "the namespace id never changes")
	assert.True(t, authOK(readToken(f5)))

	// show: no secret, the audit trail of every action
	out, err = run("show", "--name", name)
	require.NoError(t, err)
	for _, tok := range []string{tok1, tok3, tok4} {
		assert.NotContains(t, out, tok)
		assert.NotContains(t, out, domain.HashGateIntakeToken(tok))
	}
	var shown struct {
		Principal domain.GateIntakePrincipal      `json:"principal"`
		Audit     []postgres.GateIntakeAuditEntry `json:"audit"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &shown))
	var actions []string
	for _, a := range shown.Audit {
		actions = append(actions, a.Action)
		assert.Equal(t, op, a.OperatorID.String())
	}
	assert.Equal(t, []string{"issue", "rotate", "rotate", "revoke", "rearm"}, actions)
	assert.Nil(t, shown.Principal.RevokedAt)
	assert.WithinDuration(t, time.Now(), shown.Principal.CreatedAt, time.Hour)

	// argument errors
	_, err = run("issue", "--name", name)
	require.Error(t, err)
	_, err = run("frobnicate", "--name", name)
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("unknown command %q", "frobnicate"))
}
