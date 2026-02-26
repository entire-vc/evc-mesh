package storage

import (
	"context"
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
func (s *S3Client) GetPresignedURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	reqParams := make(url.Values)
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

// Delete removes an object from the bucket.
func (s *S3Client) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}
