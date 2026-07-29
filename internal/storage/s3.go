package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Client wraps a MinIO client for S3-compatible object storage operations.
type S3Client struct {
	client    *minio.Client
	bucket    string
	endpoint  string // Kept for diagnostics: error messages name the host we failed to reach
	region    string
	publicURL string // Optional: rewrite presigned URLs to this public base

	// bucketReady latches once the bucket is known to exist, so the common
	// path costs no extra round-trip. Cleared if the bucket disappears.
	bucketReady atomic.Bool
}

// NewS3Client creates a new S3-compatible storage client.
func NewS3Client(endpoint, accessKey, secretKey, bucket, region string, useSSL bool) (*S3Client, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return nil, err
	}

	return &S3Client{
		client:   client,
		bucket:   bucket,
		endpoint: endpoint,
		region:   region,
	}, nil
}

// Bucket returns the configured bucket name (for log and error messages).
func (s *S3Client) Bucket() string { return s.bucket }

// Endpoint returns the configured storage endpoint (for log and error messages).
func (s *S3Client) Endpoint() string { return s.endpoint }

// EnsureBucket creates the configured bucket when it does not already exist.
//
// Nothing else in the stack ever created it: not the compose file, not a
// migration, not the first upload. A fresh self-hosted install therefore had
// no bucket, and every artifact and workspace-icon upload failed against a
// storage backend that was itself perfectly healthy.
//
// It is safe to call concurrently and on every boot: an existing bucket is a
// no-op, and a lost create/create race resolves to success.
func (s *S3Client) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("checking bucket %q on %s: %w", s.bucket, s.endpoint, classify(err))
	}
	if exists {
		s.bucketReady.Store(true)
		return nil
	}

	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region}); err != nil {
		// Another API replica booting at the same time may have won the race
		// between our BucketExists and our MakeBucket. That is success, not a
		// failure — re-check before reporting.
		if exists, checkErr := s.client.BucketExists(ctx, s.bucket); checkErr == nil && exists {
			s.bucketReady.Store(true)
			return nil
		}
		// A create refused for lack of permission is reported as a missing
		// bucket: from the caller's side that is the actionable fact, and the
		// wrapped cause still names the permission error.
		return fmt.Errorf("creating bucket %q on %s: %w: %v", s.bucket, s.endpoint, ErrBucketMissing, classify(err))
	}

	s.bucketReady.Store(true)
	return nil
}

// SetPublicURL sets a public base URL for presigned download URLs.
// When set, the internal S3 endpoint in presigned URLs is replaced with this URL.
// Example: "https://mesh.example.com/s3" rewrites http://127.0.0.1:9000/bucket/key?sig=...
// to https://mesh.example.com/s3/bucket/key?sig=...
func (s *S3Client) SetPublicURL(publicURL string) {
	s.publicURL = strings.TrimRight(publicURL, "/")
}

// Upload stores an object in the bucket under the given key.
//
// The returned error is classified (see errors.go) so callers can tell a
// missing bucket from bad credentials from an unreachable host.
func (s *S3Client) Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error {
	// Make sure the bucket exists before streaming any bytes. This costs one
	// HEAD only until it first succeeds; afterwards it is a single atomic load.
	//
	// It has to happen *before* PutObject rather than as a retry after a
	// NoSuchBucket failure: a failed PutObject may already have consumed part
	// of the reader, and an io.Reader cannot be replayed. Pre-checking keeps
	// the upload single-shot and correct.
	if !s.bucketReady.Load() {
		if err := s.EnsureBucket(ctx); err != nil {
			return err
		}
	}

	_, err := s.client.PutObject(ctx, s.bucket, key, reader, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		cerr := classify(err)
		if errors.Is(cerr, ErrBucketMissing) {
			// The bucket went away under us (deleted while running). Re-arm so
			// the next upload recreates it instead of failing forever.
			s.bucketReady.Store(false)
		}
		return cerr
	}
	return nil
}

