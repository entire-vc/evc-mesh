package storage

import (
	"context"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Client wraps a MinIO client for S3-compatible object storage operations.
type S3Client struct {
	client *minio.Client
	bucket string
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
	return u.String(), nil
}

// Delete removes an object from the bucket.
func (s *S3Client) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}
