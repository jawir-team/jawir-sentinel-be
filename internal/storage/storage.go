package storage

import (
	"context"
	"errors"
)

// ObjectAttrs contains the storage metadata required to register file evidence.
type ObjectAttrs struct {
	ContentType string
	Size        int64
}

// ErrObjectNotFound is returned when an object does not exist in storage.
var ErrObjectNotFound = errors.New("storage: object not found")

// Store signs direct uploads and inspects uploaded objects.
type Store interface {
	SignUploadURL(ctx context.Context, key, contentType string) (string, error)
	StatObject(ctx context.Context, key string) (ObjectAttrs, error)
}
