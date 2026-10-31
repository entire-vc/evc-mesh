package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func asExtension(c echo.Context, actorID uuid.UUID) {
	c.Set(mw.ContextKeyAuthType, mw.AuthTypeExtension)
	c.Set(mw.ContextKeyExtensionPrincipal, &service.ExtensionPrincipal{ActorID: actorID, ExtensionID: "qa-pilot"})
}

// An extension token has neither user_id nor agent_id; the comment used to reach
// Postgres with an empty author_type (22P02 -> 400 "invalid value for field").
func TestCommentHandler_Create_ExtensionActorIsAuthor(t *testing.T) {
	actorID := uuid.New()
	taskID := uuid.New()
	var got *domain.Comment
	h, e := setupCommentTest(&MockCommentService{
		CreateFunc: func(_ context.Context, cm *domain.Comment) error { got = cm; return nil },
	})

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"body":"marker"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/comments")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	asExtension(c, actorID)

	require.NoError(t, h.Create(c))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NotNil(t, got)
	assert.Equal(t, actorID, got.AuthorID)
	assert.Equal(t, domain.ActorTypeExtension, got.AuthorType)
}

func TestArtifactHandler_UploadJSON_ExtensionActorIsUploader(t *testing.T) {
	actorID := uuid.New()
	taskID := uuid.New()
	var got service.UploadArtifactInput
	h, e := setupArtifactTest(&MockArtifactService{
		UploadFunc: func(_ context.Context, in service.UploadArtifactInput) (*domain.Artifact, error) {
			got = in
			return &domain.Artifact{ID: uuid.New(), TaskID: in.TaskID, Name: in.Name}, nil
		},
	})

	body := `{"name":"unique.txt","artifact_type":"file","mime_type":"text/plain","content":"marker","encoding":"text"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/artifacts")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	asExtension(c, actorID)

	require.NoError(t, h.Upload(c))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, actorID, got.UploadedBy)
	assert.Equal(t, domain.UploaderTypeExtension, got.UploadedByType)
}
