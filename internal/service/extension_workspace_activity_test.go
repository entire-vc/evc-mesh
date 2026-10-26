package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/extensions/gitlab"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
)

type fakeActivity map[string]int

type failingActivity struct{}

func (failingActivity) WorkspaceProviderActivity(context.Context, uuid.UUID, string) (int, *time.Time, error) {
	return 0, nil, errors.New("db down")
}

func (failingActivity) WorkspaceProviderRepos(context.Context, uuid.UUID, string, int) ([]repository.VCSRepoActivity, error) {
	return nil, errors.New("db down")
}

func (f fakeActivity) WorkspaceProviderRepos(_ context.Context, _ uuid.UUID, provider string, limit int) ([]repository.VCSRepoActivity, error) {
	if f[provider] == 0 {
		return nil, nil
	}
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	rows := []repository.VCSRepoActivity{
		{Repo: "entire-vc/evc-mesh", Host: "gitlab.example.invalid", Links: f[provider] - 1, LastEventAt: at},
		{Repo: "entire-vc/evc-site", Host: "gitlab.example.invalid", Links: 1, LastEventAt: at},
	}
	if limit < len(rows) {
		rows = rows[:limit]
	}
	return rows, nil
}

func (f fakeActivity) WorkspaceProviderActivity(_ context.Context, _ uuid.UUID, provider string) (int, *time.Time, error) {
	n := f[provider]
	if n == 0 {
		return 0, nil, nil
	}
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	return n, &at, nil
}

func gitlabSvc(t *testing.T, a service.VCSActivitySource) *service.ExtensionWorkspaceService {
	t.Helper()
	integ := exttest.NewMemIntegrations()
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(gitlab.New(extensions.Deps{Conns: extensions.IntegrationConnections{Repo: integ}})))
	consents := &exttest.MemConsents{Integ: integ}
	return service.NewExtensionWorkspaceService(reg, integ, consents).WithVCSActivity(a)
}

// A workspace whose GitLab links arrive through the instance-level webhook has
// no integration_config row; the list must still say links exist.
func TestExtensionWorkspace_GitLabActivityWithoutConnection(t *testing.T) {
	ctx := context.Background()
	list, err := gitlabSvc(t, fakeActivity{"gitlab": 1748}).List(ctx, uuid.New())
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Nil(t, list[0].Connection)
	require.False(t, list[0].Status.Enabled)
	require.NotNil(t, list[0].Activity)
	require.Equal(t, 1748, list[0].Activity.Links)
	require.NotNil(t, list[0].Activity.LastEventAt)
	// an internal ticket: the repositories behind the count ride along.
	require.Len(t, list[0].Activity.Repos, 2)
	require.Equal(t, service.ExtensionRepoView{Repo: "entire-vc/evc-mesh", Host: "gitlab.example.invalid", Links: 1747, LastEventAt: *list[0].Activity.LastEventAt}, list[0].Activity.Repos[0])

	empty, err := gitlabSvc(t, fakeActivity{"github": 220}).List(ctx, uuid.New())
	require.NoError(t, err)
	require.Nil(t, empty[0].Activity, "no gitlab links, no activity")
}

func TestExtensionWorkspace_ActivityErrorFailsTheList(t *testing.T) {
	_, err := gitlabSvc(t, failingActivity{}).List(context.Background(), uuid.New())
	require.ErrorContains(t, err, "db down")
}
