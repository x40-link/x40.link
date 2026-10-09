package storage

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrAborted is returned when a conditional mutation observes a changed
	// ETag. The API maps this to gRPC ABORTED.
	ErrAborted = errors.New("short link changed before mutation")
	// ErrRequestIDConflict means the caller reused an idempotency key with a
	// different request payload before its 24-hour retention expired.
	ErrRequestIDConflict = errors.New("request ID has a different payload")
)

// ManagedLink is the durable state of one v1alpha ShortLink. Owner is private
// storage metadata and is never exposed in the API resource.
type ManagedLink struct {
	Name           string
	Path           string
	DestinationURL string
	Annotations    map[string]string
	Owner          string
	UID            string
	CreateTime     time.Time
	UpdateTime     time.Time
	ETag           string
}

// Clone prevents callers from mutating an in-memory record's annotations.
func (link ManagedLink) Clone() ManagedLink {
	if link.Annotations != nil {
		annotations := make(map[string]string, len(link.Annotations))
		for key, value := range link.Annotations {
			annotations[key] = value
		}
		link.Annotations = annotations
	}
	return link
}

// ManagedStore performs the atomic operations required by the v1alpha
// management API. Every writable production backend must implement it; the
// legacy Storer interface remains for public HTTP redirects and migration.
type ManagedStore interface {
	// CreateManaged claims a source address only if absent. When requestID is
	// set, the owner/fingerprint/result tuple is retained for 24 hours.
	CreateManaged(ctx context.Context, link ManagedLink, requestID, fingerprint string, now time.Time) (ManagedLink, error)
	// GetRequest checks a retained create result before the service generates
	// a suffix for a retry. It returns ErrNotFound when absent or expired.
	GetRequest(ctx context.Context, owner, requestID, fingerprint string, now time.Time) (ManagedLink, error)
	GetManaged(ctx context.Context, name, owner string) (ManagedLink, error)
	ListManaged(ctx context.Context, owner, domain string) ([]ManagedLink, error)
	// UpdateManaged compares the previously read ETag atomically. Callers
	// retry on ErrAborted when the user did not request a precondition.
	UpdateManaged(ctx context.Context, link ManagedLink, previousETag string) (ManagedLink, error)
	DeleteManaged(ctx context.Context, name, owner, expectedETag string) error
}
