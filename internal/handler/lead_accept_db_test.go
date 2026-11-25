package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/storage"
)

// DB integration of the lead-accept context (docs/lead-accept-context.md,
// contract LA9 rows 1–11 and 13; row 12 is the operator command, in cmd/api):
// the real handlers, guards, service and repository over Postgres, with a
// controlled clock. The only fake is the object store (an external boundary).
// lead_accept_observations is global, so every test starts from an empty
// table; the package's tests do not run in parallel.

// laClock is the controlled clock of the service and the repository.
type laClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *laClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *laClock) Set(t time.Time)     { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *laClock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func (c *laClock) At(d time.Duration) string {
	return c.Now().Add(d).UTC().Format(time.RFC3339Nano)
}

// laCountingStore counts every read of registry or evidence state, so a test
// can show that a refusal happened before any of them.
type laCountingStore struct {
	*postgres.LeadAcceptRepo
	reads     atomic.Int64
	onComment func()        // runs before each comment read (contention tests)
	onCurrent func(n int64) // runs inside the n-th registry read, before it answers
	currents  atomic.Int64
}

func (s *laCountingStore) CurrentObservation(ctx context.Context) (*postgres.LeadAcceptObservation, error) {
	s.reads.Add(1)
	n := s.currents.Add(1)
	obs, err := s.LeadAcceptRepo.CurrentObservation(ctx)
	if s.onCurrent != nil {
		s.onCurrent(n)
	}
	return obs, err
}
func (s *laCountingStore) TaskScope(ctx context.Context, id uuid.UUID) (*postgres.LeadAcceptTaskScope, error) {
	s.reads.Add(1)
	return s.LeadAcceptRepo.TaskScope(ctx, id)
}
func (s *laCountingStore) Comment(ctx context.Context, id uuid.UUID) (*postgres.LeadAcceptComment, error) {
	s.reads.Add(1)
	if s.onComment != nil {
		s.onComment()
	}
	return s.LeadAcceptRepo.Comment(ctx, id)
}
func (s *laCountingStore) Artifact(ctx context.Context, id uuid.UUID) (*postgres.LeadAcceptArtifact, error) {
	s.reads.Add(1)
	return s.LeadAcceptRepo.Artifact(ctx, id)
}
func (s *laCountingStore) GateIntakeReceipts(ctx context.Context, id uuid.UUID) ([]postgres.LeadAcceptReceipt, error) {
	s.reads.Add(1)
	return s.LeadAcceptRepo.GateIntakeReceipts(ctx, id)
}

// laObjects is the object store: bytes by key, every read counted.
type laObjects struct {
	mu   sync.Mutex
	m    map[string][]byte
	gets atomic.Int64
}

func (o *laObjects) GetObject(_ context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	o.gets.Add(1)
	o.mu.Lock()
	defer o.mu.Unlock()
	b, ok := o.m[key]
	if !ok {
		return nil, storage.ObjectInfo{}, errors.New("NoSuchKey")
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), b...))), storage.ObjectInfo{Size: int64(len(b))}, nil
}

func (o *laObjects) put(key string, b []byte) { o.mu.Lock(); o.m[key] = b; o.mu.Unlock() }
func (o *laObjects) del(key string)           { o.mu.Lock(); delete(o.m, key); o.mu.Unlock() }

type leadAcceptEnv struct {
	db        *sqlx.DB
	server    *httptest.Server
	client    *http.Client
	repo      *postgres.LeadAcceptRepo
	store     *laCountingStore
	objects   *laObjects
	clock     *laClock
	suffix    string
	operator  uuid.UUID
	wsID      uuid.UUID
	chProj    uuid.UUID // the evidence (ContentHub) project
	otherProj uuid.UUID // same workspace, not the evidence project
	statusID  uuid.UUID
	otherStat uuid.UUID
	taskA     uuid.UUID // evidence task
	taskB     uuid.UUID // second evidence task
	authorID  uuid.UUID // agent that wrote the evidence
	agentKey  string
	jwt       string
	mgiToken  string
	publisher string
	stubHits  atomic.Int64
	names     []string
}

