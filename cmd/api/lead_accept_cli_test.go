package main

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// TestLeadAcceptWiring pins what the route audits cannot see: the guard is
// Echo-level, each route requires its own principal kind, the module is
// registered before the gate-intake one (so its guard answers mgi_ on its
// routes), and main() runs the operator subcommand before any module.
func TestLeadAcceptWiring(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "routes_lead_accept.go", nil, 0)
	require.NoError(t, err)
	var eUse bool
	routes := map[string]string{}
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
			eUse = strings.Contains(exprString(call.Args[0]), "LeadAcceptGuard")
		case recv.Name == "v1" && (sel.Sel.Name == "GET" || sel.Sel.Name == "POST"):
			lit, _ := call.Args[0].(*ast.BasicLit)
			require.NotNil(t, lit)
			var guards []string
			for _, a := range call.Args[2:] {
				guards = append(guards, exprString(a))
			}
			routes[sel.Sel.Name+" "+strings.Trim(lit.Value, `"`)] = strings.Join(guards, " ")
		}
		return true
	})
	assert.True(t, eUse, "LeadAcceptGuard must be an Echo-level middleware (e.Use)")
	assert.Equal(t, map[string]string{
		"GET /lead-accept-context":                "mw.RequireLeadAcceptPrincipal(domain.LeadAcceptKindReader)",
		"POST /lead-accept-registry/attestations": "mw.RequireLeadAcceptPrincipal(domain.LeadAcceptKindPublisher)",
	}, routes)

	mods, err := os.ReadFile("modules.go")
	require.NoError(t, err)
	la, gi := strings.Index(string(mods), "registerLeadAcceptRoutes,"), strings.Index(string(mods), "registerGateIntakeRoutes,")
	require.True(t, la > 0 && gi > 0)
	assert.Less(t, la, gi, "the lead-accept guard must run before the gate-intake guard")
	mainSrc, err := os.ReadFile("main.go")
	require.NoError(t, err)
	s := string(mainSrc)
	sub, mods1 := strings.Index(s, `os.Args[1] == "lead-accept"`), strings.Index(s, "range apiModules")
	require.True(t, sub > 0 && mods1 > 0)
	assert.Less(t, sub, mods1, "the subcommand must run before any module (no second server)")
}

// The fixture request of Spark!1235 (contract LA5); its request_sha256 is the
// one ch-dev-2 computed on the client side.
const leadAcceptFixtureRequest = `{"project":"entire-vc/evc-spark","mr_iid":1235,` +
	`"head_sha":"350b837f334daa6b2bbd3f3f5b520e142449a2bc","lead_gitlab_id":6,"review_note_id":50651,` +
	`"lead_accept_note_id":50661,"approval_id":"9835e466-1560-46b7-9fbe-0f80631e0b81","approval_version":5,` +
	`"payload_sha256":"64788c002a6d68c2e50ee600b6c781f3a771ff678ff30d0104360841dc4b0813",` +
	`"evidence":["comment:074fb350-74f6-4136-a9d1-9ec10506ecdc/bc203833-f3d0-484b-9f54-f232270bfa6b",` +
	`"artifact:4a3ace64-dc01-4537-8416-b3b23068b800/76b67513-81f2-47c8-944f-931ad4002fde"]}`

