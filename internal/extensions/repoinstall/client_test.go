package repoinstall

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseURLRejects(t *testing.T) {
	c := &Client{}
	for _, raw := range []string{
		"", "::bad", "https://", "https://user:pw@gitlab.example/a/b", "https://gitlab.example/a/b?x=1",
		"https://gitlab.example/a/b#f", "http://gitlab.example/a/b", "ftp://gitlab.example/a/b",
		"https://gitlab.example/a//b", "https://gitlab.example/a/../b", "https://gitlab.example/only",
		"https://github.com/owner", "https://github.com/owner/repo/extra",
	} {
		_, err := c.ParseURL(raw)
		assert.ErrorIs(t, err, ErrBadURL, raw)
	}
	src, err := c.ParseURL("https://GitHub.com/owner/repo.git/")
	require.NoError(t, err)
	assert.Equal(t, ProviderGitHub, src.Provider)
	assert.Equal(t, "owner/repo", src.Path)
	src, err = c.ParseURL("https://gitlab.example/g/sub/p")
	require.NoError(t, err)
	assert.Equal(t, ProviderGitLab, src.Provider)
}

func TestNewClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("[]")) }))
	defer srv.Close()
	c := NewClient()
	src := Source{Scheme: "http", Host: strings.TrimPrefix(srv.URL, "http://"), Path: "a/b", Provider: ProviderGitLab}
	_, err := c.Tags(context.Background(), src, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
}

func TestGitHubProvider(t *testing.T) {
	var gotAuth, gotVersion, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotVersion, gotAccept = r.Header.Get("Authorization"), r.Header.Get("X-GitHub-Api-Version"), r.Header.Get("Accept")
		switch {
		case strings.HasSuffix(r.URL.Path, "/tags"):
			_, _ = w.Write([]byte(`[{"name":"v1","commit":{"sha":"abc"}}]`))
		case strings.HasSuffix(r.URL.Path, "/branches"):
			_, _ = w.Write([]byte(`[{"name":"main","commit":{"sha":"def"}}]`))
		case strings.Contains(r.URL.Path, "/commits/"):
			_, _ = w.Write([]byte(" abc123\n"))
		case strings.Contains(r.URL.Path, "/contents/"):
			_, _ = w.Write([]byte(`{"id":"x"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), GitHubAPI: srv.URL + "/"}
	src, err := c.ParseURL("https://github.com/owner/repo")
	require.NoError(t, err)
	ctx := context.Background()

	tags, err := c.Tags(ctx, src, "ghp_tok")
	require.NoError(t, err)
	assert.Equal(t, []Ref{{Name: "v1", SHA: "abc"}}, tags)
	assert.Equal(t, "Bearer ghp_tok", gotAuth)
	assert.Equal(t, "2022-11-28", gotVersion)

	branches, err := c.Branches(ctx, src, "")
	require.NoError(t, err)
	assert.Equal(t, "def", branches[0].SHA)
	assert.Empty(t, gotAuth, "no token, no Authorization header")

	sha, err := c.Commit(ctx, src, "v1", "")
	require.NoError(t, err)
	assert.Equal(t, "abc123", sha)
	assert.Equal(t, "application/vnd.github.sha", gotAccept)

	man, err := c.Manifest(ctx, src, "v1", "")
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"x"}`, string(man))
}

func TestGetStatusMapping(t *testing.T) {
	var status int
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), AllowInsecure: true}
	src, err := c.ParseURL(srv.URL + "/a/b")
	require.NoError(t, err)
	ctx := context.Background()

	status, body = http.StatusUnauthorized, ""
	_, err = c.Tags(ctx, src, "t")
	assert.ErrorIs(t, err, ErrUnauthorized)
	status = http.StatusForbidden
	_, err = c.Tags(ctx, src, "t")
	assert.ErrorIs(t, err, ErrUnauthorized)

	status = http.StatusNotFound
	_, err = c.Tags(ctx, src, "")
	require.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "needs a token")
	_, err = c.Tags(ctx, src, "t")
	require.ErrorIs(t, err, ErrNotFound)
	assert.NotContains(t, err.Error(), "needs a token")

	status = http.StatusTeapot
	_, err = c.Tags(ctx, src, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "418")

	status, body = http.StatusOK, strings.Repeat("x", int(maxAPIBytes)+10)
	_, err = c.Tags(ctx, src, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "larger than")

	status, body = http.StatusOK, "not json"
	_, err = c.Tags(ctx, src, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected tags response")
	_, err = c.Commit(ctx, src, "v1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected commit response")

	status, body = http.StatusOK, `{"id":""}`
	_, err = c.Commit(ctx, src, "v1", "")
	assert.Error(t, err)
}

func TestGetUnreachableScrubsToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := &Client{AllowInsecure: true} // nil HTTP: falls back to the default client
	src, err := c.ParseURL(url + "/a/b")
	require.NoError(t, err)
	_, err = c.Tags(context.Background(), src, "tok")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unreachable")

	assert.EqualError(t, scrub(errString("dial tok failed"), "tok"), "dial *** failed")
	assert.EqualError(t, scrub(errString("dial failed"), ""), "dial failed")
}

type errString string

func (e errString) Error() string { return string(e) }

func TestListRefsFollowsPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := 100
		if r.URL.Query().Get("page") == "2" {
			n = 50
		}
		items := make([]string, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, fmt.Sprintf(`{"name":"p%s-%d","commit":{"sha":"s"}}`, r.URL.Query().Get("page"), i))
		}
		_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), GitHubAPI: srv.URL + "/"}
	src, err := c.ParseURL("https://github.com/owner/repo")
	require.NoError(t, err)

	tags, err := c.Tags(context.Background(), src, "")
	require.NoError(t, err)
	assert.Len(t, tags, 150, "refs beyond the first page must be listed")
}

func TestListRefsBeyondTwentyPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		n := refPageSize
		if page == "25" {
			n = 10
		}
		items := make([]string, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, fmt.Sprintf(`{"name":"p%s-%d","commit":{"sha":"s"}}`, page, i))
		}
		_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), GitHubAPI: srv.URL + "/"}
	src, err := c.ParseURL("https://github.com/owner/repo")
	require.NoError(t, err)

	tags, err := c.Tags(context.Background(), src, "")
	require.NoError(t, err)
	assert.Len(t, tags, 24*refPageSize+10, "no page cap: 2,000+ refs must all be listed")
}

func TestListRefsErrorsWhenHostIgnoresPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := make([]string, 0, refPageSize)
		for i := 0; i < refPageSize; i++ {
			items = append(items, fmt.Sprintf(`{"name":"t%d","commit":{"sha":"s"}}`, i))
		}
		_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), GitHubAPI: srv.URL + "/"}
	src, err := c.ParseURL("https://github.com/owner/repo")
	require.NoError(t, err)

	_, err = c.Tags(context.Background(), src, "")
	require.Error(t, err)
}