func newLeadAcceptEnv(t *testing.T) *leadAcceptEnv {
	t.Helper()
	db := oauthE2ETestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	_, err := db.ExecContext(ctx, `DELETE FROM lead_accept_observations`)
	require.NoError(t, err)

	userRepo := postgres.NewUserRepo(db)
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	workspaceMemberRepo := postgres.NewWorkspaceMemberRepo(db)
	agentRepo := postgres.NewAgentRepo(db)
	projectRepo := postgres.NewProjectRepo(db)
	statusRepo := postgres.NewTaskStatusRepo(db)
	agentSvc := service.NewAgentService(agentRepo, postgres.NewActivityLogRepo(db), workspaceRepo, userRepo)
	authSvc := auth.NewService(userRepo, postgres.NewRefreshTokenRepo(db), workspaceRepo, workspaceMemberRepo, "lead-accept-db-test-jwt-secret")

	user, tokens, err := authSvc.Register(ctx, fmt.Sprintf("lead-accept-%s@e2e.example.com", suffix), "Correct-Horse-Battery-Staple-1", "lead-accept-"+suffix)
	require.NoError(t, err)
	wss, err := workspaceRepo.ListForUser(ctx, user.ID)
	require.NoError(t, err)
	require.NotEmpty(t, wss)
	wsID := wss[0].ID

	mkProject := func(slug string) (uuid.UUID, uuid.UUID) {
		p := &domain.Project{ID: uuid.New(), WorkspaceID: wsID, Name: slug, Slug: slug + "-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone}
		require.NoError(t, projectRepo.Create(ctx, p))
		st := &domain.TaskStatus{ID: uuid.New(), ProjectID: p.ID, Name: "Todo", Slug: "todo", Color: "#00FF00",
			Category: domain.StatusCategoryTodo, IsDefault: true}
		require.NoError(t, statusRepo.Create(ctx, st))
		return p.ID, st.ID
	}
	chProj, statusID := mkProject("la-ch")
	otherProj, otherStat := mkProject("la-other")
	author, err := agentSvc.Register(ctx, service.RegisterAgentInput{WorkspaceID: wsID, Name: "la-author-" + suffix, AgentType: domain.AgentTypeCustom})
	require.NoError(t, err)

	clock := &laClock{t: time.Now().UTC().Truncate(time.Microsecond)}
	repo := postgres.NewLeadAcceptRepo(db)
	store := &laCountingStore{LeadAcceptRepo: repo}
	objects := &laObjects{m: map[string][]byte{}}
	env := &leadAcceptEnv{db: db, repo: repo, store: store, objects: objects, clock: clock, suffix: suffix,
		operator: uuid.New(), wsID: wsID, chProj: chProj, otherProj: otherProj, statusID: statusID, otherStat: otherStat,
		authorID: author.Agent.ID, agentKey: author.APIKey, jwt: tokens.AccessToken}
	env.taskA = env.task(t, chProj, statusID)
	env.taskB = env.task(t, chProj, statusID)

	// A live gate-intake principal of the same workspace: its mgi_ token must
	// not reach either lead-accept route.
	giRepo := postgres.NewGateIntakeRepo(db)
	mgi, err := domain.NewGateIntakeToken()
	require.NoError(t, err)
	giName := "la-gi-" + suffix
	_, _, err = giRepo.Issue(ctx, env.operator, postgres.GateIntakeBinding{Name: giName, WorkspaceID: wsID, ProjectID: chProj,
		AssigneeAgentID: author.Agent.ID, Labels: []string{"kind:ops"}, Priority: domain.PriorityHigh,
		Schema: domain.GateIntakeSchemaContentHub}, domain.HashGateIntakeToken(mgi), domain.GateIntakeTokenDisplay(mgi))
	require.NoError(t, err)
	env.mgiToken = mgi
	env.publisher = env.issue(t, domain.LeadAcceptKindPublisher, "la-pub-"+suffix, nil)

	svc := service.NewLeadAcceptService(store, objects, clock.Now)
	giSvc := service.NewGateIntakeService(giRepo, nil, statusRepo)
	stub := func(c echo.Context) error {
		env.stubHits.Add(1)
		return c.JSON(http.StatusOK, map[string]string{"reached": c.Path()})
	}
	// The production order (cmd/api/modules.go): lead-accept guard, then the
	// gate-intake guard, then the routes on v1 and the authenticated group.
	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	e.Use(mw.LeadAcceptGuard(svc))
	e.Use(mw.GateIntakeGuard(giSvc))
	v1 := e.Group("/api/v1")
	h := NewLeadAcceptHandler(svc)
	v1.GET("/lead-accept-context", h.Context, mw.RequireLeadAcceptPrincipal(domain.LeadAcceptKindReader))
	v1.POST("/lead-accept-registry/attestations", h.Attest, mw.RequireLeadAcceptPrincipal(domain.LeadAcceptKindPublisher))
	v1.POST("/gate-intake", stub, mw.RequireGateIntakePrincipal())
	api := v1.Group("")
	api.Use(mw.DualAuth(authSvc, agentSvc, nil))
	api.GET("/tasks/:task_id", stub)
	api.POST("/tasks/:task_id/comments", stub)
	env.server = httptest.NewServer(e)
	env.client = &http.Client{Timeout: 30 * time.Second}

	t.Cleanup(func() {
		env.server.Close()
		_, _ = db.ExecContext(ctx, `DELETE FROM lead_accept_observations`)
		_, _ = db.ExecContext(ctx, `DELETE FROM lead_accept_audit WHERE principal_id IN (SELECT id FROM lead_accept_principals WHERE name = ANY($1))`, pq.Array(env.names))
		_, _ = db.ExecContext(ctx, `DELETE FROM lead_accept_principals WHERE name = ANY($1)`, pq.Array(env.names))
		_, _ = db.ExecContext(ctx, `DELETE FROM gate_intake_audit WHERE principal_id IN (SELECT id FROM gate_intake_principals WHERE name = $1)`, giName)
		_, _ = db.ExecContext(ctx, `DELETE FROM gate_intake_principals WHERE name = $1`, giName)
		for _, pid := range []uuid.UUID{chProj, otherProj} {
			_, _ = db.ExecContext(ctx, `DELETE FROM comments WHERE task_id IN (SELECT id FROM tasks WHERE project_id = $1)`, pid)
			_, _ = db.ExecContext(ctx, `DELETE FROM artifacts WHERE task_id IN (SELECT id FROM tasks WHERE project_id = $1)`, pid)
			_, _ = db.ExecContext(ctx, `DELETE FROM tasks WHERE project_id = $1`, pid)
			_, _ = db.ExecContext(ctx, `DELETE FROM task_statuses WHERE project_id = $1`, pid)
			_, _ = db.ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, pid)
		}
		_, _ = db.ExecContext(ctx, `DELETE FROM agents WHERE id = $1`, author.Agent.ID)
	})
	return env
}

func (env *leadAcceptEnv) task(t *testing.T, proj, status uuid.UUID) uuid.UUID {
	t.Helper()
	tk := &domain.Task{ID: uuid.New(), ProjectID: proj, StatusID: status, Title: "la evidence",
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CreatedBy: env.authorID, CreatedByType: domain.ActorTypeAgent}
	require.NoError(t, postgres.NewTaskRepo(env.db).Create(context.Background(), tk, nil))
	return tk.ID
}

func (env *leadAcceptEnv) comment(t *testing.T, taskID uuid.UUID, body string) uuid.UUID {
	t.Helper()
	at := time.Now().UTC().Truncate(time.Microsecond)
	c := &domain.Comment{ID: uuid.New(), TaskID: taskID, AuthorID: env.authorID, AuthorType: domain.ActorTypeAgent,
		Body: body, CreatedAt: at, UpdatedAt: at}
	require.NoError(t, postgres.NewCommentRepo(env.db).Create(context.Background(), c))
	return c.ID
}

