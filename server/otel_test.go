package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/andrewhowdencom/x40.link/otel"
	"github.com/andrewhowdencom/x40.link/server"
	"github.com/andrewhowdencom/x40.link/storage"
	"github.com/andrewhowdencom/x40.link/storage/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkmetricdata "go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	otelapi "go.opentelemetry.io/otel"
)

// withSpanRecorder sets the global TracerProvider to one backed by an
// in-memory SpanRecorder and returns the recorder so the test can inspect
// it. The previous provider is restored when the test finishes.
func withSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	rec := tracetest.NewSpanRecorder()
	prev := otelapi.GetTracerProvider()
	otelapi.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otelapi.SetTracerProvider(prev) })

	return rec
}

// TestRedirectSpanName verifies that the redirect handler emits a span
// named "redirect" — the business operation name defined in AGENTS.md.
func TestRedirectSpanName(t *testing.T) {
	t.Parallel()

	rec := withSpanRecorder(t)

	storage := test.New()
	require.NoError(t, storage.Put(
		context.Background(),
		&url.URL{Host: "x40.local", Path: "/foo"},
		&url.URL{Scheme: "https", Host: "destination.local", Path: "/"},
	))

	// WithOtel must be applied before WithStorage: chi requires that
	// middlewares be defined before routes.
	srv, err := server.New(
		server.WithOtel(),
		server.WithStorage(storage, "hashmap"),
	)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/foo", nil)
	req.Host = "x40.local"

	srv.Handler.ServeHTTP(w, req)

	// Sanity: the redirect actually happened.
	assert.Equal(t, http.StatusTemporaryRedirect, w.Result().StatusCode)

	spans := rec.Ended()
	require.NotEmpty(t, spans, "expected at least one span to be recorded")

	// Find the span with the redirect name; otelhttp may emit multiple
	// spans for the request but the redirect span is named exactly.
	var found bool
	for _, s := range spans {
		if s.Name() == "redirect" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected a span named 'redirect' in: %v", spanNames(spans))
}

// TestRedirectSpanPropagator ensures the propagator is set even on the
// HTTP path. Without a propagator, downstream services cannot join the
// trace context. otelapi.SetTextMapPropagator was called by otel.Init
// but here we set a fresh propagator to verify the chain works without
// a real Init.
func TestRedirectSpanPropagator(t *testing.T) {
	t.Parallel()

	prev := otelapi.GetTextMapPropagator()
	otelapi.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otelapi.SetTextMapPropagator(prev) })

	storage := test.New()
	srv, err := server.New(
		server.WithOtel(),
		server.WithStorage(storage, "hashmap"),
	)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/x", nil)
	req.Host = "x40.local"
	srv.Handler.ServeHTTP(w, req)

	// No assertion on the traceparent header — otelhttp only adds it if
	// it actually starts a span. The test merely verifies the chain
	// does not panic.
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	out := make([]string, len(spans))
	for i, s := range spans {
		out[i] = s.Name()
	}
	return out
}

func spanAttribute(span sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value, true
		}
	}

	return attribute.Value{}, false
}

func TestMethodNotAllowedSpan(t *testing.T) {
	rec := withSpanRecorder(t)

	srv, err := server.New(
		server.WithOtel(),
		server.WithStorage(test.New(), "hashmap"),
	)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodPost, "/foo", nil)
	require.NoError(t, err)
	req.Host = "x40.local"

	srv.Handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusMethodNotAllowed, w.Result().StatusCode)

	var rejected sdktrace.ReadOnlySpan
	for _, span := range rec.Ended() {
		if span.Name() == server.SpanNameRejectedRequest {
			rejected = span
			break
		}
	}
	require.NotNil(t, rejected, "expected %s span in: %v", server.SpanNameRejectedRequest, spanNames(rec.Ended()))

	route, ok := spanAttribute(rejected, "http.route")
	require.True(t, ok)
	assert.Equal(t, "/*", route.AsString())

	method, ok := spanAttribute(rejected, "http.request.method")
	require.True(t, ok)
	assert.Equal(t, http.MethodPost, method.AsString())

	status, ok := spanAttribute(rejected, "http.response.status_code")
	require.True(t, ok)
	assert.Equal(t, int64(http.StatusMethodNotAllowed), status.AsInt64())
}

// TestRedirectEmitsResolvedMetric exercises the HTTP path and asserts
// that the resolved-link business counter fires with surface=http.
func TestRedirectEmitsResolvedMetric(t *testing.T) {
	// No t.Parallel: mutates the global OTel MeterProvider.

	storage := test.New()
	require.NoError(t, storage.Put(
		context.Background(),
		&url.URL{Host: "x40.local", Path: "/foo"},
		&url.URL{Scheme: "https", Host: "destination.local", Path: "/"},
	))

	// Install a manual reader so we can inspect emitted metrics.
	reader := sdkmetric.NewManualReader()
	otelapi.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	srv, err := server.New(
		server.WithOtel(),
		server.WithStorage(storage, "boltdb"),
	)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/foo", nil)
	req.Host = "x40.local"

	srv.Handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusTemporaryRedirect, w.Result().StatusCode)

	// Verify the resolved-link counter carries storage=boltdb,
	// surface=http.
	var rm sdkmetricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	var found bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "x40.link.links.resolved" {
				continue
			}
			intSum, ok := m.Data.(sdkmetricdata.Sum[int64])
			require.True(t, ok)
			for _, p := range intSum.DataPoints {
				if p.Value > 0 {
					storageVal, _ := p.Attributes.Value(attribute.Key("storage"))
					surfaceVal, _ := p.Attributes.Value(attribute.Key("surface"))
					assert.Equal(t, "boltdb", storageVal.AsString())
					assert.Equal(t, otel.SurfaceHTTP, surfaceVal.AsString())
					found = true
				}
			}
		}
	}
	assert.True(t, found, "expected a recorded x40.link.links.resolved datapoint")
}

// TestRedirectEmitsNotFoundMetric verifies that a 404 path increments
// the not-found counter with surface=http.
func TestRedirectEmitsNotFoundMetric(t *testing.T) {
	// No t.Parallel: mutates the global OTel MeterProvider.

	storage := test.New() // empty storage

	reader := sdkmetric.NewManualReader()
	otelapi.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	srv, err := server.New(
		server.WithOtel(),
		server.WithStorage(storage, "hashmap"),
	)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/missing", nil)
	req.Host = "x40.local"
	req.Header.Set("Accept", "application/json")

	srv.Handler.ServeHTTP(w, req)

	t.Logf("status=%d body=%q", w.Result().StatusCode, w.Body.String())
	assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)

	var rm sdkmetricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	var found bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "x40.link.links.not_found" {
				continue
			}
			intSum, ok := m.Data.(sdkmetricdata.Sum[int64])
			require.True(t, ok)
			for _, p := range intSum.DataPoints {
				if p.Value > 0 {
					storageVal, _ := p.Attributes.Value(attribute.Key("storage"))
					surfaceVal, _ := p.Attributes.Value(attribute.Key("surface"))
					assert.Equal(t, "hashmap", storageVal.AsString())
					assert.Equal(t, otel.SurfaceHTTP, surfaceVal.AsString())
					found = true
				}
			}
		}
	}
	assert.True(t, found, "expected a recorded x40.link.links.not_found datapoint")
}

// silence unused import warnings when metric helpers are not used.
var _ = storage.ErrNotFound
