// Package boltdb implements storage based on the BoltDB database, backed by the filesystem.
package boltdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/andrewhowdencom/x40.link/storage"
	"go.etcd.io/bbolt"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Err* are sentinel errors
var (
	ErrFailedToSetupDatabase = errors.New("failed to setup backing database")
	ErrFailedToTX            = errors.New("failed to complete database transaction")
	ErrDataCorrupt           = errors.New("data returned from the database corrupted")

	txBucketName = []byte("short-links")
)

// tracer is the package's OTel tracer. When no TracerProvider is set,
// this returns a no-op tracer.
func tracer() oteltrace.Tracer { return otel.Tracer("x40.link/boltdb") }

// Span name constants. Exported so tests and dashboards can reference
// them by name without depending on internal strings.
const (
	SpanNameStorageLookup = "storage.lookup"
	SpanNameStorageWrite  = "storage.write"
	SpanNameStorageList   = "storage.list"
)

// Option modifies the bolt options, allowing the user to set some property of the database.
type Option func(db *bbolt.Options) error

// BoltDB is an implementation of the link shortener that stores links in the
// boltdb storage engine by CoreOS (later, etcd-io):
//
// * https://github.com/etcd-io/bbolt
//
// Not something I've used a lot, so YMMV.
type BoltDB struct {
	db *bbolt.DB
}

// New creates a new BoltDB backed storage implementation.
func New(path string, opts ...Option) (*BoltDB, error) {
	// The initial options are derived from bbolt.DefaultOptions, with the timeout applied so it does not
	// lock forever.
	bopts := &bbolt.Options{
		Timeout:      1 * time.Second,
		NoGrowSync:   false,
		FreelistType: bbolt.FreelistArrayType,
	}

	// Allow users to override the underlying database options. While this exposes this, this implementation
	// is only a thin wrapper around BoltDB to handle the semantics of URLs.
	for _, o := range opts {
		if err := o(bopts); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrFailedToSetupDatabase, err)
		}
	}

	n, err := bbolt.Open(path, 0600, bopts)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrFailedToSetupDatabase, err)
	}

	return &BoltDB{
		db: n,
	}, nil
}

// WithFileLockWait modifies the timeout that Unix based operating systems will wait for the file lock
// to be freed.
func WithFileLockWait(dur time.Duration) Option {
	return func(db *bbolt.Options) error {
		db.Timeout = dur

		return nil
	}
}

// Get returns a URL, given another input URL
func (b *BoltDB) Get(ctx context.Context, in *url.URL) (*url.URL, error) {
	_, span := tracer().Start(ctx, SpanNameStorageLookup, oteltrace.WithAttributes(
		attribute.String("db.system", "boltdb"),
		attribute.String("db.operation", "read"),
	))
	defer span.End()

	var u *url.URL

	err := b.db.View(func(tx *bbolt.Tx) error {
		path := in.EscapedPath()
		if path == "" {
			path = "/"
		}
		name, nameErr := shortlink.ResourceName(in.Hostname(), path)
		if nameErr == nil {
			if managed := tx.Bucket(managedBucketName); managed != nil {
				if value := managed.Get([]byte(name)); value != nil {
					var link storage.ManagedLink
					if err := json.Unmarshal(value, &link); err != nil {
						return storage.ErrCorrupt
					}
					var err error
					u, err = url.Parse(link.DestinationURL)
					if err != nil {
						return storage.ErrCorrupt
					}
					return nil
				}
			}
		}
		// If there's no bucket created, no put operations can have been run. Ergo, the key cannot exist.
		b := tx.Bucket(txBucketName)
		if b == nil {
			return storage.ErrNotFound
		}

		v := b.Get([]byte(in.String()))
		if v == nil {
			return storage.ErrNotFound
		}

		var err error
		u, err = url.Parse(string(v))
		if err != nil {
			return ErrDataCorrupt
		}

		return nil
	})
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	return u, nil
}

// Put saves a URL to the datastore
func (b *BoltDB) Put(ctx context.Context, f *url.URL, t *url.URL) error {
	_, span := tracer().Start(ctx, SpanNameStorageWrite, oteltrace.WithAttributes(
		attribute.String("db.system", "boltdb"),
		attribute.String("db.operation", "write"),
	))
	defer span.End()

	err := b.db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(txBucketName)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrFailedToTX, err)
		}

		if err := b.Put([]byte(f.String()), []byte(t.String())); err != nil {
			return fmt.Errorf("%w: %s", ErrFailedToTX, err)
		}

		return nil
	})
	if err != nil {
		span.RecordError(err)
		return err
	}

	return nil
}

// List returns every matching link. BoltDB does not store ownership, so the
// caller's identity does not change the result.
func (b *BoltDB) List(ctx context.Context, domain string) ([]storage.Link, error) {
	_, span := tracer().Start(ctx, SpanNameStorageList, oteltrace.WithAttributes(
		attribute.String("db.system", "boltdb"),
		attribute.String("db.operation", "read"),
	))
	defer span.End()

	links := make([]storage.Link, 0)
	err := b.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(txBucketName)
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(key, value []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			from, err := url.Parse(string(key))
			if err != nil {
				return ErrDataCorrupt
			}
			if domain != "" && from.Host != domain {
				return nil
			}
			to, err := url.Parse(string(value))
			if err != nil {
				return ErrDataCorrupt
			}
			links = append(links, storage.Link{From: from, To: to})
			return nil
		})
	})
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	storage.SortLinks(links)
	return links, nil
}