func (env *leadAcceptEnv) artifact(t *testing.T, taskID uuid.UUID, content []byte, size int64) (artifactID uuid.UUID, ref string) {
	t.Helper()
	key := "la-test/" + uuid.NewString()
	sum := sha256.Sum256(content)
	a := &domain.Artifact{ID: uuid.New(), TaskID: taskID, Name: "evidence.json", ArtifactType: domain.ArtifactTypeData,
		MimeType: "application/json", StorageKey: key, StorageURL: "s3://test/" + key, SizeBytes: size,
		ChecksumSHA256: hex.EncodeToString(sum[:]), UploadedBy: env.authorID, UploadedByType: domain.UploaderTypeAgent,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	require.NoError(t, postgres.NewArtifactRepo(env.db).Create(context.Background(), a))
	env.objects.put(key, content)
	return a.ID, key
}

// laReq is a request tuple whose evidence exists: one comment and one
// artifact on taskA, both mentioning the approval, the payload and the head.
type laReq struct {
	req        *domain.LeadAcceptRequest
	commentID  uuid.UUID
	artifactID uuid.UUID
	key        string
	body       string
	content    []byte
}

func (env *leadAcceptEnv) newReq(t *testing.T) *laReq {
	t.Helper()
	approval := uuid.NewString()
	payload := hex.EncodeToString(func() []byte { s := sha256.Sum256([]byte(approval)); return s[:] }())
	head := strings.Repeat("3", 40)
	body := "Approved " + approval + " payload " + payload + " head " + head
	content := []byte(`{"approval_id":"` + approval + `","payload_sha256":"` + payload + `"}`)
	cid := env.comment(t, env.taskA, body)
	aid, key := env.artifact(t, env.taskA, content, int64(len(content)))
	q := url.Values{
		"project": {domain.LeadAcceptProject}, "mr_iid": {"1235"}, "head_sha": {head}, "lead_gitlab_id": {"6"},
		"review_note_id": {"50651"}, "lead_accept_note_id": {"50661"}, "approval_id": {approval}, "approval_version": {"5"},
		"payload_sha256": {payload},
		"evidence":       {"comment:" + env.taskA.String() + "/" + cid.String(), "artifact:" + env.taskA.String() + "/" + aid.String()},
	}
	r, bad := domain.ParseLeadAcceptQuery(q.Encode())
	require.Nil(t, bad)
	return &laReq{req: r, commentID: cid, artifactID: aid, key: key, body: body, content: content}
}

// query is the request as the GET's query string.
func laQuery(r *domain.LeadAcceptRequest) url.Values {
	q := url.Values{
		"project": {r.Project}, "mr_iid": {strconv.FormatInt(r.MRIID, 10)}, "head_sha": {r.HeadSHA},
		"lead_gitlab_id": {strconv.FormatInt(r.LeadGitlabID, 10)}, "review_note_id": {strconv.FormatInt(r.ReviewNoteID, 10)},
		"lead_accept_note_id": {strconv.FormatInt(r.LeadAcceptNoteID, 10)}, "approval_id": {r.ApprovalID},
		"approval_version": {strconv.FormatInt(r.ApprovalVersion, 10)}, "payload_sha256": {r.PayloadSHA256},
	}
	q["evidence"] = append([]string(nil), r.Evidence...)
	return q
}

// issue issues a principal through the repository (the operator command's
// path) and returns its token. req is the reader's bound request.
func (env *leadAcceptEnv) issue(t *testing.T, kind, name string, req *domain.LeadAcceptRequest) string {
	t.Helper()
	tok, err := domain.NewLeadAcceptToken(kind)
	require.NoError(t, err)
	in := postgres.LeadAcceptIssue{Name: name, Kind: kind}
	if kind == domain.LeadAcceptKindReader {
		in.Request, in.WorkspaceID, in.EvidenceProjectID = req, env.wsID, env.chProj
	}
	env.names = append(env.names, name)
	_, _, err = env.repo.Issue(context.Background(), env.operator, in, domain.HashGateIntakeToken(tok), domain.GateIntakeTokenDisplay(tok), env.clock.Now())
	require.NoError(t, err)
	return tok
}

func (env *leadAcceptEnv) reader(t *testing.T, r *laReq) string {
	t.Helper()
	return env.issue(t, domain.LeadAcceptKindReader, "la-r-"+uuid.NewString()[:12], r.req)
}

type laResult struct {
	status int
	header http.Header
	raw    []byte
	ctx    domain.LeadAcceptContext
	errB   apiErrBody
}

type apiErrBody struct {
	Code      int               `json:"code"`
	ErrorCode string            `json:"error_code"`
	Message   string            `json:"message"`
	Context   map[string]any    `json:"context"`
	Fields    map[string]string `json:"validation"`
}

func (env *leadAcceptEnv) do(t *testing.T, method, path string, hdr map[string]string, body []byte) *laResult {
	t.Helper()
	req, err := http.NewRequest(method, env.server.URL+path, bytes.NewReader(body))
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := env.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := &laResult{status: resp.StatusCode, header: resp.Header, raw: raw}
	if resp.StatusCode == http.StatusOK {
		require.NoError(t, json.Unmarshal(raw, &out.ctx), string(raw))
	} else {
		_ = json.Unmarshal(raw, &out.errB)
	}
	return out
}

func (env *leadAcceptEnv) get(t *testing.T, tok string, q url.Values) *laResult {
	t.Helper()
	h := map[string]string{}
	if tok != "" {
		h = bearer(tok)
	}
	return env.do(t, http.MethodGet, "/api/v1/lead-accept-context?"+q.Encode(), h, nil)
}

// okAttestation is an ok=true observation at observedAt whose evidence-spark
// leads are the given gitlab ids.
func laAttestation(observedAt string, leads ...int) map[string]any {
	ls := []any{}
	for _, id := range leads {
		ls = append(ls, map[string]any{"username": fmt.Sprintf("lead%d", id), "gitlab_id": id, "lane": fmt.Sprintf("Lane%d", id)})
	}
	return map[string]any{
		"schema":                         domain.LeadAcceptAttestationSchema,
		"observation_id":                 uuid.NewString(),
		"observed_at":                    observedAt,
		"ok":                             true,
		"git_commit":                     "7eea41f1ee64bd0146f4706b8ce90488c60bb318",
		"origin_main_commit":             "7eea41f1ee64bd0146f4706b8ce90488c60bb318",
		"registry_last_commit":           "6928e675bb1632fd11a4f248d14227055cd5a459",
		"registry_blob_sha1":             "9af06fab161ffa7e1f88400775e50de363717b9b",
		"origin_registry_blob_sha1":      "9af06fab161ffa7e1f88400775e50de363717b9b",
		"registry_sha256":                "0924c523f43aab5f11d1680df15644668becb7015b5c37bb813079bc31ea921f",
		"mirror_policy_blob_sha1":        "de103142a1357d2891435e5a682c7487242bd76c",
		"origin_mirror_policy_blob_sha1": "de103142a1357d2891435e5a682c7487242bd76c",
		"mirror_policy_sha256":           "1428816e70a798596f3baad25821d089a7b897441939d71bef51de88eae46626",
		"gate_code_sha256":               "144dd786e602aa449a29ac09b92e7fe75b0764efbc6b6e1c370a7a917e77cadc",
		"worktree_clean":                 true,
		"in_sync":                        true,
		"projects": map[string]any{domain.LeadAcceptProject: map[string]any{
			"lead_accept_allowed": true, "policy_reason": "", "leads": ls}},
	}
}

func laFailure(observedAt, step string) map[string]any {
	return map[string]any{"schema": domain.LeadAcceptAttestationSchema, "observation_id": uuid.NewString(),
		"observed_at": observedAt, "ok": false, "error_step": step, "error": "git fetch: exit status 128"}
}

func (env *leadAcceptEnv) attest(t *testing.T, body map[string]any) *laResult {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	return env.do(t, http.MethodPost, "/api/v1/lead-accept-registry/attestations", bearer(env.publisher), b)
}

// accept posts an observation that must be accepted (201).
func (env *leadAcceptEnv) accept(t *testing.T, body map[string]any) {
	t.Helper()
	r := env.attest(t, body)
	require.Equal(t, http.StatusCreated, r.status, string(r.raw))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
}

func (env *leadAcceptEnv) current(t *testing.T) *postgres.LeadAcceptObservation {
	t.Helper()
	o, err := env.repo.CurrentObservation(context.Background())
	require.NoError(t, err)
	return o
}

func (env *leadAcceptEnv) observations(t *testing.T) int {
	t.Helper()
	n, err := env.repo.ObservationCount(context.Background())
	require.NoError(t, err)
	return n
}

func requireRefusal(t *testing.T, r *laResult, status int, code string) {
	t.Helper()
	require.Equal(t, status, r.status, string(r.raw))
	assert.Equal(t, code, r.errB.ErrorCode, string(r.raw))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"), "every answer is no-store")
}

// --- positive ---------------------------------------------------------------

func TestLeadAcceptDB_Positive_FullContext(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	tok := env.reader(t, r)
	env.accept(t, laAttestation(env.clock.At(-2*time.Second), 6, 7))

	got := env.get(t, tok, laQuery(r.req))
	require.Equal(t, http.StatusOK, got.status, string(got.raw))
	assert.Equal(t, "no-store", got.header.Get("Cache-Control"))
	c := got.ctx
	assert.Equal(t, domain.LeadAcceptContextSchema, c.Schema)
	assert.Equal(t, r.req.SHA256(), c.RequestSHA256)
	assert.Equal(t, domain.LeadAcceptTime(env.clock.Now()), c.CheckedAt)
	assert.Equal(t, domain.LeadAcceptLeadOut{Username: "lead6", GitlabID: 6, Lane: "Lane6", Status: "active"}, c.Lead)
	assert.True(t, c.Scope.LeadAcceptAllowed)
	assert.True(t, c.Registry.InSync)
	assert.Equal(t, 150, c.Registry.MaxAgeSeconds)
	assert.Equal(t, "none", c.ApprovalBinding.GateIntakeReceipt)
	require.Len(t, c.Evidence, 2)
	art, com := c.Evidence[0], c.Evidence[1] // refs sorted: artifact before comment
	sumA := sha256.Sum256(r.content)
	sumC := sha256.Sum256([]byte(r.body))
	assert.Equal(t, "artifact", art.Kind)
	assert.Equal(t, hex.EncodeToString(sumA[:]), art.SHA256)
	assert.Equal(t, domain.LeadAcceptMentions{ApprovalID: true, PayloadSHA256: true, HeadSHA: false}, art.Mentions)
	assert.Equal(t, env.chProj.String(), art.ProjectID)
	assert.Equal(t, "la-author-"+env.suffix, art.Author.Name)
	assert.Equal(t, "comment", com.Kind)
	assert.Equal(t, hex.EncodeToString(sumC[:]), com.SHA256)
	assert.Equal(t, domain.LeadAcceptMentions{ApprovalID: true, PayloadSHA256: true, HeadSHA: true}, com.Mentions)
	assert.Equal(t, env.authorID.String(), com.Author.ID)
	assert.Equal(t, "agent", com.Author.Type)
	assert.Contains(t, string(got.raw), `"system_code":null`, "a comment carries system_code, null included")

	// context_sha256 recomputes from the body as received.
	sum, err := c.ComputeContextSHA256()
	require.NoError(t, err)
	assert.Equal(t, sum, c.ContextSHA256)

	// The artifact's sha256 is computed from its bytes on every GET.
	env.objects.put(r.key, []byte(`{"other":"bytes"}`))
	again := env.get(t, tok, laQuery(r.req))
	require.Equal(t, http.StatusOK, again.status, string(again.raw))
	sumNew := sha256.Sum256([]byte(`{"other":"bytes"}`))
	assert.Equal(t, hex.EncodeToString(sumNew[:]), again.ctx.Evidence[0].SHA256)
	assert.NotEqual(t, c.ContextSHA256, again.ctx.ContextSHA256)
}

// --- LA9 rows 1–9: the registry observation --------------------------------

// Row 1: a delayed ok=true is refused and the current observation stays.
func TestLeadAcceptDB_LA9_1_Delayed(t *testing.T) {
	env := newLeadAcceptEnv(t)
	o0 := laAttestation(env.clock.At(-35*time.Second), 6)
	env.clock.Add(-34 * time.Second)
	env.accept(t, o0)
	env.clock.Add(34 * time.Second)

	r := env.attest(t, laAttestation(env.clock.At(-31*time.Second), 6))
	requireRefusal(t, r, http.StatusConflict, domain.LeadAcceptCodeDelayed)
	assert.Equal(t, o0["observation_id"], env.current(t).ObservationID.String(), "the current observation did not change")
	assert.Equal(t, 1, env.observations(t))

	// Positive control: 29 s late is accepted.
	o2 := laAttestation(env.clock.At(-29*time.Second), 6)
	env.accept(t, o2)
	assert.Equal(t, o2["observation_id"], env.current(t).ObservationID.String())
}

// Row 2: the same body again is a replay; fresh_until does not move.
func TestLeadAcceptDB_LA9_2_Replay(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	tok := env.reader(t, r)
	o := laAttestation(env.clock.At(0), 6)
	env.accept(t, o)
	before := env.get(t, tok, laQuery(r.req))
	require.Equal(t, http.StatusOK, before.status, string(before.raw))

	env.clock.Add(5 * time.Second)
	rep := env.attest(t, o)
	requireRefusal(t, rep, http.StatusConflict, domain.LeadAcceptCodeReplayed)
	after := env.get(t, tok, laQuery(r.req))
	require.Equal(t, http.StatusOK, after.status, string(after.raw))
	assert.Equal(t, before.ctx.Registry.FreshUntil, after.ctx.Registry.FreshUntil, "fresh_until did not move")
	assert.Equal(t, before.ctx.Registry.ReceivedAt, after.ctx.Registry.ReceivedAt)
	assert.Equal(t, 1, env.observations(t))
}

// Row 3: an older observation after a newer one is refused; GET shows O2.
func TestLeadAcceptDB_LA9_3_NotNewer(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	tok := env.reader(t, r)
	o2 := laAttestation(env.clock.At(0), 6)
	env.accept(t, o2)
	o1 := laAttestation(env.clock.At(-5*time.Second), 6)
	requireRefusal(t, env.attest(t, o1), http.StatusConflict, domain.LeadAcceptCodeNotNewer)
	same := laAttestation(env.clock.At(0), 6) // equal observed_at is not newer either
	requireRefusal(t, env.attest(t, same), http.StatusConflict, domain.LeadAcceptCodeNotNewer)
	got := env.get(t, tok, laQuery(r.req))
	require.Equal(t, http.StatusOK, got.status, string(got.raw))
	assert.Equal(t, o2["observation_id"], got.ctx.Registry.ObservationID)
}

// Row 4: observed_at more than 5 s ahead of the server clock.
func TestLeadAcceptDB_LA9_4_ClockSkew(t *testing.T) {
	env := newLeadAcceptEnv(t)
	requireRefusal(t, env.attest(t, laAttestation(env.clock.At(6*time.Second), 6)), http.StatusConflict, domain.LeadAcceptCodeClockSkew)
	assert.Equal(t, 0, env.observations(t))
	env.accept(t, laAttestation(env.clock.At(4*time.Second), 6)) // positive control
}

// Row 5: 149 s after observed_at → 200, 151 s → 503 registry_stale.
func TestLeadAcceptDB_LA9_5_Expiry(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	tok := env.reader(t, r)
	t0 := env.clock.Now()
	env.accept(t, laAttestation(env.clock.At(0), 6))
	env.clock.Set(t0.Add(149 * time.Second))
	got := env.get(t, tok, laQuery(r.req))
	require.Equal(t, http.StatusOK, got.status, string(got.raw))
	assert.Equal(t, domain.LeadAcceptTime(t0.Add(150*time.Second)), got.ctx.Registry.FreshUntil)
	env.clock.Set(t0.Add(150 * time.Second))
	require.Equal(t, http.StatusOK, env.get(t, tok, laQuery(r.req)).status, "150 s is still fresh")
	env.clock.Set(t0.Add(151 * time.Second))
	stale := env.get(t, tok, laQuery(r.req))
	requireRefusal(t, stale, http.StatusServiceUnavailable, domain.LeadAcceptCodeRegistryStale)
	assert.Equal(t, domain.LeadAcceptTime(t0.Add(150*time.Second)), stale.errB.Context["fresh_until"])
}

// Row 6: ok=false after a good observation → 503 at once; the next ok=true → 200.
func TestLeadAcceptDB_LA9_6_SourceError(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	tok := env.reader(t, r)
	env.accept(t, laAttestation(env.clock.At(0), 6))
	require.Equal(t, http.StatusOK, env.get(t, tok, laQuery(r.req)).status)
	env.clock.Add(time.Second)
	env.accept(t, laFailure(env.clock.At(0), "fetch"))
	bad := env.get(t, tok, laQuery(r.req))
	requireRefusal(t, bad, http.StatusServiceUnavailable, domain.LeadAcceptCodeRegistrySourceError)
	assert.Equal(t, "fetch", bad.errB.Context["error_step"])
	env.clock.Add(time.Second)
	env.accept(t, laAttestation(env.clock.At(0), 6))
	require.Equal(t, http.StatusOK, env.get(t, tok, laQuery(r.req)).status)
}

// Row 7: a delayed ok=false, newer than the current one, is accepted.
func TestLeadAcceptDB_LA9_7_DelayedFailure(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	tok := env.reader(t, r)
	t0 := env.clock.Now()
	env.clock.Set(t0.Add(-49 * time.Second))
	env.accept(t, laAttestation(t0.Add(-50*time.Second).Format(time.RFC3339Nano), 6))
	env.clock.Set(t0)
	env.accept(t, laFailure(env.clock.At(-40*time.Second), "policy"))
	requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusServiceUnavailable, domain.LeadAcceptCodeRegistrySourceError)
}

