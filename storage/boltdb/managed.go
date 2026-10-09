package boltdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/andrewhowdencom/x40.link/storage"
	"go.etcd.io/bbolt"
)

var (
	managedBucketName = []byte("managed-short-links")
	requestBucketName = []byte("managed-requests")
)

type retainedRequest struct {
	Fingerprint string
	Result      storage.ManagedLink
	Expires     time.Time
}

var _ storage.ManagedStore = (*BoltDB)(nil)

func (b *BoltDB) CreateManaged(ctx context.Context, link storage.ManagedLink, requestID, fingerprint string, now time.Time) (storage.ManagedLink, error) {
	if err := validManagedIdentity(link); err != nil {
		return storage.ManagedLink{}, err
	}
	if requestID != "" && fingerprint == "" {
		return storage.ManagedLink{}, storage.ErrInvalidSource
	}
	var result storage.ManagedLink
	err := b.db.Update(func(tx *bbolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		records, err := tx.CreateBucketIfNotExists(managedBucketName)
		if err != nil {
			return fmt.Errorf("%w: %v", storage.ErrFailed, err)
		}
		requests, err := tx.CreateBucketIfNotExists(requestBucketName)
		if err != nil {
			return fmt.Errorf("%w: %v", storage.ErrFailed, err)
		}
		if requestID != "" {
			value := requests.Get(requestKey(link.Owner, requestID))
			if value != nil {
				var previous retainedRequest
				if err := json.Unmarshal(value, &previous); err != nil {
					return storage.ErrCorrupt
				}
				if now.Before(previous.Expires) {
					if previous.Fingerprint != fingerprint {
						return storage.ErrRequestIDConflict
					}
					result = previous.Result.Clone()
					return nil
				}
			}
		}
		if value := records.Get([]byte(link.Name)); value != nil {
			var previous storage.ManagedLink
			if err := json.Unmarshal(value, &previous); err != nil {
				return storage.ErrCorrupt
			}
			if previous.Owner != link.Owner {
				return storage.ErrUnauthorized
			}
			return storage.ErrAlreadyExists
		}
		// Legacy records have no owner. Reserve their source addresses until an
		// explicit migration assigns ownership rather than overwriting them.
		legacy, err := legacyClaimed(tx, link.Name)
		if err != nil {
			return err
		}
		if legacy {
			return storage.ErrUnauthorized
		}
		value, err := json.Marshal(link)
		if err != nil {
			return fmt.Errorf("%w: %v", storage.ErrInvalidSource, err)
		}
		if err := records.Put([]byte(link.Name), value); err != nil {
			return fmt.Errorf("%w: %v", storage.ErrFailed, err)
		}
		if requestID != "" {
			retained, err := json.Marshal(retainedRequest{Fingerprint: fingerprint, Result: link, Expires: now.Add(24 * time.Hour)})
			if err != nil {
				return fmt.Errorf("%w: %v", storage.ErrInvalidSource, err)
			}
			if err := requests.Put(requestKey(link.Owner, requestID), retained); err != nil {
				return fmt.Errorf("%w: %v", storage.ErrFailed, err)
			}
		}
		result = link.Clone()
		return nil
	})
	return result, err
}

func (b *BoltDB) GetRequest(ctx context.Context, owner, requestID, fingerprint string, now time.Time) (storage.ManagedLink, error) {
	if owner == "" {
		return storage.ManagedLink{}, storage.ErrUnauthorized
	}
	var result storage.ManagedLink
	err := b.db.View(func(tx *bbolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(requestBucketName)
		if bucket == nil || bucket.Get(requestKey(owner, requestID)) == nil {
			return storage.ErrNotFound
		}
		var previous retainedRequest
		if err := json.Unmarshal(bucket.Get(requestKey(owner, requestID)), &previous); err != nil {
			return storage.ErrCorrupt
		}
		if !now.Before(previous.Expires) {
			return storage.ErrNotFound
		}
		if previous.Fingerprint != fingerprint {
			return storage.ErrRequestIDConflict
		}
		result = previous.Result.Clone()
		return nil
	})
	return result, err
}

