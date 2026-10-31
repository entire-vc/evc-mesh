package middleware

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// uploadThroughReplacedRequest serves one multipart upload over a real HTTP
// server, through a middleware that swaps the request for a WithContext copy
// the way auth.go does, and returns the multipart-* files left in TMPDIR.
func uploadThroughReplacedRequest(t *testing.T, mws ...echo.MiddlewareFunc) []string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	e := echo.New()
	e.Use(mws...)
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.SetRequest(c.Request().WithContext(c.Request().Context()))
			return next(c)
		}
	})
	e.POST("/upload", func(c echo.Context) error {
		// A 1 KiB memory limit pushes the file to disk like a >32 MB
		// artifact does under the default limit, without a 32 MB body.
		if err := c.Request().ParseMultipartForm(1 << 10); err != nil {
			return err
		}
		fh, err := c.FormFile("file")
		if err != nil {
			return err
		}
		f, err := fh.Open()
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, _ = io.Copy(io.Discard, f)
		return c.NoContent(http.StatusCreated)
	})
	srv := httptest.NewServer(e)
	defer srv.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "big.bin")
	require.NoError(t, err)
	_, err = part.Write(bytes.Repeat([]byte("x"), 64<<10))
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	resp, err := http.Post(srv.URL+"/upload", mw.FormDataContentType(), &body)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// The server may clean up just after the response reaches the client;
	// give it a moment before deciding a file was left behind.
	var left []string
	deadline := time.Now().Add(time.Second)
	for {
		left, err = filepath.Glob(filepath.Join(tmp, "multipart-*"))
		require.NoError(t, err)
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Positive control: without the cleanup the replaced request leaks its temp
// file, so the green test below is the middleware and not a harness that
// never writes to disk.
func TestMultipartCleanup_WithoutItTempFileLeaks(t *testing.T) {
	left := uploadThroughReplacedRequest(t)
	require.Len(t, left, 1, "expected the leak this middleware exists for")
	info, err := os.Stat(left[0])
	require.NoError(t, err)
	require.EqualValues(t, 64<<10, info.Size())
}

func TestMultipartCleanup_RemovesTempFiles(t *testing.T) {
	left := uploadThroughReplacedRequest(t, MultipartCleanup())
	require.Empty(t, left)
}