// Row 8: a new observation without the lead → 409 at once, no waiting for the TTL.
func TestLeadAcceptDB_LA9_8_LeadRevoked(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	tok := env.reader(t, r)
	env.accept(t, laAttestation(env.clock.At(0), 6, 7))
	require.Equal(t, http.StatusOK, env.get(t, tok, laQuery(r.req)).status)
	env.clock.Add(time.Second)
	env.accept(t, laAttestation(env.clock.At(0), 7))
	requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusConflict, domain.LeadAcceptCodeLeadNotAuthorized)
}

// Row 8, under contention: the lead is revoked while the evidence is read. The
// answer must not be the 200 of the observation the read started from.
func TestLeadAcceptDB_LA9_8_LeadRevokedDuringRead(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	tok := env.reader(t, r)
	env.accept(t, laAttestation(env.clock.At(0), 6))
	var once sync.Once
	env.store.onComment = func() {
		once.Do(func() {
			env.clock.Add(time.Second)
			env.accept(t, laAttestation(env.clock.At(0), 7))
		})
	}
	requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusConflict, domain.LeadAcceptCodeLeadNotAuthorized)

	// The TTL running out during the read is the same: 503 registry_stale.
	env.store.onComment = nil
	env.clock.Add(30 * time.Second)
	env.accept(t, laAttestation(env.clock.At(-20*time.Second), 6))
	var once2 sync.Once
	env.store.onComment = func() { once2.Do(func() { env.clock.Add(135 * time.Second) }) }
	requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusServiceUnavailable, domain.LeadAcceptCodeRegistryStale)
}

