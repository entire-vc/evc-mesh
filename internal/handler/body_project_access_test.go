package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// TestBodyNamedTasksAndProjects_RequireProjectAccess: the project guard sees only
// path parameters. Every handler that is handed another task or a project in
// the body applies the same rule to it before any write, and refuses when the
// guard's check is not on the request at all.
func TestBodyNamedTasksAndProjects_RequireProjectAccess(t *testing.T) {
	own, foreign := uuid.New(), uuid.New()
	onlyOwn := mw.ProjectAccessCheck{
		Task:    func(id uuid.UUID) bool { return id == own },
		Project: func(id uuid.UUID) bool { return id == own },
	}

	wrote := false
	tasks := &MockTaskService{
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
			return &domain.Task{ID: id, ProjectID: own}, nil
		},
		CreateFunc: func(context.Context, *domain.Task) error { wrote = true; return nil },
		UpdateFunc: func(context.Context, *domain.Task) error { wrote = true; return nil },
		MoveToProjectFunc: func(context.Context, uuid.UUID, uuid.UUID) (*domain.Task, error) {
			wrote = true
			return &domain.Task{}, nil
		},
	}
	taskHandler := NewTaskHandler(tasks)
	eventHandler := NewEventHandler(nil)
	ruleHandler := NewRuleHandler(nil)
	turnHandler := NewTurnCommandHandler(nil, tasks)

	cases := []struct {
		name   string
		call   func(echo.Context) error
		params map[string]string
		body   string
		want   int
	}{
		{"create task under a foreign parent", taskHandler.Create, map[string]string{"proj_id": own.String()},
			fmt.Sprintf(`{"title":"x","parent_task_id":%q}`, foreign), http.StatusNotFound},
		{"re-parent a task under a foreign task", taskHandler.Update, map[string]string{"task_id": own.String()},
			fmt.Sprintf(`{"parent_task_id":%q}`, foreign), http.StatusNotFound},
		{"move a task into a foreign project", taskHandler.MoveToProject, map[string]string{"task_id": own.String()},
			fmt.Sprintf(`{"project_id":%q}`, foreign), http.StatusForbidden},
		{"publish an event about a foreign task", eventHandler.Create, map[string]string{"proj_id": own.String()},
			fmt.Sprintf(`{"event_type":"custom","subject":"s","task_id":%q}`, foreign), http.StatusNotFound},
		{"dry-run rules on a foreign task", ruleHandler.EvaluateRules, nil,
			fmt.Sprintf(`{"action":"move","workspace_id":%q,"task_id":%q}`, uuid.New(), foreign), http.StatusNotFound},
		{"dry-run rules in a foreign project", ruleHandler.EvaluateRules, nil,
			fmt.Sprintf(`{"action":"move","workspace_id":%q,"project_id":%q}`, uuid.New(), foreign), http.StatusForbidden},
		{"wait on a foreign card", turnHandler.WaitFor, map[string]string{"task_id": own.String()},
			fmt.Sprintf(`{"on":"card","target_task_id":%q}`, foreign), http.StatusNotFound},
	}
	for _, tc := range cases {
		for _, access := range []struct {
			label  string
			checks []mw.ProjectAccessCheck
		}{{"not a member", []mw.ProjectAccessCheck{onlyOwn}}, {"no check on the request", nil}} {
			t.Run(tc.name+" / "+access.label, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
				req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
				rec := httptest.NewRecorder()
				c := echo.New().NewContext(req, rec)
				var names, values []string
				for name, value := range tc.params {
					names, values = append(names, name), append(values, value)
				}
				c.SetParamNames(names...)
				c.SetParamValues(values...)
				for _, check := range access.checks {
					mw.SetProjectAccess(c, check)
				}
				require.NoError(t, tc.call(c))
				assert.Equal(t, tc.want, rec.Code, rec.Body.String())
			})
		}
	}
	assert.False(t, wrote, "a refused request reached the service")
}

// bodyIDFieldTag matches a request field that names a task or a project.
var bodyIDFieldTag = regexp.MustCompile("json:\"([a-z_]*(?:task_id|task_ids|project_id))(?:,[a-z]+)*\"")

