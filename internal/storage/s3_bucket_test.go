package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeS3 is a minimal S3-compatible server: enough of the protocol to drive
// BucketExists / MakeBucket / PutObject / GetObject without a MinIO container.
type fakeS3 struct {
	mu      sync.Mutex
	buckets map[string]bool
	objects map[string][]byte

	// forceStatus, when non-zero, makes every request answer with that status
	// so error classification can be exercised.
	forceStatus int

	headCalls atomic.Int32
	putBucket atomic.Int32
}

// minioNoSuchBucket is the S3 error code MinIO reports via the
// x-minio-error-code header when the bucket is gone.
const minioNoSuchBucket = "NoSuchBucket"

// s3Parts splits a request path into bucket and object key. A bucket-level
// request has an empty key — note that MakeBucket sends "/bucket/", so a naive
// "contains a slash" check misreads it as an object operation.
func s3Parts(path string) (bucket, key string) {
	trimmed := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

func newFakeS3() *fakeS3 {
	return &fakeS3{buckets: map[string]bool{}, objects: map[string][]byte{}}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.forceStatus != 0 {
		w.WriteHeader(f.forceStatus)
		return
	}

	bucket, key := s3Parts(r.URL.Path)

	f.mu.Lock()
	defer f.mu.Unlock()

	// Bucket-level operations.
	if key == "" {
		switch r.Method {
		case http.MethodHead, http.MethodGet:
			f.headCalls.Add(1)
			if f.buckets[bucket] {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		case http.MethodPut:
			f.putBucket.Add(1)
			f.buckets[bucket] = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Object-level operations.
	if !f.buckets[bucket] {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objects[bucket+"/"+key] = decodeAWSChunked(body)
		w.Header().Set("ETag", `"fakeetag"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodHead, http.MethodGet:
		body, ok := f.objects[bucket+"/"+key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", `"fakeetag"`)
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Last-Modified", "Tue, 29 Jul 2026 12:00:00 GMT")
		w.Header().Set("Content-Length", itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	case http.MethodDelete:
		delete(f.objects, bucket+"/"+key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// decodeAWSChunked unwraps the aws-chunked streaming-signature body framing
// that minio-go uses over plain HTTP. Each chunk is
// "<hexlen>;chunk-signature=<sig>\r\n<payload>\r\n", terminated by a zero-length
// chunk. Bodies without the framing are returned untouched.
func decodeAWSChunked(body []byte) []byte {
	if !strings.Contains(string(body), ";chunk-signature=") {
		return body
	}

	var out []byte
	rest := body
	for {
		nl := strings.Index(string(rest), "\r\n")
		if nl < 0 {
			break
		}
		header := string(rest[:nl])
		sizeHex, _, ok := strings.Cut(header, ";")
		if !ok {
			break
		}
		var size int
		for _, ch := range sizeHex {
			switch {
			case ch >= '0' && ch <= '9':
				size = size*16 + int(ch-'0')
			case ch >= 'a' && ch <= 'f':
				size = size*16 + int(ch-'a'+10)
			case ch >= 'A' && ch <= 'F':
				size = size*16 + int(ch-'A'+10)
			default:
				return out
			}
		}
		rest = rest[nl+2:]
		if size == 0 {
			break
		}
		if size > len(rest) {
			break
		}
		out = append(out, rest[:size]...)
		rest = rest[size:]
		if len(rest) >= 2 {
			rest = rest[2:] // trailing \r\n
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func newTestClient(t *testing.T, srv *httptest.Server) *S3Client {
	t.Helper()
	endpoint := strings.TrimPrefix(srv.URL, "http://")
	c, err := NewS3Client(endpoint, "key", "secret", "mesh-artifacts", "us-east-1", false)
	if err != nil {
		t.Fatalf("NewS3Client: %v", err)
	}
	return c
}

func TestEnsureBucketCreatesMissingBucket(t *testing.T) {
	fake := newFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv)
	if err := c.EnsureBucket(context.Background()); err != nil {
		t.Fatalf("EnsureBucket on empty storage: %v", err)
	}

	fake.mu.Lock()
	created := fake.buckets["mesh-artifacts"]
	fake.mu.Unlock()
	if !created {
		t.Fatal("bucket was not created — this is the whole bug")
	}
	if got := fake.putBucket.Load(); got != 1 {
		t.Fatalf("expected exactly 1 create call, got %d", got)
	}
}

func TestEnsureBucketIsNoOpWhenBucketExists(t *testing.T) {
	fake := newFakeS3()
	fake.buckets["mesh-artifacts"] = true
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv)
	if err := c.EnsureBucket(context.Background()); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}
	if got := fake.putBucket.Load(); got != 0 {
		t.Fatalf("existing bucket must not be re-created, got %d create calls", got)
	}
}

func TestEnsureBucketReportsUnreachableStorage(t *testing.T) {
	// A server that is closed immediately: connections are refused.
	srv := httptest.NewServer(newFakeS3())
	endpoint := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()

	c, err := NewS3Client(endpoint, "key", "secret", "mesh-artifacts", "us-east-1", false)
	if err != nil {
		t.Fatalf("NewS3Client: %v", err)
	}

	err = c.EnsureBucket(context.Background())
	if err == nil {
		t.Fatal("expected an error against a dead endpoint")
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("expected ErrUnreachable, got %v", err)
	}
	// The message must name the bucket and the endpoint, or the operator
	// cannot tell which storage is broken.
	if !strings.Contains(err.Error(), "mesh-artifacts") || !strings.Contains(err.Error(), endpoint) {
		t.Fatalf("error must name bucket and endpoint, got: %v", err)
	}
}

func TestEnsureBucketReportsAccessDenied(t *testing.T) {
	fake := newFakeS3()
	fake.forceStatus = http.StatusForbidden
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.EnsureBucket(context.Background())
	if err == nil {
		t.Fatal("expected an error when storage returns 403")
	}
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestUploadCreatesBucketOnFirstUse(t *testing.T) {
	fake := newFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv)

	// No EnsureBucket at boot (storage was down then) — the upload itself must
	// still work rather than fail with "upload failed".
	err := c.Upload(context.Background(), "workspaces/w/icon.png", strings.NewReader("\x89PNGdata"), 8, "image/png")
	if err != nil {
		t.Fatalf("Upload on a storage with no bucket: %v", err)
	}

	fake.mu.Lock()
	stored, ok := fake.objects["mesh-artifacts/workspaces/w/icon.png"]
	fake.mu.Unlock()
	if !ok {
		t.Fatal("object was not stored")
	}
	if string(stored) != "\x89PNGdata" {
		t.Fatalf("stored bytes differ: %q", stored)
	}
}

func TestUploadChecksBucketOnlyUntilItIsKnownPresent(t *testing.T) {
	fake := newFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv)
	ctx := context.Background()

	if err := c.Upload(ctx, "a.png", strings.NewReader("x"), 1, "image/png"); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	after := fake.headCalls.Load()

	if err := c.Upload(ctx, "b.png", strings.NewReader("y"), 1, "image/png"); err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if got := fake.headCalls.Load(); got != after {
		t.Fatalf("bucket check should be latched after first success: %d -> %d", after, got)
	}
}

func TestUploadSurfacesAccessDenied(t *testing.T) {
	fake := newFakeS3()
	fake.forceStatus = http.StatusForbidden
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.Upload(context.Background(), "a.png", strings.NewReader("x"), 1, "image/png")
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestGetObjectReturnsBytesAndMetadata(t *testing.T) {
	fake := newFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv)
	ctx := context.Background()
	if err := c.Upload(ctx, "icon.png", strings.NewReader("\x89PNGbody"), 8, "image/png"); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	rc, info, err := c.GetObject(ctx, "icon.png")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "\x89PNGbody" {
		t.Fatalf("body mismatch: %q", body)
	}
	if info.Size != 8 {
		t.Fatalf("size: got %d want 8", info.Size)
	}
	if info.ContentType != "image/png" {
		t.Fatalf("content type: got %q", info.ContentType)
	}
	if info.ETag == "" {
		t.Fatal("ETag must be surfaced so the handler can answer 304")
	}
}

func TestGetObjectMissingKeyIsNotFound(t *testing.T) {
	fake := newFakeS3()
	fake.buckets["mesh-artifacts"] = true
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv)
	_, _, err := c.GetObject(context.Background(), "nope.png")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestEnsureBucketTreatsALostCreateRaceAsSuccess(t *testing.T) {
	// Two API replicas booting together: ours sees no bucket, the other
	// creates it, our MakeBucket is refused. That is success, not a failure.
	fake := newFakeS3()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, key := s3Parts(r.URL.Path); r.Method == http.MethodPut && key == "" {
			// Simulate the other replica winning, then refuse our create.
			fake.mu.Lock()
			fake.buckets["mesh-artifacts"] = true
			fake.mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if err := c.EnsureBucket(context.Background()); err != nil {
		t.Fatalf("a lost create race must not be reported as an error: %v", err)
	}
}

func TestEnsureBucketReportsUncreatableBucket(t *testing.T) {
	// Credentials that may read but not create: the operator has to create the
	// bucket by hand, and the message must say so.
	fake := newFakeS3()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, key := s3Parts(r.URL.Path); r.Method == http.MethodPut && key == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.EnsureBucket(context.Background())
	if !errors.Is(err, ErrBucketMissing) {
		t.Fatalf("expected ErrBucketMissing, got %v", err)
	}
	if !strings.Contains(err.Error(), "mesh-artifacts") {
		t.Fatalf("message must name the bucket: %v", err)
	}
}

func TestUploadRecoversAfterBucketIsDeletedAtRuntime(t *testing.T) {
	fake := newFakeS3()
	var dropped atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The first object PUT after the bucket "vanishes" answers NoSuchBucket.
		_, key := s3Parts(r.URL.Path)
		isObjectPut := r.Method == http.MethodPut && key != ""
		if isObjectPut && dropped.CompareAndSwap(false, true) {
			fake.mu.Lock()
			delete(fake.buckets, "mesh-artifacts")
			fake.mu.Unlock()
			w.Header().Set("x-minio-error-code", minioNoSuchBucket)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	ctx := context.Background()

	// Latch the bucket as present, then have it disappear mid-flight.
	if err := c.EnsureBucket(ctx); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}
	err := c.Upload(ctx, "a.png", strings.NewReader("x"), 1, "image/png")
	if !errors.Is(err, ErrBucketMissing) {
		t.Fatalf("expected ErrBucketMissing on the failed upload, got %v", err)
	}

	// The next upload must recreate the bucket rather than fail forever.
	if err := c.Upload(ctx, "b.png", strings.NewReader("y"), 1, "image/png"); err != nil {
		t.Fatalf("upload after re-arm should recreate the bucket, got %v", err)
	}
}

func TestDownloadAndDeleteClassifyErrors(t *testing.T) {
	fake := newFakeS3()
	fake.forceStatus = http.StatusForbidden
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv)
	ctx := context.Background()

	if err := c.Delete(ctx, "a.png"); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("Delete: expected ErrAccessDenied, got %v", err)
	}

	// Download is lazy in minio-go, so the error surfaces on read.
	rc, err := c.Download(ctx, "a.png")
	if err == nil {
		defer rc.Close()
		if _, readErr := io.ReadAll(rc); readErr == nil {
			t.Fatal("expected the read of a forbidden object to fail")
		}
	}
}

func TestDeleteSucceedsOnHealthyStorage(t *testing.T) {
	fake := newFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv)
	ctx := context.Background()
	if err := c.Upload(ctx, "a.png", strings.NewReader("x"), 1, "image/png"); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if err := c.Delete(ctx, "a.png"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestBucketAndEndpointAccessors(t *testing.T) {
	fake := newFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv)
	if c.Bucket() != "mesh-artifacts" {
		t.Fatalf("Bucket(): %q", c.Bucket())
	}
	if c.Endpoint() != strings.TrimPrefix(srv.URL, "http://") {
		t.Fatalf("Endpoint(): %q", c.Endpoint())
	}
}
