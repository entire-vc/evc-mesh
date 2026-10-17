package repoinstall

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func errStatus(err error) int {
	var ae *apierror.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return 0
}

func TestListRefs(t *testing.T) {
	svc, _, gl, url := setup(t, "tok")
	gl.tags, gl.branches = []string{"v1"}, []string{"main"}
	gl.set("v1", "aaa", manifestJSON(t, nil))
	gl.set("main", "bbb", manifestJSON(t, nil))

	refs, err := svc.ListRefs(context.Background(), url, "tok")
	require.NoError(t, err)
	assert.Equal(t, "aaa", refs.Tags[0].SHA)
	assert.Equal(t, "main", refs.Branches[0].Name)

	_, err = svc.ListRefs(context.Background(), "ftp://x/a/b", "")
	assert.Equal(t, http.StatusBadRequest, errStatus(err))
	_, err = svc.ListRefs(context.Background(), url, "")
	assert.Equal(t, http.StatusNotFound, errStatus(err), "a private repo without a token looks absent")
}

func TestWrapHostStatuses(t *testing.T) {
	assert.NoError(t, wrapHost(nil))
	assert.Equal(t, http.StatusBadRequest, errStatus(wrapHost(ErrBadURL)))
	assert.Equal(t, http.StatusNotFound, errStatus(wrapHost(ErrNotFound)))
	assert.Equal(t, http.StatusForbidden, errStatus(wrapHost(ErrUnauthorized)))
	assert.Equal(t, http.StatusBadGateway, errStatus(wrapHost(errors.New("boom"))))
	assert.Equal(t, CodeRepoUnavailable, errCode(wrapHost(errors.New("boom"))))
}

func TestAutoUpdateLoopStopsWithContext(t *testing.T) {
	svc, repo, _, _ := setup(t, "")
	repo.auto = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.RunAutoUpdate(ctx, 5*time.Millisecond)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond) // several ticks run against an empty store
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunAutoUpdate did not stop after the context ended")
	}
}

func TestPreviewAndInstallErrorPaths(t *testing.T) {
	svc, _, gl, url := setup(t, "")
	gl.branches = []string{"main"}
	gl.set("main", "bbb", "not json")
	ctx := context.Background()

	_, err := svc.Preview(ctx, "ftp://x/a/b", "main", "")
	assert.Equal(t, http.StatusBadRequest, errStatus(err))
	_, err = svc.Preview(ctx, url, "missing", "")
	assert.Equal(t, http.StatusNotFound, errStatus(err))
	_, err = svc.Preview(ctx, url, "main", "")
	assert.Equal(t, CodeManifestInvalid, errCode(err))
	_, err = svc.Install(ctx, [16]byte{1}, url, "main", "")
	assert.Equal(t, CodeManifestInvalid, errCode(err))

	_, err = svc.Update(ctx, nil, "unknown", "")
	assert.Error(t, err)
	_, err = svc.Updates(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "a", firstLine("a\nb"))
	assert.Equal(t, "ab", firstLine("ab"))
}
