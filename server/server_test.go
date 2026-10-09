package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/andrewhowdencom/x40.link/server"
	"github.com/andrewhowdencom/x40.link/storage/test"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServer_WithBadOption(t *testing.T) {
	t.Parallel()

	_, err := server.New(func(_ *http.Server) error {
		return errors.New("i am bad")
	})

	assert.ErrorIs(t, err, server.ErrFailedToApplyOption)
}

func TestNewServer_WithListenAddress(t *testing.T) {
	t.Parallel()

	srv, err := server.New(server.WithListenAddress("0.0.0.0:1234"))

	assert.Equal(t, "0.0.0.0:1234", srv.Addr)
	assert.Nil(t, err)
}

func TestNewServer_WithDefaults(t *testing.T) {
	t.Parallel()

	srv, err := server.New()
	assert.Nil(t, err)

	mux, ok := srv.Handler.(*chi.Mux)
	assert.True(t, ok)

	// This is a weak test, but not sure yet how to validate this
	assert.Len(t, mux.Middlewares(), 2)
}

func TestNewServer_WithMiddleware(t *testing.T) {
	t.Parallel()

	iWasInvoked := false

	srv, err := server.New(server.WithMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			iWasInvoked = true

			next.ServeHTTP(w, r)
		})
	}))

	assert.Nil(t, err)

	// Create a path so the request actually gets routed somewhere
	c := srv.Handler.(*chi.Mux)
	c.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Yeah!"))
	})

	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "/", nil)

	srv.Handler.ServeHTTP(w, r)

	assert.True(t, iWasInvoked)
	assert.Equal(t, []byte("Yeah!"), w.Body.Bytes())
}

// WithStorage is a more extensive test as it binds a slug, rather than just modifying the state of http.Request
func TestNewServer_WithStorage(t *testing.T) {
	t.Parallel()

	storage := test.New()
	err := storage.Put(context.Background(), &url.URL{
		Host: "test",
		Path: "/foo",
	},
		&url.URL{
			Host: "test",
			Path: "/bar",
		},
	)
	assert.Nil(t, err)

	srv, err := server.New(server.WithStorage(storage, "hashmap"))
	assert.Nil(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/foo", nil)
	req.Host = "test"

	srv.Handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusTemporaryRedirect, w.Result().StatusCode)
	assert.Equal(t, "//test/bar", w.Header().Get("Location"))
}

func TestGatewayRoutePrecedesRedirect(t *testing.T) {
	store := test.New()
	srv, err := server.New(
		server.WithGateway(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1alpha/domains/example.com/shortLinks", r.URL.Path)
			w.WriteHeader(http.StatusAccepted)
		})),
		server.WithStorage(store, "hashmap"),
	)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/v1alpha/domains/example.com/shortLinks", nil)
	req.Host = "example.com"
	response := httptest.NewRecorder()
	srv.Handler.ServeHTTP(response, req)
	require.Equal(t, http.StatusAccepted, response.Code)
}

func TestNewServer_WithH2CConcurrentRequests(t *testing.T) {
	rec := withSpanRecorder(t)

	storage := test.New()
	require.NoError(t, storage.Put(context.Background(), &url.URL{
		Host: "test",
		Path: "/foo",
	}, &url.URL{
		Scheme: "https",
		Host:   "example.com",
	}))

	srv, err := server.New(
		server.WithH2C(),
		server.WithOtel(),
		server.WithStorage(storage, "hashmap"),
	)
	require.NoError(t, err)

	ts := httptest.NewUnstartedServer(srv.Handler)
	ts.Config.ConnContext = srv.ConnContext
	ts.Start()
	t.Cleanup(ts.Close)

	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	t.Cleanup(transport.CloseIdleConnections)

	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	const requests = 256
	type result struct {
		status int
		proto  int
		allow  []string
		err    error
	}

	start := make(chan struct{})
	results := make(chan result, requests)
	var wg sync.WaitGroup

	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/foo", nil)
			if reqErr != nil {
				results <- result{err: reqErr}
				return
			}
			req.Host = "test"

			resp, doErr := client.Do(req)
			if doErr != nil {
				results <- result{err: doErr}
				return
			}
			closeErr := resp.Body.Close()

			results <- result{
				status: resp.StatusCode,
				proto:  resp.ProtoMajor,
				allow:  resp.Header.Values("Allow"),
				err:    closeErr,
			}
		}()
	}

	close(start)
	wg.Wait()
	close(results)

	for got := range results {
		require.NoError(t, got.err)
		assert.Equal(t, 2, got.proto)
		assert.Equal(t, http.StatusTemporaryRedirect, got.status)
		assert.Empty(t, got.allow)
	}

	connectionRequests := map[int64]int{}
	redirects := 0
	for _, span := range rec.Ended() {
		if span.Name() != server.SpanNameRedirect {
			continue
		}
		redirects++

		route, ok := spanAttribute(span, "http.route")
		require.True(t, ok)
		assert.Equal(t, "/*", route.AsString())

		protocol, ok := spanAttribute(span, "network.protocol.version")
		require.True(t, ok)
		assert.Equal(t, "2.0", protocol.AsString())

		connectionID, ok := spanAttribute(span, server.TraceAttributeServerConnectionID)
		require.True(t, ok)
		connectionRequests[connectionID.AsInt64()]++
	}

	assert.Equal(t, requests, redirects)
	// HTTP/2 may open an additional connection for a single request under
	// load. Fewer connections than requests still proves connection reuse.
	assert.Less(t, len(connectionRequests), requests)
}
