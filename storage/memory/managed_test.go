package memory_test

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/andrewhowdencom/x40.link/storage"
	"github.com/andrewhowdencom/x40.link/storage/memory"
	"github.com/stretchr/testify/require"
)

func managedLink(path, owner, uid, etag, destination string, at time.Time) storage.ManagedLink {
	name, err := shortlink.ResourceName("example.com", "/"+path)
	if err != nil {
		panic(err)
	}
	return storage.ManagedLink{
		Name:           name,
		Path:           "/" + path,
		DestinationURL: destination,
		Owner:          owner,
		UID:            uid,
		ETag:           etag,
		CreateTime:     at,
		UpdateTime:     at,
	}
}

func TestManagedHashTableCreateAndRedirect(t *testing.T) {
	ctx := context.Background()
	store := memory.NewHashTable()
	now := time.Unix(100, 0)
	link := managedLink("foo", "alice", "uid-1", "etag-1", "https://destination.example/a", now)
	created, err := store.CreateManaged(ctx, link, "", "", now)
	require.NoError(t, err)
	require.Equal(t, link, created)

	got, err := store.GetManaged(ctx, link.Name, "alice")
	require.NoError(t, err)
	require.Equal(t, created, got)
	_, err = store.GetManaged(ctx, link.Name, "bob")
	require.ErrorIs(t, err, storage.ErrUnauthorized)

	destination, err := store.Get(ctx, &url.URL{Scheme: "http", Host: "EXAMPLE.COM:8080", Path: "/foo", RawQuery: "ignored=1"})
	require.NoError(t, err)
	require.Equal(t, link.DestinationURL, destination.String())

	_, err = store.CreateManaged(ctx, managedLink("foo", "alice", "uid-2", "etag-2", "https://other.example", now), "", "", now)
	require.ErrorIs(t, err, storage.ErrAlreadyExists)
	_, err = store.CreateManaged(ctx, managedLink("foo", "bob", "uid-3", "etag-3", "https://other.example", now), "", "", now)
	require.ErrorIs(t, err, storage.ErrUnauthorized)
}

func TestManagedHashTablePreservesLegacyLink(t *testing.T) {
	store := memory.NewHashTable()
	ctx := context.Background()
	legacy := &url.URL{Host: "example.com", Path: "/legacy"}
	require.NoError(t, store.Put(ctx, legacy, &url.URL{Scheme: "https", Host: "old.example"}))
	_, err := store.CreateManaged(ctx, managedLink("legacy", "alice", "uid-new", "etag-new", "https://new.example", time.Now()), "", "", time.Now())
	require.ErrorIs(t, err, storage.ErrUnauthorized)
	name, err := shortlink.ResourceName("example.com", "/legacy")
	require.NoError(t, err)
	_, err = store.GetManaged(ctx, name, "alice")
	require.ErrorIs(t, err, storage.ErrUnauthorized)
	require.ErrorIs(t, store.DeleteManaged(ctx, name, "alice", ""), storage.ErrUnauthorized)
	to, err := store.Get(ctx, legacy)
	require.NoError(t, err)
	require.Equal(t, "https://old.example", to.String())
}

func TestManagedHashTableIdempotencyAndExpiry(t *testing.T) {
	ctx := context.Background()
	store := memory.NewHashTable()
	now := time.Unix(100, 0)
	first := managedLink("foo", "alice", "uid-1", "etag-1", "https://destination.example", now)
	created, err := store.CreateManaged(ctx, first, "request-1", "payload-1", now)
	require.NoError(t, err)

	retry := managedLink("different", "alice", "uid-2", "etag-2", "https://destination.example", now)
	got, err := store.CreateManaged(ctx, retry, "request-1", "payload-1", now.Add(23*time.Hour))
	require.NoError(t, err)
	require.Equal(t, created, got)
	_, err = store.CreateManaged(ctx, retry, "request-1", "other-payload", now.Add(time.Hour))
	require.ErrorIs(t, err, storage.ErrRequestIDConflict)
	_, err = store.CreateManaged(ctx, retry, "request-1", "payload-1", now.Add(24*time.Hour))
	require.NoError(t, err)
}

func TestManagedHashTableConditionalLifecycle(t *testing.T) {
	ctx := context.Background()
	store := memory.NewHashTable()
	now := time.Unix(100, 0)
	initial := managedLink("foo", "alice", "uid-1", "etag-1", "https://one.example", now)
	_, err := store.CreateManaged(ctx, initial, "", "", now)
	require.NoError(t, err)

	changed := initial
	changed.DestinationURL = "https://two.example"
	changed.ETag = "etag-2"
	changed.UpdateTime = now.Add(time.Second)
	_, err = store.UpdateManaged(ctx, changed, "stale")
	require.ErrorIs(t, err, storage.ErrAborted)
	_, err = store.UpdateManaged(ctx, changed, initial.ETag)
	require.NoError(t, err)
	_, err = store.UpdateManaged(ctx, changed, initial.ETag)
	require.ErrorIs(t, err, storage.ErrAborted)

	redirect, err := store.Get(ctx, &url.URL{Host: "example.com", Path: "/foo"})
	require.NoError(t, err)
	require.Equal(t, changed.DestinationURL, redirect.String())
	require.ErrorIs(t, store.DeleteManaged(ctx, changed.Name, "bob", changed.ETag), storage.ErrUnauthorized)
	require.ErrorIs(t, store.DeleteManaged(ctx, changed.Name, "alice", initial.ETag), storage.ErrAborted)
	require.NoError(t, store.DeleteManaged(ctx, changed.Name, "alice", changed.ETag))
	_, err = store.Get(ctx, &url.URL{Host: "example.com", Path: "/foo"})
	require.ErrorIs(t, err, storage.ErrNotFound)

	recreated := initial
	recreated.UID = "uid-new"
	recreated.ETag = "etag-new"
	_, err = store.CreateManaged(ctx, recreated, "", "", now.Add(time.Minute))
	require.NoError(t, err)
	require.ErrorIs(t, store.DeleteManaged(ctx, recreated.Name, "alice", changed.ETag), storage.ErrAborted)
}

func TestManagedHashTableConcurrentClaimAndOwnerList(t *testing.T) {
	ctx := context.Background()
	store := memory.NewHashTable()
	now := time.Unix(100, 0)
	first := managedLink("foo", "alice", "uid-1", "etag-1", "https://one.example", now)
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.CreateManaged(ctx, first, "", "", now)
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
			require.True(t, errors.Is(err, storage.ErrAlreadyExists), "unexpected create error: %v", err)
		}
	}
	require.Equal(t, 1, success)

	links, err := store.ListManaged(ctx, "alice", "example.com")
	require.NoError(t, err)
	require.Len(t, links, 1)
	links, err = store.ListManaged(ctx, "bob", "")
	require.NoError(t, err)
	require.Empty(t, links)
}
