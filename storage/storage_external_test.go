package storage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/andrewhowdencom/x40.link/server"
	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/andrewhowdencom/x40.link/storage"
	storer "github.com/andrewhowdencom/x40.link/storage/firestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// pids is a registry of processes that need to be shutdown. Used primarily for the firebase emulator.
var pids = struct {
	mu sync.Mutex
	m  map[string]*exec.Cmd
}{
	m: make(map[string]*exec.Cmd),
}

// Factories to generate valid storage engines
var externalSinkFactories = map[string]func(string) storage.Storer{
	"firestore": func(s string) storage.Storer {
		// Start the firebase emulator
		cmd := exec.Command(
			"gcloud",
			"emulators",
			"firestore",
			"start",
			"--host-port=localhost:8500",
		)

		// Set process group ID
		// See https://medium.com/@felixge/killing-a-child-process-and-all-of-its-children-in-go-54079af94773
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		// Save the STDOUT & STDERR
		cmd.Stdout = &bytes.Buffer{}
		cmd.Stderr = &bytes.Buffer{}

		if err := cmd.Start(); err != nil {
			panic(err)
		}

		// Wait for firestore to come up.
		ctx, cxl := context.WithTimeout(context.Background(), time.Second*60)
		defer cxl()

		ticker := time.NewTicker(time.Second * 1)

		i := 0
	Wait:
		for {
			i++

			select {
			case <-ctx.Done():
				log.Println("command output")
				log.Println(cmd.Stdout.(*bytes.Buffer).String())
				log.Println(cmd.Stderr.(*bytes.Buffer).String())

				panic("waited for firebase to come up, but it did not")
			case <-ticker.C:
				conn, err := net.DialTimeout("tcp", "localhost:8500", time.Millisecond*500)
				if err != nil {
					log.Printf("tried to connect; failed: %s. attempt %d", err.Error(), i)
				} else {
					// Error ignored as this is a test, and it will be shutdown anyway.
					_ = conn.Close()
					log.Println("connection succeeded")
					break Wait
				}
			}
		}

		// It takes a second for firestore to come up.
		time.Sleep(time.Second * 5)

		pids.mu.Lock()
		defer pids.mu.Unlock()

		pids.m[s] = cmd

		conn, err := grpc.NewClient("localhost:8500", grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			panic(err)
		}

		client, err := firestore.NewClient(context.Background(), "andrewhowdencom", option.WithGRPCConn(conn))
		if err != nil {
			panic(err)
		}

		// Bootstrap the client
		return &storer.Firestore{
			Client: client,
		}
	},
}

// Factories to tear down valid storage engines
var externalSinkTeardown = map[string]func(string){
	"firestore": func(s string) {
		if err := syscall.Kill(-pids.m[s].Process.Pid, syscall.SIGINT); err != nil {
			panic(err)
		}
	},
}