// Row 8/LA6 staleness at the last moment: the 150 s run out while the final
// registry read (the recheck before the answer) is in flight. Freshness must be
// judged at the time that read returned, not before it started; exactly 150 s
// is still fresh.
func TestLeadAcceptDB_LA9_StaleDuringFinalRecheck(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	tok := env.reader(t, r)
	env.accept(t, laAttestation(env.clock.At(-20*time.Second), 6))

	// The recheck is the second registry read of the GET.
	at := func(age time.Duration) {
		start := env.store.currents.Load()
		env.store.onCurrent = func(n int64) {
			if n == start+2 {
				env.clock.Add(age - 20*time.Second)
			}
		}
	}
	at(150 * time.Second)
	rec := env.get(t, tok, laQuery(r.req))
	require.Equal(t, http.StatusOK, rec.status, string(rec.raw))

	env.clock.Add(-130 * time.Second) // back to 20 s old
	at(150*time.Second + time.Millisecond)
	requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusServiceUnavailable, domain.LeadAcceptCodeRegistryStale)
	env.store.onCurrent = nil
}

// Row 9: in_sync=false → 503 registry_out_of_sync (each of its three inputs).
func TestLeadAcceptDB_LA9_9_OutOfSync(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	tok := env.reader(t, r)
	for _, mut := range []func(map[string]any){
		func(a map[string]any) { a["worktree_clean"], a["in_sync"] = false, false },
		func(a map[string]any) { a["origin_registry_blob_sha1"], a["in_sync"] = strings.Repeat("1", 40), false },
		func(a map[string]any) {
			a["origin_mirror_policy_blob_sha1"], a["in_sync"] = strings.Repeat("2", 40), false
		},
	} {
		env.clock.Add(time.Second)
		a := laAttestation(env.clock.At(0), 6)
		mut(a)
		env.accept(t, a)
		requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusServiceUnavailable, domain.LeadAcceptCodeRegistryOutOfSync)
	}
	env.clock.Add(time.Second)
	env.accept(t, laAttestation(env.clock.At(0), 6))
	require.Equal(t, http.StatusOK, env.get(t, tok, laQuery(r.req)).status)
}

// --- LA9 row 10: binding mismatch --------------------------------------------

func TestLeadAcceptDB_LA9_10_BindingMismatch(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	other := env.newReq(t) // a second, real evidence set
	tok := env.reader(t, r)
	env.accept(t, laAttestation(env.clock.At(0), 6))
	require.Equal(t, http.StatusOK, env.get(t, tok, laQuery(r.req)).status, "positive control")

	variants := map[string]func(url.Values){
		"approval_id":         func(q url.Values) { q.Set("approval_id", uuid.NewString()) },
		"approval_version":    func(q url.Values) { q.Set("approval_version", "6") },
		"head_sha":            func(q url.Values) { q.Set("head_sha", strings.Repeat("4", 40)) },
		"lead_accept_note_id": func(q url.Values) { q.Set("lead_accept_note_id", "50662") },
		"lead_gitlab_id":      func(q url.Values) { q.Set("lead_gitlab_id", "7") },
		"mr_iid":              func(q url.Values) { q.Set("mr_iid", "1236") },
		"payload_sha256":      func(q url.Values) { q.Set("payload_sha256", strings.Repeat("5", 64)) },
		"project":             func(q url.Values) { q.Set("project", "entire-vc/evc-mesh") },
		"review_note_id":      func(q url.Values) { q.Set("review_note_id", "50650") },
		"evidence (other set)": func(q url.Values) {
			q["evidence"] = append([]string(nil), other.req.Evidence...)
		},
		"evidence (subset)": func(q url.Values) { q["evidence"] = q["evidence"][:1] },
	}
	for name, mut := range variants {
		t.Run(name, func(t *testing.T) {
			q := laQuery(r.req)
			mut(q)
			reads, gets := env.store.reads.Load(), env.objects.gets.Load()
			got := env.get(t, tok, q)
			requireRefusal(t, got, http.StatusForbidden, domain.LeadAcceptCodeScopeForbidden)
			assert.NotContains(t, got.errB.Message, name, "the refusal does not name the differing field")
			assert.Nil(t, got.errB.Context)
			assert.Equal(t, reads, env.store.reads.Load(), "neither registry nor evidence is read")
			assert.Equal(t, gets, env.objects.gets.Load(), "no artifact is read")
		})
	}
}

