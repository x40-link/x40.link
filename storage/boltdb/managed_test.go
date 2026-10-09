package boltdb

import (
	"context"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/andrewhowdencom/x40.link/storage"
	"github.com/stretchr/testify/require"
)

func boltManagedLink(t *testing.T, path, owner, uid, etag, destination string, at time.Time) storage.ManagedLink {
	t.Helper()
	name, err := shortlink.ResourceName("example.com", path)
	require.NoError(t, err)
	return storage.ManagedLink{
		Name: name, Path: path, DestinationURL: destination, Owner: owner,
		UID: uid, ETag: etag, CreateTime: at, UpdateTime: at,
		Annotations: map[string]string{"team": "one"},
	}
}

func TestManagedBoltDBPersistsLifecycleAndRequestID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "links.db")
	db, err := New(path)
	require.NoError(t, err)
	defer func() { _ = db.db.Close() }()
	now := time.Unix(100, 0).UTC()
	initial := boltManagedLink(t, "/foo%2Fbar", "alice", "uid-1", "etag-1", "https://destination.example/one", now)
	created, err := db.CreateManaged(ctx, initial, "request-1", "payload-1", now)
	require.NoError(t, err)
	require.Equal(t, initial, created)
	require.NoError(t, db.db.Close())
	db, err = New(path)
	require.NoError(t, err)

	retained, err := db.GetRequest(ctx, "alice", "request-1", "payload-1", now.Add(23*time.Hour))
	require.NoError(t, err)
	require.Equal(t, created, retained)
	_, err = db.GetRequest(ctx, "alice", "request-1", "other-payload", now.Add(time.Hour))
	require.ErrorIs(t, err, storage.ErrRequestIDConflict)
	_, err = db.GetRequest(ctx, "alice", "request-1", "payload-1", now.Add(24*time.Hour))
	require.ErrorIs(t, err, storage.ErrNotFound)

	_, err = db.GetManaged(ctx, initial.Name, "bob")
	require.ErrorIs(t, err, storage.ErrUnauthorized)
	got, err := db.GetManaged(ctx, initial.Name, "alice")
	require.NoError(t, err)
	require.Equal(t, created, got)
	destination, err := db.Get(ctx, &url.URL{Host: "EXAMPLE.COM", Path: "/foo/bar", RawPath: "/foo%2Fbar", RawQuery: "ignored=1"})
	require.NoError(t, err)
	require.Equal(t, initial.DestinationURL, destination.String())

	changed := initial.Clone()
	changed.DestinationURL = "https://destination.example/two"
	changed.ETag = "etag-2"
	changed.Annotations["team"] = "two"
	changed.UpdateTime = now.Add(time.Minute)
	_, err = db.UpdateManaged(ctx, changed, "stale")
	require.ErrorIs(t, err, storage.ErrAborted)
	_, err = db.UpdateManaged(ctx, changed, initial.ETag)
	require.NoError(t, err)
	redirect, err := db.Get(ctx, &url.URL{Host: "example.com", Path: "/foo/bar", RawPath: "/foo%2Fbar"})
	require.NoError(t, err)
	require.Equal(t, changed.DestinationURL, redirect.String())
	require.ErrorIs(t, db.DeleteManaged(ctx, changed.Name, "alice", initial.ETag), storage.ErrAborted)
	require.NoError(t, db.DeleteManaged(ctx, changed.Name, "alice", changed.ETag))
	_, err = db.Get(ctx, &url.URL{Host: "example.com", Path: "/foo/bar", RawPath: "/foo%2Fbar"})
	require.ErrorIs(t, err, storage.ErrNotFound)

	recreated := initial.Clone()
	recreated.UID = "uid-new"
	recreated.ETag = "etag-new"
	_, err = db.CreateManaged(ctx, recreated, "", "", now.Add(time.Minute))
	require.NoError(t, err)
	require.ErrorIs(t, db.DeleteManaged(ctx, recreated.Name, "alice", changed.ETag), storage.ErrAborted)
	links, err := db.ListManaged(ctx, "alice", "example.com")
	require.NoError(t, err)
	require.Equal(t, []storage.ManagedLink{recreated}, links)
	links, err = db.ListManaged(ctx, "bob", "")
	require.NoError(t, err)
	require.Empty(t, links)
}

func TestManagedBoltDBAtomicClaimAndLegacyCollision(t *testing.T) {
	db, cleanup := newTempDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Unix(100, 0).UTC()
	link := boltManagedLink(t, "/foo", "alice", "uid-1", "etag-1", "https://destination.example", now)
	const writers = 20
	var wg sync.WaitGroup
	results := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := db.CreateManaged(ctx, link, "", "", now)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, storage.ErrAlreadyExists)
		}
	}
	require.Equal(t, 1, success)

	legacy := &url.URL{Host: "example.com", Path: "/legacy"}
	require.NoError(t, db.Put(ctx, legacy, &url.URL{Scheme: "https", Host: "old.example"}))
	legacyLink := boltManagedLink(t, "/legacy", "alice", "uid-2", "etag-2", "https://new.example", now)
	_, err := db.CreateManaged(ctx, legacyLink, "", "", now)
	require.ErrorIs(t, err, storage.ErrUnauthorized)
	_, err = db.GetManaged(ctx, legacyLink.Name, "alice")
	require.ErrorIs(t, err, storage.ErrUnauthorized)
	require.ErrorIs(t, db.DeleteManaged(ctx, legacyLink.Name, "alice", ""), storage.ErrUnauthorized)
	redirect, err := db.Get(ctx, legacy)
	require.NoError(t, err)
	require.Equal(t, "https://old.example", redirect.String())
}
