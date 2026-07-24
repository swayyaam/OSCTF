// Package storage persists challenge attachments and future blobs behind the
// ObjectStore interface. v0.1 implementation: S3Store (MinIO). The interface is
// S3-shaped on purpose so the concrete store can change without touching callers.
package storage

import (
	"context"
	"io"
)

// ObjectStore is the blob persistence interface. Only main.go and this package's
// tests may reference the concrete implementation.
type ObjectStore interface {
	// Put stores r under key. size is the exact byte length; contentType is the MIME type.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get opens the object at key for reading. The caller closes it.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes the object at key. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
}
