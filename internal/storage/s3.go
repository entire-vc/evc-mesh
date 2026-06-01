package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Client wraps a MinIO client for S3-compatible object storage operations.
type S3Client struct {
	client    *minio.Client
	bucket    string
	publicURL string // Optional: rewrite presigned URLs to this public base
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
		client: client,
		bucket: bucket,
	}, nil
}

// SetPublicURL sets a public base URL for presigned download URLs.
// When set, the internal S3 endpoint in presigned URLs is replaced with this URL.
// Example: "https://mesh.example.com/s3" rewrites http://127.0.0.1:9000/bucket/key?sig=...
// to https://mesh.example.com/s3/bucket/key?sig=...
func (s *S3Client) SetPublicURL(publicURL string) {
	s.publicURL = strings.TrimRight(publicURL, "/")
}

// Upload stores an object in the bucket under the given key.
func (s *S3Client) Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, reader, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	return err
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
		return "", err
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
// Non-ASCII filenames use RFC 5987 filename*=UTF-8''<pct-encoded> alongside
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
		return fmt.Sprintf(`attachment; filename="%s"`, filename)
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
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, sb.String(), encoded)
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
		return nil, err
	}
	return obj, nil
}

// Delete removes an object from the bucket.
func (s *S3Client) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}
