package memory

import (
	"context"
	"net/url"
	"sort"
	"time"

	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/andrewhowdencom/x40.link/storage"
)

type managedRequest struct {
	fingerprint string
	result      storage.ManagedLink
	expires     time.Time
}

var _ storage.ManagedStore = (*HashTable)(nil)

func (ht *HashTable) GetRequest(_ context.Context, owner, requestID, fingerprint string, now time.Time) (storage.ManagedLink, error) {
	if owner == "" {
		return storage.ManagedLink{}, storage.ErrUnauthorized
	}
	ht.mu.RLock()
	defer ht.mu.RUnlock()
	previous, ok := ht.requests[owner+"\x00"+requestID]
	if !ok || !now.Before(previous.expires) {
		return storage.ManagedLink{}, storage.ErrNotFound
	}
	if previous.fingerprint != fingerprint {
		return storage.ManagedLink{}, storage.ErrRequestIDConflict
	}
	return previous.result.Clone(), nil
}

// CreateManaged claims a canonical source address and its optional
// idempotency key under one lock. A retry returns the original result even if
// the link has since been updated or deleted.
func (ht *HashTable) CreateManaged(_ context.Context, link storage.ManagedLink, requestID, fingerprint string, now time.Time) (storage.ManagedLink, error) {
	if err := validManagedIdentity(link); err != nil {
		return storage.ManagedLink{}, err
	}
	ht.mu.Lock()
	defer ht.mu.Unlock()

	if requestID != "" {
		if fingerprint == "" {
			return storage.ManagedLink{}, storage.ErrInvalidSource
		}
		key := link.Owner + "\x00" + requestID
		if previous, ok := ht.requests[key]; ok && now.Before(previous.expires) {
			if previous.fingerprint != fingerprint {
				return storage.ManagedLink{}, storage.ErrRequestIDConflict
			}
			return previous.result.Clone(), nil
		}
	}

	if existing, ok := ht.records[link.Name]; ok {
		if existing.Owner != link.Owner {
			return storage.ManagedLink{}, storage.ErrUnauthorized
		}
		return storage.ManagedLink{}, storage.ErrAlreadyExists
	}
	legacy, err := ht.hasLegacyName(link.Name)
	if err != nil {
		return storage.ManagedLink{}, err
	}
	if legacy {
		return storage.ManagedLink{}, storage.ErrUnauthorized
	}

	stored := link.Clone()
	ht.records[link.Name] = stored
	if requestID != "" {
		ht.requests[link.Owner+"\x00"+requestID] = managedRequest{
			fingerprint: fingerprint,
			result:      stored.Clone(),
			expires:     now.Add(24 * time.Hour),
		}
	}
	return stored.Clone(), nil
}

func (ht *HashTable) GetManaged(_ context.Context, name, owner string) (storage.ManagedLink, error) {
	if owner == "" {
		return storage.ManagedLink{}, storage.ErrUnauthorized
	}
	ht.mu.RLock()
	defer ht.mu.RUnlock()
	link, ok := ht.records[name]
	if !ok {
		legacy, err := ht.hasLegacyName(name)
		if err != nil {
			return storage.ManagedLink{}, err
		}
		if legacy {
			return storage.ManagedLink{}, storage.ErrUnauthorized
		}
		return storage.ManagedLink{}, storage.ErrNotFound
	}
	if link.Owner != owner {
		return storage.ManagedLink{}, storage.ErrUnauthorized
	}
	return link.Clone(), nil
}

func (ht *HashTable) ListManaged(_ context.Context, owner, domain string) ([]storage.ManagedLink, error) {
	if owner == "" {
		return nil, storage.ErrUnauthorized
	}
	ht.mu.RLock()
	defer ht.mu.RUnlock()
	links := make([]storage.ManagedLink, 0)
	for _, link := range ht.records {
		if link.Owner != owner {
			continue
		}
		linkDomain, _, err := shortlink.ParseResourceName(link.Name)
		if err != nil {
			return nil, storage.ErrCorrupt
		}
		if domain == "" || linkDomain == domain {
			links = append(links, link.Clone())
		}
	}
	sort.Slice(links, func(i, j int) bool { return links[i].Name < links[j].Name })
	return links, nil
}

// UpdateManaged compares the ETag from the caller's read while holding the
// write lock, preventing concurrent updates from silently overwriting one
// another. The caller supplies a new ETag for an actual state change.
func (ht *HashTable) UpdateManaged(_ context.Context, link storage.ManagedLink, previousETag string) (storage.ManagedLink, error) {
	if err := validManagedIdentity(link); err != nil {
		return storage.ManagedLink{}, err
	}
	ht.mu.Lock()
	defer ht.mu.Unlock()
	current, ok := ht.records[link.Name]
	if !ok {
		return storage.ManagedLink{}, storage.ErrNotFound
	}
	if current.Owner != link.Owner {
		return storage.ManagedLink{}, storage.ErrUnauthorized
	}
	if previousETag == "" || current.ETag != previousETag || current.UID != link.UID {
		return storage.ManagedLink{}, storage.ErrAborted
	}
	if link.Path != current.Path || link.CreateTime != current.CreateTime {
		return storage.ManagedLink{}, storage.ErrInvalidSource
	}
	stored := link.Clone()
	ht.records[link.Name] = stored
	return stored.Clone(), nil
}

// DeleteManaged conditionally releases the address. A blank expectedETag is
// an unconditional delete, but owner authorization is always enforced.
func (ht *HashTable) DeleteManaged(_ context.Context, name, owner, expectedETag string) error {
	if owner == "" {
		return storage.ErrUnauthorized
	}
	ht.mu.Lock()
	defer ht.mu.Unlock()
	current, ok := ht.records[name]
	if !ok {
		legacy, err := ht.hasLegacyName(name)
		if err != nil {
			return err
		}
		if legacy {
			return storage.ErrUnauthorized
		}
		return storage.ErrNotFound
	}
	if current.Owner != owner {
		return storage.ErrUnauthorized
	}
	if expectedETag != "" && current.ETag != expectedETag {
		return storage.ErrAborted
	}
	delete(ht.records, name)
	return nil
}

// hasLegacyName must be called while holding ht.mu.
func (ht *HashTable) hasLegacyName(name string) (bool, error) {
	for source := range ht.table {
		legacy, err := url.Parse(source)
		if err != nil {
			return false, storage.ErrCorrupt
		}
		path := legacy.EscapedPath()
		if path == "" {
			path = "/"
		}
		legacyName, err := shortlink.ResourceName(legacy.Hostname(), path)
		if err != nil {
			return false, storage.ErrCorrupt
		}
		if legacyName == name {
			return true, nil
		}
	}
	return false, nil
}

func validManagedIdentity(link storage.ManagedLink) error {
	if link.Owner == "" {
		return storage.ErrUnauthorized
	}
	_, path, err := shortlink.ParseResourceName(link.Name)
	if err != nil || path != link.Path {
		return storage.ErrInvalidSource
	}
	return nil
}