// bodyIDFields classifies every such field in the handlers. "checked" means the
// file applies MayAccessTask / MayAccessProject (or, for ui-context, the same
// UserMayAccessProject rule) to it; anything else says why the rule does not
// apply. TestBodyIDFieldsAreClassified fails on a new field until it is placed
// here: the project guard reads only the path, so a body id nobody looked at is
// a way around it.
var bodyIDFields = map[string]string{
	"dependency_handler.go depends_on_task_id":  "checked",
	"event_handler.go task_id":                  "checked",
	"extension_ui_handler.go project_id":        "checked",
	"extension_ui_handler.go task_id":           "checked",
	"extension_authorize_handler.go project_id": "the subject of the answer, not an access grant: resolved inside the extension's workspace (404 otherwise) and the decision applies the project rule to the asked user's current rows",
	"extension_authorize_handler.go task_id":    "the subject of the answer, not an access grant: resolved inside the extension's workspace (404 otherwise) and the decision applies the project rule to the asked user's current rows",
	"initiative_handler.go project_id":          "checked",
	"rule_handler.go task_id":                   "checked",
	"rule_handler.go project_id":                "checked",
	"task_handler.go parent_task_id":            "checked",
	"task_handler.go project_id":                "checked",
	"task_handler.go task_ids":                  "checked (move batch); bulk update is filtered to the path project by the service",
	"turn_command_handler.go target_task_id":    "checked",
	"memory_handler.go project_id":              "checked",
	"memory_handler.go source_task_id":          "checked",
	"workspace_secrets.go project_id":           "checked (create); the response field is read-only",
	"agent_handler.go current_task_id":          "an agent's own status record; reveals nothing about the task",
	"agent_handler.go task_id":                  "an agent's own activity and usage reports; written, never read back through it",
	"chat_session_handler.go project_id":        "internal chat controller endpoint outside the API group, its own auth",
	"project_integration_handler.go project_id": "response field",
	"spawn_secrets.go project_id":               "internal spawner endpoint, its own auth",
}

func TestBodyIDFieldsAreClassified(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, m := range bodyIDFieldTag.FindAllStringSubmatch(string(src), -1) {
			seen++
			key := f + " " + m[1]
			verdict, ok := bodyIDFields[key]
			if !assert.True(t, ok, "%s: field %q names a task or project in a request and is unclassified. "+
				"Apply mw.MayAccessTask / mw.MayAccessProject to it and add it here as checked, or say why the rule does not apply", f, m[1]) {
				continue
			}
			if strings.HasPrefix(verdict, "checked") {
				assert.Regexp(t, `MayAccessTask|MayAccessProject|UserMayAccessProject`, string(src), "%s is marked checked but applies no check", key)
			}
		}
	}
	require.Greater(t, seen, 10, "the scan found almost nothing; is it reading the handler sources?")
}