// ObjectInfo carries the object metadata needed to serve bytes over HTTP.
type ObjectInfo struct {
	Size         int64
	ContentType  string
	ETag         string
	LastModified time.Time
}

// GetObject opens an object for reading and returns its metadata alongside.
// The caller must close the returned ReadCloser.
//
// MinIO's GetObject is lazy — it does not contact the server until the first
// Read or Stat — so this calls Stat eagerly. Without it a missing object would
// surface as a truncated 200 response mid-stream instead of a clean 404.
func (s *S3Client) GetObject(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, ObjectInfo{}, classify(err)
	}

	stat, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, ObjectInfo{}, classify(err)
	}

	return obj, ObjectInfo{
		Size:         stat.Size,
		ContentType:  stat.ContentType,
		ETag:         stat.ETag,
		LastModified: stat.LastModified,
	}, nil
}

// GetPresignedURL generates a time-limited download URL for the given key.
// contentType overrides the response Content-Type; charset=utf-8 is appended
// automatically for text/* and application/json when not already present.
// filename sets Content-Disposition: attachment; empty string omits the header.
func (s *S3Client) GetPresignedURL(ctx context.Context, key string, expiry time.Duration, contentType, filename string) (string, error) {
	reqParams := make(url.Values)

	if contentType != "" {
		reqParams.Set("response-content-type", addCharset(contentType))
	}
	if filename != "" {
		reqParams.Set("response-content-disposition", contentDisposition(filename))
	}

	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, expiry, reqParams)
	if err != nil {
		return "", classify(err)
	}

	// Rewrite URL if a public URL is configured.
	if s.publicURL != "" {
		return s.rewriteURL(u), nil
	}

	return u.String(), nil
}

// addCharset appends "; charset=utf-8" to text/* and application/json types
// when they don't already carry a charset parameter.
func addCharset(ct string) string {
	lower := strings.ToLower(ct)
	if strings.Contains(lower, "charset") {
		return ct
	}
	if strings.HasPrefix(lower, "text/") || lower == "application/json" {
		return ct + "; charset=utf-8"
	}
	return ct
}

// contentDisposition builds a Content-Disposition: attachment value.
// Pure-ASCII filenames use the simple filename="" form.
// Non-ASCII filenames use RFC 5987 filename*=UTF-8”<pct-encoded> alongside
// an ASCII fallback for older clients.
func contentDisposition(filename string) string {
	ascii := true
	for _, r := range filename {
		if r > 127 {
			ascii = false
			break
		}
	}
	if ascii {
		return fmt.Sprintf("attachment; filename=%q", filename)
	}
	// RFC 5987 percent-encoding: url.QueryEscape encodes all non-unreserved
	// chars; replace "+" (space) with "%20" to comply with the pct-encoded form.
	encoded := strings.ReplaceAll(url.QueryEscape(filename), "+", "%20")
	// ASCII fallback: strip non-ASCII runes.
	var sb strings.Builder
	for _, r := range filename {
		if r <= 127 {
			sb.WriteRune(r)
		}
	}
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", sb.String(), encoded)
}

// rewriteURL replaces the scheme+host portion of a presigned URL with the public URL.
// Input:  http://127.0.0.1:9000/mesh-artifacts/key?X-Amz-...
// Output: https://mesh.example.com/s3/mesh-artifacts/key?X-Amz-...
func (s *S3Client) rewriteURL(u *url.URL) string {
	pub, err := url.Parse(s.publicURL)
	if err != nil {
		return u.String()
	}

	u.Scheme = pub.Scheme
	u.Host = pub.Host
	u.Path = strings.TrimRight(pub.Path, "/") + u.Path
	return u.String()
}

// Download fetches an object from the bucket and returns a reader for its contents.
// Caller must close the returned ReadCloser.
func (s *S3Client) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, classify(err)
	}
	return obj, nil
}

// Delete removes an object from the bucket.
func (s *S3Client) Delete(ctx context.Context, key string) error {
	return classify(s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}))
}
