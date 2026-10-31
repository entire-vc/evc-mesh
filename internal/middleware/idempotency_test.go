package middleware

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

func TestValidIdempotencyKey(t *testing.T) {
	for _, k := range []string{"a", "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", strings.Repeat("x", 255), "op:42/retry"} {
		assert.True(t, validIdempotencyKey(k), k)
	}
	for _, k := range []string{"", strings.Repeat("x", 256), "has space", "tab\there", "ключ", "nl\n"} {
		assert.False(t, validIdempotencyKey(k), k)
	}
}

// A part's headers are part of the request: the same bytes under another file
// name are another upload. A body that does not parse as multipart is hashed
// raw, so identical bytes still match and different ones do not.
func TestFingerprintRequest_MultipartHeadersAndMalformed(t *testing.T) {
	upload := func(filename string) *http.Request {
		var buf bytes.Buffer
		mwr := multipart.NewWriter(&buf)
		fw, err := mwr.CreateFormFile("file", filename)
		require.NoError(t, err)
		_, _ = fw.Write([]byte("same"))
		require.NoError(t, mwr.Close())
		req := httptest.NewRequest(http.MethodPost, "/", &buf)
		req.Header.Set("Content-Type", mwr.FormDataContentType())
		return req
	}
	a, _, err := fingerprintRequest(upload("a.png"), 64<<20)
	require.NoError(t, err)
	b, _, err := fingerprintRequest(upload("b.png"), 64<<20)
	require.NoError(t, err)
	assert.NotEqual(t, a, b)

	broken := func(body string) string {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=zz")
		h, _, err := fingerprintRequest(req, 64<<20)
		require.NoError(t, err)
		got, _ := io.ReadAll(req.Body)
		assert.Equal(t, body, string(got), "handler reads the original body")
		return h
	}
	assert.Equal(t, broken("--zz\r\nno end"), broken("--zz\r\nno end"))
	assert.NotEqual(t, broken("--zz\r\nno end"), broken("--zz\r\nno end!"))
}

func multipartUpload(t *testing.T, boundary string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mwr := multipart.NewWriter(&buf)
	require.NoError(t, mwr.SetBoundary(boundary))
	fw, err := mwr.CreateFormFile("file", "shot.png")
	require.NoError(t, err)
	_, _ = fw.Write(content)
	require.NoError(t, mwr.WriteField("name", "shot"))
	require.NoError(t, mwr.Close())
	req := httptest.NewRequest(http.MethodPost, "/", &buf)
	req.Header.Set("Content-Type", mwr.FormDataContentType())
	return req
}

// A retried upload usually comes with a fresh random boundary: same parts,
// same fingerprint. Other content is another fingerprint. The body the handler
// reads afterwards is the original, byte for byte, for both the in-memory and
// the spooled path.
func TestFingerprintRequest_MultipartBoundaryAndBodyRestored(t *testing.T) {
	for _, size := range []int{10, idemMemBody + 4096} {
		content := bytes.Repeat([]byte{0xAB}, size)
		r1 := multipartUpload(t, "aaaaaaaaaaaaaaaaaaaa", content)
		r2 := multipartUpload(t, "bbbbbbbbbbbbbbbbbbbb", content)
		raw1, _ := io.ReadAll(multipartUpload(t, "aaaaaaaaaaaaaaaaaaaa", content).Body)

		h1, clean1, err := fingerprintRequest(r1, 64<<20)
		require.NoError(t, err)
		t.Cleanup(clean1)
		h2, clean2, err := fingerprintRequest(r2, 64<<20)
		require.NoError(t, err)
		t.Cleanup(clean2)
		assert.Equal(t, h1, h2, "size %d: boundary does not change the fingerprint", size)

		got, err := io.ReadAll(r1.Body)
		require.NoError(t, err)
		assert.Equal(t, raw1, got, "size %d: handler reads the original body", size)

		other := append(bytes.Clone(content), 1)
		h3, clean3, err := fingerprintRequest(multipartUpload(t, "aaaaaaaaaaaaaaaaaaaa", other), 64<<20)
		require.NoError(t, err)
		t.Cleanup(clean3)
		assert.NotEqual(t, h1, h3, "size %d: other content, other fingerprint", size)
	}
}