// TestComplianceAll tests that the storages actually store and retrieve valid records in the (simplest) expected ways.
func TestComplianceExternalAll(t *testing.T) {
	for n, f := range externalSinkFactories {
		f := f
		n := n

		t.Run(n, func(t *testing.T) {
			str := f("compliance")
			defer externalSinkTeardown[n]("compliance")

			// Query for a record that doesn't exit, to ensure the data store will not panic.
			_, err := str.Get(context.Background(), &url.URL{Host: "x40"})

			assert.ErrorIs(t, err, storage.ErrNotFound)

			// Insert and query a record.
			assert.Nil(t, str.Put(context.Background(), &url.URL{Host: "x40"}, &url.URL{Host: "andrewhowden.com"}))

			res, err := str.Get(context.Background(), &url.URL{
				Host: "x40",
			})

			assert.Nil(t, err)
			assert.Equal(t, &url.URL{
				Host: "andrewhowden.com",
			}, res)
			managed, ok := str.(storage.ManagedStore)
			if !ok {
				t.Fatal("writable backend does not implement ManagedStore")
			}
			unownedName, err := shortlink.ResourceName("x40", "/")
			require.NoError(t, err)
			_, err = managed.GetManaged(context.Background(), unownedName, "alice")
			require.ErrorIs(t, err, storage.ErrUnauthorized)
			require.ErrorIs(t, managed.DeleteManaged(context.Background(), unownedName, "alice", ""), storage.ErrUnauthorized)
			name, err := shortlink.ResourceName("example.com", "/foo%2Fbar")
			require.NoError(t, err)
			now := time.Now().UTC().Truncate(time.Second)
			link := storage.ManagedLink{Name: name, Path: "/foo%2Fbar", DestinationURL: "https://destination.example/one", Owner: "alice", UID: "uid-1", ETag: "etag-1", CreateTime: now, UpdateTime: now}
			created, err := managed.CreateManaged(context.Background(), link, "request-1", "fingerprint-1", now)
			require.NoError(t, err)
			require.Equal(t, link, created)
			retried, err := managed.GetRequest(context.Background(), "alice", "request-1", "fingerprint-1", now.Add(time.Hour))
			require.NoError(t, err)
			require.Equal(t, link, retried)
			_, err = managed.GetManaged(context.Background(), name, "bob")
			require.ErrorIs(t, err, storage.ErrUnauthorized)
			read, err := managed.GetManaged(context.Background(), name, "alice")
			require.NoError(t, err)
			require.Equal(t, link, read)
			to, err := str.Get(context.Background(), &url.URL{Host: "EXAMPLE.COM", Path: "/foo/bar", RawPath: "/foo%2Fbar"})
			require.NoError(t, err)
			require.Equal(t, link.DestinationURL, to.String())
			updated := link.Clone()
			updated.DestinationURL = "https://destination.example/two"
			updated.UpdateTime = now.Add(time.Minute)
			updated.ETag = "etag-2"
			_, err = managed.UpdateManaged(context.Background(), updated, link.ETag)
			require.NoError(t, err)
			links, err := managed.ListManaged(context.Background(), "alice", "example.com")
			require.NoError(t, err)
			require.Equal(t, []storage.ManagedLink{updated}, links)
			require.ErrorIs(t, managed.DeleteManaged(context.Background(), name, "alice", link.ETag), storage.ErrAborted)
			require.NoError(t, managed.DeleteManaged(context.Background(), name, "alice", updated.ETag))
			_, err = str.Get(context.Background(), &url.URL{Host: "example.com", Path: "/foo/bar", RawPath: "/foo%2Fbar"})
			require.ErrorIs(t, err, storage.ErrNotFound)
			legacyCtx := context.WithValue(context.Background(), storage.CtxKeyAgent, "alice")
			legacySource := &url.URL{Host: "example.com", Path: "/existing"}
			require.NoError(t, str.Put(legacyCtx, legacySource, &url.URL{Scheme: "https", Host: "existing.example"}))
			legacyName, err := shortlink.ResourceName("example.com", "/existing")
			require.NoError(t, err)
			legacyManaged, err := managed.GetManaged(context.Background(), legacyName, "alice")
			require.NoError(t, err)
			require.Equal(t, "https://existing.example", legacyManaged.DestinationURL)
			require.NotEmpty(t, legacyManaged.UID)
			require.NotEmpty(t, legacyManaged.ETag)
			updatedLegacy := legacyManaged.Clone()
			updatedLegacy.DestinationURL = "https://updated-existing.example"
			updatedLegacy.UpdateTime = time.Now().UTC()
			updatedLegacy.ETag = "updated-legacy-etag"
			_, err = managed.UpdateManaged(context.Background(), updatedLegacy, legacyManaged.ETag)
			require.NoError(t, err)
			legacyRedirect, err := str.Get(context.Background(), legacySource)
			require.NoError(t, err)
			require.Equal(t, updatedLegacy.DestinationURL, legacyRedirect.String())
			legacyClaim := link.Clone()
			legacyClaim.Name, legacyClaim.Path = legacyName, "/existing"
			_, err = managed.CreateManaged(context.Background(), legacyClaim, "", "", now)
			require.ErrorIs(t, err, storage.ErrAlreadyExists)
			_, err = managed.CreateManaged(context.Background(), link, "", "", now)
			require.NoError(t, err)
			concurrentName, err := shortlink.ResourceName("example.com", "/concurrent")
			require.NoError(t, err)
			concurrent := link.Clone()
			concurrent.Name, concurrent.Path = concurrentName, "/concurrent"
			const writers = 8
			results := make(chan error, writers)
			for i := 0; i < writers; i++ {
				go func() {
					_, err := managed.CreateManaged(context.Background(), concurrent, "", "", now)
					results <- err
				}()
			}
			successes := 0
			for i := 0; i < writers; i++ {
				err := <-results
				if err == nil {
					successes++
				} else {
					require.ErrorIs(t, err, storage.ErrAlreadyExists)
				}
			}
			require.Equal(t, 1, successes)
		})
	}
}

