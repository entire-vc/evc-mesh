package storage

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/minio/minio-go/v7"
)

// Sentinel errors describing *why* a storage operation failed.
//
// Every one of these means something different to the operator — "the bucket
// is gone", "your keys are wrong", "the storage host is down" — and each has a
// different fix. Collapsing them into one opaque failure is what produced the
// undiagnosable "upload failed" that self-hosted installs hit on day one.
// Callers use errors.Is to turn them into an actionable message.
var (
	// ErrBucketMissing means the configured bucket does not exist and could
	// not be created (usually the credentials lack CreateBucket permission).
	ErrBucketMissing = errors.New("storage bucket does not exist")

	// ErrAccessDenied means the storage endpoint rejected our credentials.
	ErrAccessDenied = errors.New("storage rejected the credentials")

	// ErrNotFound means the bucket exists but holds no object under that key.
	ErrNotFound = errors.New("object not found in storage")

	// ErrUnreachable means we never got an answer from the storage endpoint
	// (wrong host/port, DNS failure, service down, network partition).
	ErrUnreachable = errors.New("storage endpoint is unreachable")
)

// classify maps a MinIO/S3 error onto one of the sentinels above, keeping the
// original error in the chain so logs still carry the full detail.
//
// It matches on the S3 error code first and falls back to the HTTP status,
// because non-MinIO S3 implementations (R2, Garage, Ceph) do not all use the
// same code strings for the same condition. Errors it does not recognise are
// returned unchanged rather than mislabelled.
func classify(err error) error {
	if err == nil {
		return nil
	}

	resp := minio.ToErrorResponse(err)

	switch resp.Code {
	case minio.NoSuchBucket, "NoSuchBucketPolicy":
		return fmt.Errorf("%w: %v", ErrBucketMissing, err)
	case minio.NoSuchKey:
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case minio.AccessDenied, minio.SignatureDoesNotMatch, "InvalidAccessKeyId":
		return fmt.Errorf("%w: %v", ErrAccessDenied, err)
	}

	switch resp.StatusCode {
	case http.StatusForbidden, http.StatusUnauthorized:
		return fmt.Errorf("%w: %v", ErrAccessDenied, err)
	case http.StatusNotFound:
		// A 404 with no parseable S3 code: the key is the more likely
		// subject, since the bucket is ensured at boot.
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}

	// StatusCode == 0 means we never completed an HTTP exchange: connection
	// refused, DNS failure, TLS handshake error, timeout. Anything with a
	// status is a real server answer and is passed through as-is.
	if resp.StatusCode == 0 && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}

	return err
}