// --- LA9 row 11: rotate -------------------------------------------------------

func TestLeadAcceptDB_LA9_11_Rotate(t *testing.T) {
	env := newLeadAcceptEnv(t)
	ctx := context.Background()
	r := env.newReq(t)
	other := env.newReq(t)
	name := "la-rot-" + env.suffix
	old := env.issue(t, domain.LeadAcceptKindReader, name, r.req)
	env.accept(t, laAttestation(env.clock.At(0), 6))
	before, err := env.repo.PrincipalByName(ctx, name)
	require.NoError(t, err)
	require.Equal(t, r.req.SHA256(), *before.BindingSHA256)

	nt, err := domain.NewLeadAcceptToken(domain.LeadAcceptKindReader)
	require.NoError(t, err)
	rot, err := env.repo.Rotate(ctx, env.operator, name, domain.HashGateIntakeToken(nt), domain.GateIntakeTokenDisplay(nt), time.Minute, env.clock.Now())
	require.NoError(t, err)
	assert.Equal(t, *before.BindingSHA256, *rot.BindingSHA256, "binding_sha256 does not change")
	assert.Equal(t, string(before.Binding), string(rot.Binding))

	require.Equal(t, http.StatusOK, env.get(t, nt, laQuery(r.req)).status, "new token, same tuple")
	requireRefusal(t, env.get(t, nt, laQuery(other.req)), http.StatusForbidden, domain.LeadAcceptCodeScopeForbidden)
	require.Equal(t, http.StatusOK, env.get(t, old, laQuery(r.req)).status, "old token inside the grace")
	env.clock.Add(61 * time.Second)
	env.accept(t, laAttestation(env.clock.At(0), 6))
	requireRefusal(t, env.get(t, old, laQuery(r.req)), http.StatusUnauthorized, "unauthorized")
	require.Equal(t, http.StatusOK, env.get(t, nt, laQuery(r.req)).status)

	// A rotate token of the other kind is refused; the principal is unchanged.
	pt, err := domain.NewLeadAcceptToken(domain.LeadAcceptKindPublisher)
	require.NoError(t, err)
	_, err = env.repo.Rotate(ctx, env.operator, name, domain.HashGateIntakeToken(pt), domain.GateIntakeTokenDisplay(pt), time.Minute, env.clock.Now())
	require.Error(t, err)
}

// --- LA9 row 13: the refusals of LA6 ------------------------------------------

func TestLeadAcceptDB_LA9_13_Auth(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	tok := env.reader(t, r)
	env.accept(t, laAttestation(env.clock.At(0), 6))
	q := laQuery(r.req)
	path := "/api/v1/lead-accept-context?" + q.Encode()
	attest, _ := json.Marshal(laAttestation(env.clock.At(0), 6))

	wrong, _ := domain.NewLeadAcceptToken(domain.LeadAcceptKindReader)
	revokedTok := env.issue(t, domain.LeadAcceptKindReader, "la-rev-"+env.suffix, r.req)
	_, err := env.repo.Revoke(context.Background(), env.operator, "la-rev-"+env.suffix, "test", env.clock.Now())
	require.NoError(t, err)

	reads := env.store.reads.Load()
	// 401: no key, a wrong key, a revoked key — one answer for all.
	for name, h := range map[string]map[string]string{
		"none": nil, "wrong mlr_": bearer(wrong), "revoked mlr_": bearer(revokedTok),
		"short mlr_": bearer("mlr_abc"),
	} {
		t.Run("401 GET "+name, func(t *testing.T) {
			requireRefusal(t, env.do(t, http.MethodGet, path, h, nil), http.StatusUnauthorized, "unauthorized")
		})
	}
	requireRefusal(t, env.do(t, http.MethodPost, "/api/v1/lead-accept-registry/attestations", nil, attest), http.StatusUnauthorized, "unauthorized")

	// 403: every other credential on either route, on its presence alone.
	others := map[string]map[string]string{
		"mgi_":          bearer(env.mgiToken),
		"agent key":     {"X-Agent-Key": env.agentKey},
		"user JWT":      bearer(env.jwt),
		"unknown token": bearer("ext_" + strings.Repeat("x", 43)),
	}
	for name, h := range others {
		t.Run("403 GET "+name, func(t *testing.T) {
			requireRefusal(t, env.do(t, http.MethodGet, path, h, nil), http.StatusForbidden, domain.LeadAcceptCodeScopeForbidden)
		})
		t.Run("403 POST "+name, func(t *testing.T) {
			requireRefusal(t, env.do(t, http.MethodPost, "/api/v1/lead-accept-registry/attestations", h, attest),
				http.StatusForbidden, domain.LeadAcceptCodeScopeForbidden)
		})
	}
	// mlp_ on the GET, mlr_ on the POST.
	requireRefusal(t, env.get(t, env.publisher, q), http.StatusForbidden, domain.LeadAcceptCodeScopeForbidden)
	requireRefusal(t, env.do(t, http.MethodPost, "/api/v1/lead-accept-registry/attestations", bearer(tok), attest),
		http.StatusForbidden, domain.LeadAcceptCodeScopeForbidden)
	// A non-reader with a bad query is still the 403 (LA6 order: kind before query).
	requireRefusal(t, env.do(t, http.MethodGet, "/api/v1/lead-accept-context?x=1", bearer(env.publisher), nil),
		http.StatusForbidden, domain.LeadAcceptCodeScopeForbidden)
	assert.Equal(t, reads, env.store.reads.Load(), "no refusal above read registry or evidence")

	// mlr_ and mlp_ on the other routes: 403, never reaching a handler.
	for _, tk := range []string{tok, env.publisher} {
		for _, rt := range []struct{ m, p string }{
			{http.MethodGet, "/api/v1/tasks/" + env.taskA.String()},
			{http.MethodPost, "/api/v1/tasks/" + env.taskA.String() + "/comments"},
			{http.MethodPost, "/api/v1/gate-intake"},
		} {
			got := env.do(t, rt.m, rt.p, bearer(tk), []byte(`{}`))
			assert.Equal(t, http.StatusForbidden, got.status, "%s %s: %s", rt.m, rt.p, got.raw)
			assert.Equal(t, domain.LeadAcceptCodeScopeForbidden, got.errB.ErrorCode)
		}
	}
	// A wrong mlr_ elsewhere is a 401, not a pass to DualAuth.
	got := env.do(t, http.MethodGet, "/api/v1/tasks/"+env.taskA.String(), bearer(wrong), nil)
	assert.Equal(t, http.StatusUnauthorized, got.status)
	assert.Zero(t, env.stubHits.Load(), "no lead-accept token reached another route's handler")
	// Positive control of the stub routes: the agent key reaches them.
	require.Equal(t, http.StatusOK, env.do(t, http.MethodGet, "/api/v1/tasks/"+env.taskA.String(), map[string]string{"X-Agent-Key": env.agentKey}, nil).status)
	// And the reader itself still reads.
	require.Equal(t, http.StatusOK, env.get(t, tok, q).status)
}