func (b *BoltDB) GetManaged(ctx context.Context, name, owner string) (storage.ManagedLink, error) {
	if owner == "" {
		return storage.ManagedLink{}, storage.ErrUnauthorized
	}
	var result storage.ManagedLink
	err := b.db.View(func(tx *bbolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(managedBucketName)
		if bucket == nil || bucket.Get([]byte(name)) == nil {
			legacy, err := legacyClaimed(tx, name)
			if err != nil {
				return err
			}
			if legacy {
				return storage.ErrUnauthorized
			}
			return storage.ErrNotFound
		}
		if err := json.Unmarshal(bucket.Get([]byte(name)), &result); err != nil {
			return storage.ErrCorrupt
		}
		if result.Owner != owner {
			return storage.ErrUnauthorized
		}
		return nil
	})
	return result.Clone(), err
}

func (b *BoltDB) ListManaged(ctx context.Context, owner, domain string) ([]storage.ManagedLink, error) {
	if owner == "" {
		return nil, storage.ErrUnauthorized
	}
	links := make([]storage.ManagedLink, 0)
	err := b.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(managedBucketName)
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(_, value []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			var link storage.ManagedLink
			if err := json.Unmarshal(value, &link); err != nil {
				return storage.ErrCorrupt
			}
			if link.Owner != owner {
				return nil
			}
			if domain != "" && !strings.HasPrefix(link.Name, "domains/"+domain+"/shortLinks/") {
				return nil
			}
			links = append(links, link.Clone())
			return nil
		})
	})
	sort.Slice(links, func(i, j int) bool { return links[i].Name < links[j].Name })
	return links, err
}

func (b *BoltDB) UpdateManaged(ctx context.Context, link storage.ManagedLink, previousETag string) (storage.ManagedLink, error) {
	if err := validManagedIdentity(link); err != nil {
		return storage.ManagedLink{}, err
	}
	err := b.db.Update(func(tx *bbolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(managedBucketName)
		if bucket == nil || bucket.Get([]byte(link.Name)) == nil {
			return storage.ErrNotFound
		}
		var current storage.ManagedLink
		if err := json.Unmarshal(bucket.Get([]byte(link.Name)), &current); err != nil {
			return storage.ErrCorrupt
		}
		if current.Owner != link.Owner {
			return storage.ErrUnauthorized
		}
		if previousETag == "" || current.ETag != previousETag || current.UID != link.UID {
			return storage.ErrAborted
		}
		if current.Path != link.Path || current.CreateTime != link.CreateTime {
			return storage.ErrInvalidSource
		}
		value, err := json.Marshal(link)
		if err != nil {
			return fmt.Errorf("%w: %v", storage.ErrInvalidSource, err)
		}
		return bucket.Put([]byte(link.Name), value)
	})
	return link.Clone(), err
}

func (b *BoltDB) DeleteManaged(ctx context.Context, name, owner, expectedETag string) error {
	if owner == "" {
		return storage.ErrUnauthorized
	}
	return b.db.Update(func(tx *bbolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(managedBucketName)
		if bucket == nil || bucket.Get([]byte(name)) == nil {
			legacy, err := legacyClaimed(tx, name)
			if err != nil {
				return err
			}
			if legacy {
				return storage.ErrUnauthorized
			}
			return storage.ErrNotFound
		}
		var current storage.ManagedLink
		if err := json.Unmarshal(bucket.Get([]byte(name)), &current); err != nil {
			return storage.ErrCorrupt
		}
		if current.Owner != owner {
			return storage.ErrUnauthorized
		}
		if expectedETag != "" && current.ETag != expectedETag {
			return storage.ErrAborted
		}
		return bucket.Delete([]byte(name))
	})
}

func requestKey(owner, requestID string) []byte { return []byte(owner + "\x00" + requestID) }

func legacyClaimed(tx *bbolt.Tx, name string) (bool, error) {
	bucket := tx.Bucket(txBucketName)
	if bucket == nil {
		return false, nil
	}
	claimed := false
	err := bucket.ForEach(func(key, _ []byte) error {
		oldURL, err := url.Parse(string(key))
		if err != nil {
			return storage.ErrCorrupt
		}
		path := oldURL.EscapedPath()
		if path == "" {
			path = "/"
		}
		legacyName, err := shortlink.ResourceName(oldURL.Hostname(), path)
		if err != nil {
			return storage.ErrCorrupt
		}
		if legacyName == name {
			claimed = true
		}
		return nil
	})
	return claimed, err
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