func TestLeadAcceptCLI_DB(t *testing.T) {
	db := gateIntakeCLIDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	// Workspace, evidence project and a project outside it, with the evidence
	// tasks of the fixture (ids are the fixture's, so its hash is the golden one).
	ws := &domain.Workspace{ID: uuid.New(), Name: "la-cli", Slug: "la-cli-" + suffix, OwnerID: uuid.New()}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	mkProject := func(slug string) (uuid.UUID, uuid.UUID) {
		p := &domain.Project{ID: uuid.New(), WorkspaceID: ws.ID, Name: slug, Slug: slug + "-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone}
		require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, p))
		st := &domain.TaskStatus{ID: uuid.New(), ProjectID: p.ID, Name: "Todo", Slug: "todo", Color: "#00FF00",
			Category: domain.StatusCategoryTodo, IsDefault: true}
		require.NoError(t, postgres.NewTaskStatusRepo(db).Create(ctx, st))
		return p.ID, st.ID
	}
	chProj, chStatus := mkProject("la-cli-ch")
	otherProj, otherStatus := mkProject("la-cli-other")
	mkTask := func(id, proj, status uuid.UUID) {
		tk := &domain.Task{ID: id, ProjectID: proj, StatusID: status, Title: "la-cli", AssigneeType: domain.AssigneeTypeUnassigned,
			Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser}
		require.NoError(t, postgres.NewTaskRepo(db).Create(ctx, tk, nil))
	}
	// The fixture's evidence task ids are fixed (its hash is the golden one),
	// so this test needs a database where they are free.
	commentTask := uuid.MustParse("074fb350-74f6-4136-a9d1-9ec10506ecdc")
	artifactTask := uuid.MustParse("4a3ace64-dc01-4537-8416-b3b23068b800")
	for _, id := range []uuid.UUID{commentTask, artifactTask} {
		var n int
		require.NoError(t, db.GetContext(ctx, &n, `SELECT count(*) FROM tasks WHERE id = $1`, id))
		if n > 0 {
			t.Skipf("task %s exists in this database (a real one?); the fixture test needs a throwaway database", id)
		}
		mkTask(id, chProj, chStatus)
	}
	foreignTask := uuid.New()
	mkTask(foreignTask, otherProj, otherStatus)

	repo := postgres.NewLeadAcceptRepo(db)
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	op := uuid.NewString()
	var names []string
	t.Cleanup(func() {
		for _, n := range names {
			_, _ = db.ExecContext(ctx, `DELETE FROM lead_accept_audit WHERE principal_id IN (SELECT id FROM lead_accept_principals WHERE name = $1)`, n)
			_, _ = db.ExecContext(ctx, `DELETE FROM lead_accept_principals WHERE name = $1`, n)
		}
		for _, p := range []uuid.UUID{chProj, otherProj} {
			_, _ = db.ExecContext(ctx, `DELETE FROM tasks WHERE project_id = $1`, p)
			_, _ = db.ExecContext(ctx, `DELETE FROM task_statuses WHERE project_id = $1`, p)
			_, _ = db.ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, p)
		}
		_, _ = db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = $1`, ws.ID)
	})
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		cli := &leadAcceptCLI{repo: repo, stdout: &out, now: func() time.Time { return now },
			workspaceID: ws.ID, evidenceProjectID: chProj}
		rerr := cli.run(ctx, args)
		return out.String(), rerr
	}
	writeReq := func(name, body string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		return p
	}
	count := func(name string) int {
		var n int
		require.NoError(t, db.GetContext(ctx, &n, `SELECT count(*) FROM lead_accept_principals WHERE name = $1`, name))
		return n
	}
	readToken := func(path, kind string) string {
		t.Helper()
		b, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		st, serr := os.Stat(path)
		require.NoError(t, serr)
		assert.Equal(t, os.FileMode(0o600), st.Mode().Perm(), "token file mode")
		tok := string(b)
		require.True(t, domain.LeadAcceptTokenWellFormed(tok), "token file holds exactly one token")
		require.Equal(t, kind, domain.LeadAcceptTokenKind(tok))
		return tok
	}
	fixture := leadAcceptFixtureRequest

	// LA9 row 12: a reader without, with an invalid or with an out-of-project
	// --request-file is refused and nothing is created; the token file is gone.
	reader := "la-cli-r-" + suffix
	names = append(names, reader)
	refused := map[string][]string{
		"no request file":  {},
		"invalid json":     {"--request-file", writeReq("bad.json", `{"project":`)},
		"unknown field":    {"--request-file", writeReq("extra.json", strings.Replace(fixture, `{`, `{"x":1,`, 1))},
		"short head":       {"--request-file", writeReq("short.json", strings.Replace(fixture, "350b837f334daa6b2bbd3f3f5b520e142449a2bc", "350b837f", 1))},
		"other project":    {"--request-file", writeReq("proj.json", strings.Replace(fixture, "entire-vc/evc-spark", "entire-vc/evc-mesh", 1))},
		"evidence outside": {"--request-file", writeReq("out.json", strings.Replace(fixture, artifactTask.String(), foreignTask.String(), 1))},
		"evidence missing": {"--request-file", writeReq("gone.json", strings.Replace(fixture, artifactTask.String(), uuid.NewString(), 1))},
		"missing file":     {"--request-file", filepath.Join(dir, "nope.json")},
	}
	for name, extra := range refused {
		t.Run("refused: "+name, func(t *testing.T) {
			tf := filepath.Join(dir, "r-"+strings.ReplaceAll(name, " ", "-")+".mlr")
			args := append([]string{"issue", "--kind", "reader", "--operator", op, "--name", reader, "--token-file", tf}, extra...)
			out, rerr := run(args...)
			require.Error(t, rerr)
			assert.Empty(t, out)
			assert.NoFileExists(t, tf)
			assert.Zero(t, count(reader), "no principal is created")
		})
	}
	pubName := "la-cli-p-" + suffix
	names = append(names, pubName)
	_, err := run("issue", "--kind", "publisher", "--operator", op, "--name", pubName, "--token-file", filepath.Join(dir, "p0.mlp"),
		"--request-file", writeReq("p.json", fixture))
	require.Error(t, err, "a publisher takes no request")
	assert.Zero(t, count(pubName))

	// Issue the reader on the fixture: binding_sha256 is the golden request_sha256.
	rf := writeReq("fixture.json", fixture)
	t1 := filepath.Join(dir, "r1.mlr")
	out, err := run("issue", "--kind", "reader", "--operator", op, "--name", reader, "--request-file", rf, "--token-file", t1)
	require.NoError(t, err)
	tok1 := readToken(t1, domain.LeadAcceptKindReader)
	assert.NotContains(t, out, tok1, "the token is never printed")
	assert.NotContains(t, out, domain.HashGateIntakeToken(tok1), "nor its hash")
	var issued struct {
		Result        string `json:"result"`
		Kind          string `json:"kind"`
		BindingSHA256 string `json:"binding_sha256"`
		TokenFile     string `json:"token_file"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &issued))
	assert.Equal(t, "issue", issued.Result)
	assert.Equal(t, domain.LeadAcceptKindReader, issued.Kind)
	assert.Equal(t, t1, issued.TokenFile)
	req, bad := domain.ParseLeadAcceptRequestJSON([]byte(fixture))
	require.Nil(t, bad)
	assert.Equal(t, req.SHA256(), issued.BindingSHA256)
	assert.Equal(t, "61a6ec8206a3beddf9f8fa2829beb7cbfc06109dabf0307110015481841c5205", issued.BindingSHA256,
		"the client's request_sha256 for Spark!1235")
	p, err := repo.PrincipalByTokenHash(ctx, domain.LeadAcceptKindReader, domain.HashGateIntakeToken(tok1), now)
	require.NoError(t, err)
	require.NotNil(t, p)

	// A live name is refused; the existing token file is never overwritten.
	_, err = run("issue", "--kind", "reader", "--operator", op, "--name", reader, "--request-file", rf, "--token-file", filepath.Join(dir, "r2.mlr"))
	require.Error(t, err)
	_, err = run("rotate", "--operator", op, "--name", reader, "--token-file", t1)
	require.Error(t, err, "an existing token file is refused")
	assert.Equal(t, tok1, readToken(t1, domain.LeadAcceptKindReader))

	// rotate keeps the binding; the old token works for the grace only.
	t2 := filepath.Join(dir, "r2.mlr")
	out, err = run("rotate", "--operator", op, "--name", reader, "--token-file", t2, "--grace", "10m")
	require.NoError(t, err)
	tok2 := readToken(t2, domain.LeadAcceptKindReader)
	assert.NotContains(t, out, tok2)
	var rotated struct {
		BindingSHA256 string `json:"binding_sha256"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &rotated))
	assert.Equal(t, issued.BindingSHA256, rotated.BindingSHA256)
	for tok, at := range map[string]time.Time{tok1: now.Add(9 * time.Minute), tok2: now.Add(time.Hour)} {
		p, perr := repo.PrincipalByTokenHash(ctx, domain.LeadAcceptKindReader, domain.HashGateIntakeToken(tok), at)
		require.NoError(t, perr)
		assert.NotNil(t, p)
	}
	gone, err := repo.PrincipalByTokenHash(ctx, domain.LeadAcceptKindReader, domain.HashGateIntakeToken(tok1), now.Add(11*time.Minute))
	require.NoError(t, err)
	assert.Nil(t, gone, "the old token ends with its grace")

	// revoke, then show: revoked, binding kept, audit complete, no token.
	_, err = run("revoke", "--operator", op, "--name", reader)
	require.Error(t, err, "--reason is required")
	_, err = run("revoke", "--operator", op, "--name", reader, "--reason", "probe done")
	require.NoError(t, err)
	for _, tok := range []string{tok1, tok2} {
		p, perr := repo.PrincipalByTokenHash(ctx, domain.LeadAcceptKindReader, domain.HashGateIntakeToken(tok), now)
		require.NoError(t, perr)
		assert.Nil(t, p, "a revoked principal has no live token")
	}
	readerPrincipal, err := repo.PrincipalByName(ctx, reader)
	require.NoError(t, err)
	require.NotNil(t, readerPrincipal)
	beforeShow, err := repo.Audit(ctx, readerPrincipal.ID)
	require.NoError(t, err)
	require.Len(t, beforeShow, 3)
	for _, invalidOp := range []string{"", "not-a-uuid", uuid.Nil.String()} {
		out, err = run("show", "--operator", invalidOp, "--name", reader)
		require.Error(t, err, "show requires an identifiable operator")
		assert.Empty(t, out)
	}
	afterInvalid, err := repo.Audit(ctx, readerPrincipal.ID)
	require.NoError(t, err)
	assert.Equal(t, beforeShow, afterInvalid)
	out, err = run("show", "--operator", op, "--name", reader)
	require.NoError(t, err)
	assert.NotContains(t, out, tok2)
	assert.NotContains(t, out, domain.HashGateIntakeToken(tok2))
	var shown struct {
		Principal domain.LeadAcceptPrincipal      `json:"principal"`
		Audit     []postgres.GateIntakeAuditEntry `json:"audit"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &shown))
	require.NotNil(t, shown.Principal.RevokedAt)
	assert.Equal(t, issued.BindingSHA256, *shown.Principal.BindingSHA256)
	var actions []string
	for _, a := range shown.Audit {
		actions = append(actions, a.Action)
	}
	assert.ElementsMatch(t, []string{"issue", "rotate", "revoke", "show"}, actions)
	last := shown.Audit[len(shown.Audit)-1]
	assert.Equal(t, "show", last.Action)
	assert.Equal(t, uuid.MustParse(op), last.OperatorID)
	assert.True(t, now.Equal(last.CreatedAt))
	storedAudit, err := repo.Audit(ctx, shown.Principal.ID)
	require.NoError(t, err)
	storedJSON, err := json.Marshal(storedAudit)
	require.NoError(t, err)
	shownJSON, err := json.Marshal(shown.Audit)
	require.NoError(t, err)
	assert.JSONEq(t, string(storedJSON), string(shownJSON), "show includes its newly committed audit entry")

	// A publisher: issue, show with its latest observation (none yet).
	tp := filepath.Join(dir, "p1.mlp")
	_, err = run("issue", "--kind", "publisher", "--operator", op, "--name", pubName, "--token-file", tp)
	require.NoError(t, err)
	readToken(tp, domain.LeadAcceptKindPublisher)
	out, err = run("show", "--operator", op, "--name", pubName)
	require.NoError(t, err)
	assert.Contains(t, out, `"latest_observation": null`)
	assert.NotContains(t, out, "binding_sha256")
	var publisherShown struct {
		Principal domain.LeadAcceptPrincipal      `json:"principal"`
		Audit     []postgres.GateIntakeAuditEntry `json:"audit"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &publisherShown))
	publisherAudit, err := repo.Audit(ctx, publisherShown.Principal.ID)
	require.NoError(t, err)
	storedJSON, err = json.Marshal(publisherAudit)
	require.NoError(t, err)
	shownJSON, err = json.Marshal(publisherShown.Audit)
	require.NoError(t, err)
	assert.JSONEq(t, string(storedJSON), string(shownJSON))
	require.Len(t, publisherAudit, 2)
	assert.Equal(t, "show", publisherAudit[1].Action)

	// Flags of another command and unknown commands are refused.
	_, err = run("show", "--name", reader, "--kind", "reader")
	require.Error(t, err)
	_, err = run("delete", "--name", reader)
	require.Error(t, err)
}

// stdout is an external boundary: a closed pipe must not destroy a token
// whose hash and audit have already committed to PostgreSQL.
type leadAcceptClosedOutput struct{}

func (leadAcceptClosedOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestLeadAcceptCLICommittedTokenSurvivesOutputFailure(t *testing.T) {
	db := gateIntakeCLIDB(t)
	ctx := context.Background()
	repo := postgres.NewLeadAcceptRepo(db)
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	op := uuid.NewString()
	for _, action := range []string{"issue", "rotate"} {
		t.Run(action, func(t *testing.T) {
			name := "la-stdout-" + uuid.NewString()[:8]
			t.Cleanup(func() {
				_, err := db.ExecContext(ctx, `DELETE FROM lead_accept_audit WHERE principal_id IN
					(SELECT id FROM lead_accept_principals WHERE name = $1)`, name)
				require.NoError(t, err)
				_, err = db.ExecContext(ctx, `DELETE FROM lead_accept_principals WHERE name = $1`, name)
				require.NoError(t, err)
			})
			dir := t.TempDir()
			cli := &leadAcceptCLI{repo: repo, stdout: io.Discard, now: func() time.Time { return now }}
			if action == "rotate" {
				require.NoError(t, cli.run(ctx, []string{"issue", "--kind", "publisher", "--operator", op,
					"--name", name, "--token-file", filepath.Join(dir, "original.mlp")}))
			}
			path := filepath.Join(dir, "committed.mlp")
			args := []string{action, "--operator", op, "--name", name, "--token-file", path}
			if action == "issue" {
				args = append(args, "--kind", "publisher")
			}
			cli.stdout = leadAcceptClosedOutput{}
			require.ErrorIs(t, cli.run(ctx, args), io.ErrClosedPipe)
			p, err := repo.PrincipalByName(ctx, name)
			require.NoError(t, err)
			require.NotNil(t, p)
			require.Nil(t, p.RevokedAt, "the operation committed despite the stdout error")
			tokenBytes, err := os.ReadFile(path)
			require.NoError(t, err, "the committed token file must survive a stdout error")
			st, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
			live, err := repo.PrincipalByTokenHash(ctx, domain.LeadAcceptKindPublisher,
				domain.HashGateIntakeToken(string(tokenBytes)), now)
			require.NoError(t, err)
			require.NotNil(t, live, "the preserved token must authenticate against the committed row")
			assert.Equal(t, p.ID, live.ID)
			audit, err := repo.Audit(ctx, p.ID)
			require.NoError(t, err)
			require.NotEmpty(t, audit)
			assert.Equal(t, action, audit[len(audit)-1].Action)
		})
	}
}
