package storage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/minio/minio-go/v7"
)

// The whole point of classify is that the operator can tell the three
// actionable storage faults apart. Each S3 shape must land on the right one.
func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error // nil means "returned unchanged"
	}{
		{"nil stays nil", nil, nil},
		{
			"NoSuchBucket",
			minio.ErrorResponse{Code: minio.NoSuchBucket, StatusCode: http.StatusNotFound},
			ErrBucketMissing,
		},
		{
			"NoSuchKey",
			minio.ErrorResponse{Code: minio.NoSuchKey, StatusCode: http.StatusNotFound},
			ErrNotFound,
		},
		{
			"AccessDenied",
			minio.ErrorResponse{Code: minio.AccessDenied, StatusCode: http.StatusForbidden},
			ErrAccessDenied,
		},
		{
			"SignatureDoesNotMatch",
			minio.ErrorResponse{Code: minio.SignatureDoesNotMatch, StatusCode: http.StatusForbidden},
			ErrAccessDenied,
		},
		{
			"InvalidAccessKeyId",
			minio.ErrorResponse{Code: "InvalidAccessKeyId", StatusCode: http.StatusForbidden},
			ErrAccessDenied,
		},
		{
			// Non-MinIO S3 implementations do not all use the same code
			// strings, so the HTTP status has to carry the decision.
			"403 with an unfamiliar code",
			minio.ErrorResponse{Code: "SomeVendorCode", StatusCode: http.StatusForbidden},
			ErrAccessDenied,
		},
		{
			"401 with an unfamiliar code",
			minio.ErrorResponse{Code: "SomeVendorCode", StatusCode: http.StatusUnauthorized},
			ErrAccessDenied,
		},
		{
			"404 with an unfamiliar code",
			minio.ErrorResponse{Code: "SomeVendorCode", StatusCode: http.StatusNotFound},
			ErrNotFound,
		},
		{
			// No HTTP exchange ever completed — connection refused, DNS, TLS.
			"transport failure",
			errors.New("dial tcp 127.0.0.1:9000: connect: connection refused"),
			ErrUnreachable,
		},
		{
			// A cancelled request is our own doing, not a broken backend.
			"context cancellation is not a storage fault",
			fmt.Errorf("get object: %w", context.Canceled),
			nil,
		},
		{
			// A real server answer we have no rule for must not be
			// mislabelled — a confidently wrong message is worse than a vague
			// one.
			"unmapped server error passes through",
			minio.ErrorResponse{Code: "SlowDown", StatusCode: http.StatusServiceUnavailable},
			nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classify(tt.err)

			if tt.err == nil {
				if got != nil {
					t.Fatalf("classify(nil) = %v, want nil", got)
				}
				return
			}

			if tt.want == nil {
				// Unchanged: must not carry any of our sentinels.
				for _, sentinel := range []error{ErrBucketMissing, ErrAccessDenied, ErrNotFound, ErrUnreachable} {
					if errors.Is(got, sentinel) {
						t.Fatalf("classify(%v) was labelled %v, expected it to pass through unchanged", tt.err, sentinel)
					}
				}
				return
			}

			if !errors.Is(got, tt.want) {
				t.Fatalf("classify(%v) = %v, want errors.Is(..., %v)", tt.err, got, tt.want)
			}
			// The original cause must survive in the chain for the logs.
			if got.Error() == tt.want.Error() {
				t.Fatalf("classify dropped the underlying cause: %v", got)
			}
		})
	}
}