func TestOwnership(t *testing.T) {
	for _, k := range []string{
		"firestore",
	} {
		k := k
		t.Run(k, func(t *testing.T) {
			str := externalSinkFactories[k]("ownership")
			defer externalSinkTeardown[k]("ownership")

			auth, isAuthenticator := str.(storage.Authenticator)

			assert.Truef(t, isAuthenticator, "supplied storer does not authenticate")

			// Write the context into the store
			ownerCtx := context.WithValue(context.Background(), storage.CtxKeyAgent, "email:user1@example.com")
			thiefCtx := context.WithValue(context.Background(), storage.CtxKeyAgent, "email:user2@example.com")

			// Write a record into the datastore
			err := str.Put(ownerCtx, &url.URL{Host: "x40"}, &url.URL{Host: "x40"})
			assert.Nil(t, err)

			assert.True(t, auth.Owns(ownerCtx, &url.URL{Host: "x40"}))
			assert.False(t, auth.Owns(thiefCtx, &url.URL{Host: "x40"}))

			// Try and update the record as the thief
			assert.ErrorIs(
				t,
				str.Put(thiefCtx, &url.URL{Host: "x40"}, &url.URL{Host: "x40"}),
				storage.ErrUnauthorized,
			)

			// Creation cannot overwrite a link, even for its owner.
			assert.ErrorIs(t, str.Put(ownerCtx, &url.URL{Host: "x40"}, &url.URL{Host: "40x"}), storage.ErrAlreadyExists)

			// Allow all users to read URLs
			to, err := str.Get(thiefCtx, &url.URL{Host: "x40"})
			assert.Nil(t, err)
			assert.Equal(t, to.String(), (&url.URL{Host: "x40"}).String())

			// Do not allow anonymous users to update the record
			assert.ErrorIs(t,
				str.Put(context.Background(), &url.URL{Host: "x40"}, &url.URL{Host: "x40"}),
				storage.ErrUnauthorized,
			)
		})
	}
}

func TestFirestoreList(t *testing.T) {
	store := externalSinkFactories["firestore"]("list")
	defer externalSinkTeardown["firestore"]("list")

	owner := context.WithValue(context.Background(), storage.CtxKeyAgent, "sub:owner")
	other := context.WithValue(context.Background(), storage.CtxKeyAgent, "sub:other")
	for _, item := range []struct {
		ctx  context.Context
		from string
	}{
		{owner, "//a.example"},
		{owner, "//a.example/one"},
		{owner, "//a.example/plus+sign"},
		{owner, "//b.example/two"},
		{other, "//a.example/other"},
	} {
		from, err := url.Parse(item.from)
		assert.NoError(t, err)
		assert.NoError(t, store.Put(item.ctx, from, &url.URL{Host: "target.example"}))
	}

	all, err := store.List(owner, "")
	assert.NoError(t, err)
	assert.Equal(t, []string{"//a.example/", "//a.example/one", "//a.example/plus+sign", "//b.example/two"}, sources(all))

	filtered, err := store.List(owner, "a.example")
	assert.NoError(t, err)
	assert.Equal(t, []string{"//a.example/", "//a.example/one", "//a.example/plus+sign"}, sources(filtered))

	_, err = store.List(context.Background(), "")
	assert.ErrorIs(t, err, storage.ErrUnauthorized)
}