// The boundary string inside file content is content: two uploads whose files
// differ only there are different requests, whatever boundaries they came with.
func TestFingerprintRequest_BoundaryInsideContent(t *testing.T) {
	a, b := "aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb"
	h1, _, err := fingerprintRequest(multipartUpload(t, a, []byte("x"+a+"y")), 64<<20)
	require.NoError(t, err)
	h2, _, err := fingerprintRequest(multipartUpload(t, b, []byte("x"+b+"y")), 64<<20)
	require.NoError(t, err)
	assert.NotEqual(t, h1, h2)
}

func TestFingerprintRequest_JSONBodyAndLimit(t *testing.T) {
	req := func(body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	a, _, err := fingerprintRequest(req(`{"body":"a"}`), idemMemBody)
	require.NoError(t, err)
	b, _, err := fingerprintRequest(req(`{"body":"b"}`), idemMemBody)
	require.NoError(t, err)
	assert.NotEqual(t, a, b)

	big := strings.Repeat("x", idemMemBody+10)
	_, clean, err := fingerprintRequest(req(big), idemMemBody+5)
	defer clean()
	assert.ErrorIs(t, err, errIdemBodyTooLarge)
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// The paths that answer before the store is consulted, and the store being
// down. The store is the real repo on a Postgres address nothing listens on,
// so its failure is a real connection error, not a stub.
func TestIdempotency_EarlyAnswers(t *testing.T) {
	down, err := sqlx.Open("postgres", "postgres://mesh:mesh@127.0.0.1:1/mesh?sslmode=disable&connect_timeout=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = down.Close() })
	store := postgres.NewIdempotencyRepo(down)
	actor := uuid.New()

	run := func(key string, withActor bool, body io.Reader, maxBody int64) (*httptest.ResponseRecorder, bool) {
		reached := false
		e := echo.New()
		h := Idempotency(store, maxBody)(func(c echo.Context) error {
			reached = true
			return c.NoContent(http.StatusCreated)
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/t/comments", body)
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		if key != "" {
			req.Header.Set(HeaderIdempotencyKey, key)
		}
		if withActor {
			req = req.WithContext(actorctx.WithActor(req.Context(), actor, domain.ActorTypeAgent))
		}
		rec := httptest.NewRecorder()
		require.NoError(t, h(e.NewContext(req, rec)))
		return rec, reached
	}

	rec, reached := run("", true, strings.NewReader(`{}`), 0)
	assert.True(t, reached, "no key: the route runs as before")
	assert.Equal(t, http.StatusCreated, rec.Code)

	rec, reached = run("bad key", true, strings.NewReader(`{}`), 0)
	assert.False(t, reached)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec, reached = run("k", false, strings.NewReader(`{}`), 0)
	assert.True(t, reached, "no actor: nothing to scope by, the route runs")
	assert.Equal(t, http.StatusCreated, rec.Code)

	rec, reached = run("k", true, strings.NewReader(strings.Repeat("x", idemMemBody+10)), idemMemBody+5)
	assert.False(t, reached)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	rec, reached = run("k", true, failingBody{}, 0)
	assert.False(t, reached)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec, reached = run("k", true, strings.NewReader(`{}`), 0)
	assert.False(t, reached, "store down: refuse rather than risk a double write")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// A done record without a stored content type replays as JSON.
func TestAnswerHeldKey_DefaultContentType(t *testing.T) {
	status := http.StatusCreated
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", http.NoBody), rec)
	require.NoError(t, answerHeldKey(c, &domain.IdempotencyRecord{
		RequestHash: "h", State: domain.IdempotencyDone, ResponseStatus: &status, ResponseBody: []byte(`{"id":1}`),
	}, "h"))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "true", rec.Header().Get(HeaderIdempotentReplayed))
	assert.Equal(t, echo.MIMEApplicationJSON, rec.Header().Get(echo.HeaderContentType))
	assert.JSONEq(t, `{"id":1}`, rec.Body.String())
}

// A response too large to keep passes through untouched and is not stored.
func TestIdemRecorder_Overflow(t *testing.T) {
	under := httptest.NewRecorder()
	rw := &idemRecorder{ResponseWriter: under}
	big := bytes.Repeat([]byte("x"), idemMaxStoredResponse/2+1)
	_, _ = rw.Write(big)
	_, _ = rw.Write(big)
	rw.Flush()
	assert.True(t, rw.overflow)
	assert.Zero(t, rw.buf.Len())
	assert.Equal(t, 2*len(big), under.Body.Len())
	assert.True(t, under.Flushed)
}