func TestLeadAcceptDB_LA9_13_Validation(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	tok := env.reader(t, r)
	env.accept(t, laAttestation(env.clock.At(0), 6))
	for name, mut := range map[string]func(url.Values){
		"missing":          func(q url.Values) { q.Del("mr_iid") },
		"extra":            func(q url.Values) { q.Set("x", "1") },
		"repeated":         func(q url.Values) { q.Add("mr_iid", "1235") },
		"short head":       func(q url.Values) { q.Set("head_sha", "350b837f") },
		"note order":       func(q url.Values) { q.Set("lead_accept_note_id", "50651") },
		"bad evidence ref": func(q url.Values) { q["evidence"] = []string{"task:" + uuid.NewString()} },
	} {
		t.Run(name, func(t *testing.T) {
			q := laQuery(r.req)
			mut(q)
			requireRefusal(t, env.get(t, tok, q), http.StatusBadRequest, "validation_failed")
		})
	}
}

func TestLeadAcceptDB_LA9_13_Registry(t *testing.T) {
	env := newLeadAcceptEnv(t)
	r := env.newReq(t)
	tok := env.reader(t, r)
	requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusServiceUnavailable, domain.LeadAcceptCodeRegistryUnavailable)

	closed := laAttestation(env.clock.At(0), 6)
	closed["projects"] = map[string]any{domain.LeadAcceptProject: map[string]any{
		"lead_accept_allowed": false, "policy_reason": "mirror policy closes lead-accept", "leads": []any{}}}
	env.accept(t, closed)
	na := env.get(t, tok, laQuery(r.req))
	requireRefusal(t, na, http.StatusConflict, domain.LeadAcceptCodeNotApplicable)
	assert.Equal(t, "mirror policy closes lead-accept", na.errB.Context["policy_reason"])

	env.clock.Add(time.Second)
	env.accept(t, laAttestation(env.clock.At(0), 7))
	requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusConflict, domain.LeadAcceptCodeLeadNotAuthorized)
}

func TestLeadAcceptDB_LA9_13_Evidence(t *testing.T) {
	env := newLeadAcceptEnv(t)
	ctx := context.Background()
	env.accept(t, laAttestation(env.clock.At(0), 6))
	ok := func(t *testing.T, tok string, r *laReq) {
		t.Helper()
		got := env.get(t, tok, laQuery(r.req))
		require.Equal(t, http.StatusOK, got.status, string(got.raw))
	}

	t.Run("task moved out of the evidence project", func(t *testing.T) {
		r := env.newReq(t)
		tk := env.task(t, env.chProj, env.statusID)
		cid := env.comment(t, tk, r.body)
		r.req.Evidence = []string{"comment:" + tk.String() + "/" + cid.String()}
		tok := env.reader(t, r)
		ok(t, tok, r)
		_, err := env.db.ExecContext(ctx, `UPDATE tasks SET project_id = $2, status_id = $3 WHERE id = $1`, tk, env.otherProj, env.otherStat)
		require.NoError(t, err)
		got := env.get(t, tok, laQuery(r.req))
		requireRefusal(t, got, http.StatusForbidden, domain.LeadAcceptCodeEvidenceScope)
	})
	t.Run("task deleted reads the same as foreign", func(t *testing.T) {
		r := env.newReq(t)
		tk := env.task(t, env.chProj, env.statusID)
		cid := env.comment(t, tk, r.body)
		r.req.Evidence = []string{"comment:" + tk.String() + "/" + cid.String()}
		tok := env.reader(t, r)
		ok(t, tok, r)
		_, err := env.db.ExecContext(ctx, `UPDATE tasks SET deleted_at = now() WHERE id = $1`, tk)
		require.NoError(t, err)
		requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusForbidden, domain.LeadAcceptCodeEvidenceScope)
	})
	t.Run("comment edited", func(t *testing.T) {
		r := env.newReq(t)
		tok := env.reader(t, r)
		ok(t, tok, r)
		c := &domain.Comment{ID: r.commentID, Body: r.body + " (edited)", UpdatedAt: time.Now().UTC().Add(time.Second)}
		require.NoError(t, postgres.NewCommentRepo(env.db).Update(ctx, c))
		requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusConflict, domain.LeadAcceptCodeEvidenceChanged)
	})
	t.Run("comment deleted", func(t *testing.T) {
		r := env.newReq(t)
		tok := env.reader(t, r)
		ok(t, tok, r)
		require.NoError(t, postgres.NewCommentRepo(env.db).Delete(ctx, r.commentID))
		requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusConflict, domain.LeadAcceptCodeEvidenceRevoked)
	})
	t.Run("comment of another task", func(t *testing.T) {
		r := env.newReq(t)
		r.req.Evidence = []string{"comment:" + env.taskB.String() + "/" + r.commentID.String()} // the comment is on taskA
		tok := env.reader(t, r)
		requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusConflict, domain.LeadAcceptCodeEvidenceRevoked)
	})
	t.Run("artifact deleted", func(t *testing.T) {
		r := env.newReq(t)
		tok := env.reader(t, r)
		ok(t, tok, r)
		require.NoError(t, postgres.NewArtifactRepo(env.db).Delete(ctx, r.artifactID))
		requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusConflict, domain.LeadAcceptCodeEvidenceRevoked)
	})
	t.Run("artifact bytes unreadable", func(t *testing.T) {
		r := env.newReq(t)
		tok := env.reader(t, r)
		ok(t, tok, r)
		env.objects.del(r.key)
		requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusServiceUnavailable, domain.LeadAcceptCodeEvidenceUnavailable)
	})
	t.Run("artifact over 1 MiB by its bytes", func(t *testing.T) {
		r := env.newReq(t)
		tok := env.reader(t, r)
		ok(t, tok, r)
		env.objects.put(r.key, bytes.Repeat([]byte("x"), domain.LeadAcceptMaxArtifactBytes+1))
		requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusConflict, domain.LeadAcceptCodeEvidenceUnsupported)
	})
	t.Run("artifact over 1 MiB by its record", func(t *testing.T) {
		r := env.newReq(t)
		aid, _ := env.artifact(t, env.taskA, []byte("{}"), domain.LeadAcceptMaxArtifactBytes+1)
		r.req.Evidence = []string{"artifact:" + env.taskA.String() + "/" + aid.String()}
		tok := env.reader(t, r)
		gets := env.objects.gets.Load()
		requireRefusal(t, env.get(t, tok, laQuery(r.req)), http.StatusConflict, domain.LeadAcceptCodeEvidenceUnsupported)
		assert.Equal(t, gets, env.objects.gets.Load(), "an oversized record is refused before its bytes are read")
	})
}