func TestFirestorePathIdentityAndAtomicCreation(t *testing.T) {
	store := externalSinkFactories["firestore"]("identity")
	defer externalSinkTeardown["firestore"]("identity")
	paths := []string{"/foo/bar", "/foo+bar", "/foo%2Fbar", "/foo//bar", "/Foo", "/foo", "/", "/%2F"}
	for _, reverse := range []bool{false, true} {
		for _, differentOwners := range []bool{false, true} {
			domain := fmt.Sprintf("identity-%t-%t.example", reverse, differentOwners)
			srv, err := server.New(server.WithStorage(store, "firestore"))
			require.NoError(t, err)
			for index := range paths {
				i := index
				if reverse {
					i = len(paths) - 1 - index
				}
				owner := "owner"
				if differentOwners {
					owner = fmt.Sprintf("owner-%d", i)
				}
				ctx := context.WithValue(context.Background(), storage.CtxKeyAgent, owner)
				from, err := url.Parse("https://" + domain + paths[i])
				require.NoError(t, err)
				to, err := url.Parse(fmt.Sprintf("https://destination.example/%d", i))
				require.NoError(t, err)
				require.NoError(t, store.Put(ctx, from, to))
			}
			for i, p := range paths {
				from, err := url.Parse("https://" + domain + p)
				require.NoError(t, err)
				want := fmt.Sprintf("https://destination.example/%d", i)
				to, err := store.Get(context.Background(), from)
				require.NoError(t, err)
				require.Equal(t, want, to.String())
				recorder := httptest.NewRecorder()
				srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, from.String(), nil))
				require.Equal(t, http.StatusTemporaryRedirect, recorder.Code)
				require.Equal(t, want, recorder.Header().Get("Location"))
			}
		}
	}
	from := &url.URL{Host: "concurrent.example", Path: "/same"}
	const creators = 16
	type result struct {
		destination string
		err         error
	}
	results := make(chan result, creators)
	start := make(chan struct{})
	for i := range creators {
		go func(i int) {
			<-start
			to := &url.URL{Scheme: "https", Host: "destination.example", Path: fmt.Sprintf("/%d", i)}
			ctx := context.WithValue(context.Background(), storage.CtxKeyAgent, "same-owner")
			results <- result{to.String(), store.Put(ctx, from, to)}
		}(i)
	}
	close(start)
	var winner string
	successes := 0
	for range creators {
		r := <-results
		if r.err == nil {
			winner = r.destination
			successes++
		} else {
			require.ErrorIs(t, r.err, storage.ErrAlreadyExists)
		}
	}
	require.Equal(t, 1, successes)
	to, err := store.Get(context.Background(), from)
	require.NoError(t, err)
	require.Equal(t, winner, to.String())
}

func TestFirestoreOfflineMigration(t *testing.T) {
	store := externalSinkFactories["firestore"]("migration").(*storer.Firestore)
	defer externalSinkTeardown["firestore"]("migration")
	ctx := context.Background()
	originals := map[string]map[string]interface{}{
		"links/example.com":                  {},
		"links/root.example":                 {"to": "https://root.example", "owner": "owner"},
		"links/example.com/id/+foo+bar":      {"to": "https://slash.example", "owner": "owner"},
		"links/example.com/id/foo+bar":       {"to": "https://slash.example", "owner": "owner"},
		"links/example.com/id/+literal+plus": {"to": "https://plus.example", "owner": "owner", "from": "//example.com/literal+plus"},
		"links/example.com/id/+empty":        {"to": "", "owner": ""},
	}
	for path, data := range originals {
		_, err := store.Client.Doc(path).Set(ctx, data)
		require.NoError(t, err)
	}
	plan, err := store.PlanMigration(ctx)
	require.NoError(t, err)
	require.Len(t, plan.Entries, 4)
	require.Equal(t, 1, plan.IdenticalDuplicates)
	// Exercise the exact JSON backup round trip used by the migration command.
	backup, err := json.Marshal(plan)
	require.NoError(t, err)
	var restored storer.MigrationPlan
	require.NoError(t, json.Unmarshal(backup, &restored))
	require.NoError(t, store.ApplyMigration(ctx, plan))
	_, err = store.Client.Doc(plan.Entries[0].Target).Set(ctx, map[string]interface{}{"to": "changed", "owner": "owner"})
	require.NoError(t, err)
	require.Error(t, store.RollbackMigration(ctx, &restored))
	_, err = store.Client.Doc(plan.Entries[0].Target).Set(ctx, plan.Entries[0].Data)
	require.NoError(t, err)
	for source, want := range map[string]string{"//example.com/foo/bar": "https://slash.example", "//example.com/literal+plus": "https://plus.example", "//root.example/": "https://root.example"} {
		from, err := url.Parse(source)
		require.NoError(t, err)
		to, err := store.Get(ctx, from)
		require.NoError(t, err)
		require.Equal(t, want, to.String())
	}
	next, err := store.PlanMigration(ctx)
	require.NoError(t, err)
	require.Empty(t, next.Entries)
	require.NoError(t, store.RollbackMigration(ctx, &restored))
	require.NoError(t, store.RollbackMigration(ctx, &restored))
	for path, data := range originals {
		snap, err := store.Client.Doc(path).Get(ctx)
		require.NoError(t, err)
		require.Equal(t, data, snap.Data())
	}
	stale, err := store.PlanMigration(ctx)
	require.NoError(t, err)
	_, err = store.Client.Doc(stale.Entries[0].Source.Path).Set(ctx, map[string]interface{}{"to": "changed", "owner": "owner"})
	require.NoError(t, err)
	require.Error(t, store.ApplyMigration(ctx, stale))
	_, err = store.Get(ctx, &url.URL{Host: "example.com", Path: "/empty"})
	require.True(t, errors.Is(err, storage.ErrNotFound))
}