// TestRememberAndSecretCreate_BodyProjectAccess: a memory write names a project
// and a source task in its body, a secret create names a project. Both go
// through the same rule, and neither reaches its service when the check refuses
// or is not on the request.
func TestRememberAndSecretCreate_BodyProjectAccess(t *testing.T) {
	ws, own, foreign := uuid.New(), uuid.New(), uuid.New()
	onlyOwn := mw.ProjectAccessCheck{
		Task:    func(id uuid.UUID) bool { return id == own },
		Project: func(id uuid.UUID) bool { return id == own },
	}

	wrote := false
	memories := &MockMemoryService{
		RememberFunc: func(context.Context, *domain.Memory) (service.RememberResult, error) {
			wrote = true
			return service.RememberResult{}, nil
		},
	}
	memHandler := NewMemoryHandler(memories, &mockWorkspaceMemberRepo{})
	secretHandler := NewSecretHandler(&mockSecretService{
		create: func(context.Context, domain.CreateSecretInput) (domain.Secret, error) {
			wrote = true
			return domain.Secret{}, nil
		},
	}, nil)

	cases := []struct {
		name   string
		call   func(echo.Context) error
		body   string
		access *mw.ProjectAccessCheck
		want   int
	}{
		{"memory in a foreign project", memHandler.Remember,
			fmt.Sprintf(`{"key":"k","content":"c","project_id":%q}`, foreign), &onlyOwn, http.StatusNotFound},
		{"memory sourced from a foreign task", memHandler.Remember,
			fmt.Sprintf(`{"key":"k","content":"c","source_task_id":%q}`, foreign), &onlyOwn, http.StatusNotFound},
		{"memory naming a project without the guard", memHandler.Remember,
			fmt.Sprintf(`{"key":"k","content":"c","project_id":%q}`, own), nil, http.StatusNotFound},
		{"secret in a foreign project", secretHandler.Create,
			fmt.Sprintf(`{"name":"N","scope":"project","project_id":%q,"value":"v"}`, foreign), &onlyOwn, http.StatusNotFound},
		{"secret naming a project without the guard", secretHandler.Create,
			fmt.Sprintf(`{"name":"N","scope":"project","project_id":%q,"value":"v"}`, own), nil, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			asAgent(c, uuid.New(), ws)
			c.Set("workspace_id", ws)
			if tc.access != nil {
				mw.SetProjectAccess(c, *tc.access)
			}
			require.NoError(t, tc.call(c))
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
	assert.False(t, wrote, "a refused request reached the service")

	// Positive control: the own project passes the rule and reaches the service.
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		fmt.Sprintf(`{"key":"k","content":"c","project_id":%q,"source_task_id":%q}`, own, own)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ws)
	mw.SetProjectAccess(c, onlyOwn)
	require.NoError(t, memHandler.Remember(c))
	assert.True(t, wrote, "the own project must reach the service, got %d %s", rec.Code, rec.Body.String())
}

// TestRemember_UserTokenProjectCheckSeesTheAdmittedWorkspace: a user token
// carries no workspace, so a memory write takes it from the body. The project
// rule judges an id against the workspace on the request context (as the real
// check does), so Remember must put the workspace it just admitted there.
func TestRemember_UserTokenProjectCheckSeesTheAdmittedWorkspace(t *testing.T) {
	ws, user, proj := uuid.New(), uuid.New(), uuid.New()
	wrote := false
	memHandler := NewMemoryHandler(&MockMemoryService{
		RememberFunc: func(context.Context, *domain.Memory) (service.RememberResult, error) {
			wrote = true
			return service.RememberResult{}, nil
		},
	}, &mockWorkspaceMemberRepo{members: map[string]string{fmt.Sprintf("%s/%s", ws, user): "member"}})

	call := func(c echo.Context) {
		mw.SetProjectAccess(c, mw.ProjectAccessCheck{
			Project: func(id uuid.UUID) bool {
				got, err := mw.GetWorkspaceID(c)
				return err == nil && got == ws && id == proj
			},
		})
	}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		fmt.Sprintf(`{"key":"k","content":"c","workspace_id":%q,"project_id":%q}`, ws, proj)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asUser(c, user)
	call(c)
	require.NoError(t, memHandler.Remember(c))
	assert.True(t, wrote, "a member's project-scoped memory write was refused: %d %s", rec.Code, rec.Body.String())
}

// An owner or admin who is not a project member keeps the bypass on a body-named
// project: the role of the admitted workspace is on the context the rule reads.
func TestRemember_AdminWithoutProjectRowKeepsTheBypass(t *testing.T) {
	ws, user, proj := uuid.New(), uuid.New(), uuid.New()
	wrote := false
	memHandler := NewMemoryHandler(&MockMemoryService{
		RememberFunc: func(context.Context, *domain.Memory) (service.RememberResult, error) {
			wrote = true
			return service.RememberResult{}, nil
		},
	}, &mockWorkspaceMemberRepo{members: map[string]string{fmt.Sprintf("%s/%s", ws, user): domain.RoleAdmin}})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		fmt.Sprintf(`{"key":"k","content":"c","workspace_id":%q,"project_id":%q}`, ws, proj)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asUser(c, user)
	mw.SetProjectAccess(c, mw.ProjectAccessCheck{
		Project: func(uuid.UUID) bool {
			role, _ := c.Get(mw.ContextKeyWorkspaceRole).(string)
			return role == domain.RoleOwner || role == domain.RoleAdmin
		},
	})
	require.NoError(t, memHandler.Remember(c))
	assert.True(t, wrote, "an admin without a project_members row lost the bypass: %d %s", rec.Code, rec.Body.String())
}