// approval_binding: a real gate-intake receipt, written by the gate-intake
// service, is "match"; a receipt with other fields, or one whose card no
// longer proves its fields, is 409 approval_binding_conflict.
func TestLeadAcceptDB_LA9_13_ApprovalBinding(t *testing.T) {
	env := newLeadAcceptEnv(t)
	gi := newGateIntakeEnv(t)
	ctx := context.Background()
	env.accept(t, laAttestation(env.clock.At(0), 6))
	post := func(r *laReq, head string) uuid.UUID {
		t.Helper()
		res := gi.post(t, gi.token, gateBody(uuid.MustParse(r.req.ApprovalID), int(r.req.ApprovalVersion), map[string]any{
			"mr_iid": r.req.MRIID, "head_sha": head, "payload_sha256": r.req.PayloadSHA256,
			"mr_url": fmt.Sprintf("https://gitlab.example.com/entire-vc/evc-spark/-/merge_requests/%d", r.req.MRIID)}))
		require.Contains(t, []int{http.StatusCreated, http.StatusOK}, res.status, string(res.raw))
		return res.resp.TaskID
	}

	t.Run("match", func(t *testing.T) {
		r := env.newReq(t)
		tok := env.reader(t, r)
		post(r, r.req.HeadSHA)
		got := env.get(t, tok, laQuery(r.req))
		require.Equal(t, http.StatusOK, got.status, string(got.raw))
		assert.Equal(t, "match", got.ctx.ApprovalBinding.GateIntakeReceipt)
		assert.Equal(t, r.req.PayloadSHA256, got.ctx.ApprovalBinding.PayloadSHA256)
	})
	t.Run("fields differ", func(t *testing.T) {
		r := env.newReq(t)
		tok := env.reader(t, r)
		post(r, strings.Repeat("9", 40))
		got := env.get(t, tok, laQuery(r.req))
		requireRefusal(t, got, http.StatusConflict, domain.LeadAcceptCodeBindingConflict)
		assert.Equal(t, "fields_differ", got.errB.Context["reason"])
		assert.Equal(t, r.req.SHA256(), got.errB.Context["request_sha256"])
		assert.Len(t, got.errB.Context["receipt_request_sha256"], 64)
	})
	t.Run("card edited: receipt unverifiable", func(t *testing.T) {
		r := env.newReq(t)
		tok := env.reader(t, r)
		taskID := post(r, r.req.HeadSHA)
		require.Equal(t, http.StatusOK, env.get(t, tok, laQuery(r.req)).status)
		_, err := env.db.ExecContext(ctx, `UPDATE tasks SET description = replace(description, '- mr_iid: 1235', '- mr_iid: 1236') WHERE id = $1`, taskID)
		require.NoError(t, err)
		got := env.get(t, tok, laQuery(r.req))
		requireRefusal(t, got, http.StatusConflict, domain.LeadAcceptCodeBindingConflict)
		assert.Equal(t, "receipt_unverifiable", got.errB.Context["reason"])
	})
	t.Run("card soft deleted: receipt unverifiable", func(t *testing.T) {
		r := env.newReq(t)
		tok := env.reader(t, r)
		taskID := post(r, r.req.HeadSHA)
		before := env.get(t, tok, laQuery(r.req))
		require.Equal(t, http.StatusOK, before.status, string(before.raw))
		require.Equal(t, "match", before.ctx.ApprovalBinding.GateIntakeReceipt)
		require.NoError(t, postgres.NewTaskRepo(env.db).Delete(ctx, taskID))
		var deleted bool
		require.NoError(t, env.db.GetContext(ctx, &deleted, `SELECT deleted_at IS NOT NULL FROM tasks WHERE id = $1`, taskID))
		require.True(t, deleted)
		got := env.get(t, tok, laQuery(r.req))
		requireRefusal(t, got, http.StatusConflict, domain.LeadAcceptCodeBindingConflict)
		assert.Equal(t, "receipt_unverifiable", got.errB.Context["reason"])
	})
	t.Run("card gone: receipt unverifiable", func(t *testing.T) {
		r := env.newReq(t)
		tok := env.reader(t, r)
		taskID := post(r, r.req.HeadSHA)
		_, err := env.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = $1`, taskID)
		require.NoError(t, err)
		got := env.get(t, tok, laQuery(r.req))
		requireRefusal(t, got, http.StatusConflict, domain.LeadAcceptCodeBindingConflict)
		assert.Equal(t, "receipt_unverifiable", got.errB.Context["reason"])
	})
}

// --- the attestation route ----------------------------------------------------

func TestLeadAcceptDB_Attestation_ReceiptAndRetention(t *testing.T) {
	env := newLeadAcceptEnv(t)
	o := laAttestation(env.clock.At(-time.Second), 6)
	res := env.attest(t, o)
	require.Equal(t, http.StatusCreated, res.status, string(res.raw))
	var receipt service.LeadAcceptAttestationReceipt
	require.NoError(t, json.Unmarshal(res.raw, &receipt))
	assert.Equal(t, o["observation_id"], receipt.ObservationID)
	assert.True(t, receipt.InSync)
	assert.True(t, receipt.OK)

	// Each observation is its own row; one 8 days old is still kept.
	first := env.current(t).ObservationID
	env.clock.Add(8 * 24 * time.Hour)
	env.accept(t, laAttestation(env.clock.At(0), 6))
	assert.Equal(t, 2, env.observations(t))
	var kept bool
	require.NoError(t, env.db.Get(&kept, `SELECT EXISTS (SELECT 1 FROM lead_accept_observations WHERE observation_id = $1)`, first))
	assert.True(t, kept, "an observation is kept at least 7 days")

	// Body checks: content type, size, shape.
	b, _ := json.Marshal(laAttestation(env.clock.At(0), 6))
	req := env.do(t, http.MethodPost, "/api/v1/lead-accept-registry/attestations",
		map[string]string{"Authorization": "Bearer " + env.publisher, "Content-Type": "text/plain"}, b)
	requireRefusal(t, req, http.StatusBadRequest, "validation_failed")
	big := laAttestation(env.clock.At(0), 6)
	big["error"] = strings.Repeat("x", domain.LeadAcceptMaxBody)
	bb, _ := json.Marshal(big)
	requireRefusal(t, env.do(t, http.MethodPost, "/api/v1/lead-accept-registry/attestations", bearer(env.publisher), bb),
		http.StatusBadRequest, "validation_failed")
	bad := laAttestation(env.clock.At(0), 6)
	bad["unknown"] = 1
	requireRefusal(t, env.attest(t, bad), http.StatusBadRequest, "validation_failed")
	assert.Equal(t, 2, env.observations(t))
}

// Concurrent posts of one body: exactly one is accepted (the advisory lock).
func TestLeadAcceptDB_Attestation_ConcurrentReplay(t *testing.T) {
	env := newLeadAcceptEnv(t)
	o := laAttestation(env.clock.At(0), 6)
	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = env.attest(t, o).status
		}(i)
	}
	wg.Wait()
	created := 0
	for _, c := range codes {
		if c == http.StatusCreated {
			created++
		} else {
			assert.Equal(t, http.StatusConflict, c)
		}
	}
	assert.Equal(t, 1, created)
	assert.Equal(t, 1, env.observations(t))
}
