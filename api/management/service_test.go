package management_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/andrewhowdencom/x40.link/api/management"
	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/andrewhowdencom/x40.link/storage"
	"github.com/andrewhowdencom/x40.link/storage/memory"
	"github.com/stretchr/testify/require"
	v1alpha "github.com/x40-link/api/gen/x40/link/v1alpha"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func caller(owner string) context.Context {
	return context.WithValue(context.Background(), storage.CtxKeyAgent, owner)
}

func name(t *testing.T, domain, path string) string {
	t.Helper()
	value, err := shortlink.ResourceName(domain, path)
	require.NoError(t, err)
	return value
}

func TestLifecycleAndOwnerAuthorization(t *testing.T) {
	store := memory.NewHashTable()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	svc := management.New(store, management.WithClock(func() time.Time { return now }))
	alice, bob := caller("alice"), caller("bob")
	path := "/foo%2Fbar"
	created, err := svc.CreateShortLink(alice, &v1alpha.CreateShortLinkRequest{
		Parent: "domains/EXAMPLE.COM",
		ShortLink: &v1alpha.ShortLink{
			Name:           "ignored-on-create",
			Path:           &path,
			DestinationUrl: "https://destination.example/one",
			Annotations:    map[string]string{"team": "alpha"},
			Uid:            "ignored-on-create",
		},
	})
	require.NoError(t, err)
	require.Equal(t, name(t, "example.com", path), created.Name)
	require.Equal(t, path, created.GetPath())
	require.Equal(t, "https://example.com"+path, created.ShortUrl)
	require.NotEmpty(t, created.Uid)
	require.NotEqual(t, "ignored-on-create", created.Uid)
	require.True(t, strings.HasPrefix(created.Etag, "\"") && strings.HasSuffix(created.Etag, "\""))
	require.Equal(t, now, created.CreateTime.AsTime())
	require.Equal(t, now, created.UpdateTime.AsTime())

	got, err := svc.GetShortLink(alice, &v1alpha.GetShortLinkRequest{Name: created.Name})
	require.NoError(t, err)
	require.Equal(t, created, got)
	_, err = svc.GetShortLink(bob, &v1alpha.GetShortLinkRequest{Name: created.Name})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = svc.GetShortLink(context.Background(), &v1alpha.GetShortLinkRequest{Name: created.Name})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	redirect, err := store.Get(context.Background(), &url.URL{Scheme: "http", Host: "EXAMPLE.COM", Path: "/foo/bar", RawPath: path, RawQuery: "ignored=1"})
	require.NoError(t, err)
	require.Equal(t, created.DestinationUrl, redirect.String())

	now = now.Add(time.Minute)
	updated, err := svc.UpdateShortLink(alice, &v1alpha.UpdateShortLinkRequest{
		ShortLink:  &v1alpha.ShortLink{Name: created.Name, DestinationUrl: "https://destination.example/two", Annotations: map[string]string{}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"destination_url", "annotations"}},
	})
	require.NoError(t, err)
	require.Equal(t, created.Uid, updated.Uid)
	require.NotEqual(t, created.Etag, updated.Etag)
	require.Empty(t, updated.Annotations)
	require.Equal(t, now, updated.UpdateTime.AsTime())

	_, err = svc.UpdateShortLink(alice, &v1alpha.UpdateShortLinkRequest{
		ShortLink: &v1alpha.ShortLink{Name: created.Name, DestinationUrl: "https://stale.example", Etag: created.Etag},
	})
	require.Equal(t, codes.Aborted, status.Code(err))
	_, err = svc.DeleteShortLink(alice, &v1alpha.DeleteShortLinkRequest{Name: created.Name, Etag: created.Etag})
	require.Equal(t, codes.Aborted, status.Code(err))
	_, err = svc.DeleteShortLink(bob, &v1alpha.DeleteShortLinkRequest{Name: created.Name})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = svc.DeleteShortLink(alice, &v1alpha.DeleteShortLinkRequest{Name: created.Name, Etag: updated.Etag})
	require.NoError(t, err)
	_, err = store.Get(context.Background(), &url.URL{Host: "example.com", Path: "/foo/bar", RawPath: path})
	require.ErrorIs(t, err, storage.ErrNotFound)

	recreated, err := svc.CreateShortLink(alice, &v1alpha.CreateShortLinkRequest{
		Parent:    "domains/example.com",
		ShortLink: &v1alpha.ShortLink{Path: &path, DestinationUrl: "https://destination.example/three"},
	})
	require.NoError(t, err)
	require.Equal(t, created.Name, recreated.Name)
	require.NotEqual(t, created.Uid, recreated.Uid)
	require.NotEqual(t, created.Etag, recreated.Etag)
}

func TestGeneratedCreateRetriesAndDeduplicates(t *testing.T) {
	store := memory.NewHashTable()
	suffixes := []string{"taken", "taken", "new"}
	svc := management.New(store, management.WithSuffixGenerator(func() (string, error) {
		value := suffixes[0]
		suffixes = suffixes[1:]
		return value, nil
	}))
	ctx := caller("alice")
	taken := "/taken"
	_, err := svc.CreateShortLink(ctx, &v1alpha.CreateShortLinkRequest{
		Parent:    "domains/example.com",
		ShortLink: &v1alpha.ShortLink{Path: &taken, DestinationUrl: "https://existing.example"},
	})
	require.NoError(t, err)
	request := &v1alpha.CreateShortLinkRequest{
		Parent:    "domains/example.com",
		ShortLink: &v1alpha.ShortLink{DestinationUrl: "https://destination.example"},
		RequestId: "retry-1",
	}
	created, err := svc.CreateShortLink(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "/new", created.GetPath())
	// A retry must return the original result without asking for another suffix.
	request.ShortLink.Annotations = map[string]string{}
	again, err := svc.CreateShortLink(ctx, request)
	require.NoError(t, err)
	require.Equal(t, created, again)
	request.ShortLink.DestinationUrl = "https://different.example"
	_, err = svc.CreateShortLink(ctx, request)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestListPaginationAndDesiredStateUpdate(t *testing.T) {
	store := memory.NewHashTable()
	svc := management.New(store)
	ctx := caller("alice")
	for _, source := range []struct{ domain, path string }{
		{"a.example", "/a"}, {"a.example", "/b"}, {"b.example", "/c"},
	} {
		path := source.path
		_, err := svc.CreateShortLink(ctx, &v1alpha.CreateShortLinkRequest{
			Parent:    "domains/" + source.domain,
			ShortLink: &v1alpha.ShortLink{Path: &path, DestinationUrl: "https://destination.example"},
		})
		require.NoError(t, err)
	}
	first, err := svc.ListShortLinks(ctx, &v1alpha.ListShortLinksRequest{Parent: "domains/-", PageSize: 1})
	require.NoError(t, err)
	require.Len(t, first.ShortLinks, 1)
	require.NotEmpty(t, first.NextPageToken)
	second, err := svc.ListShortLinks(ctx, &v1alpha.ListShortLinksRequest{Parent: "domains/-", PageSize: 1, PageToken: first.NextPageToken})
	require.NoError(t, err)
	require.Len(t, second.ShortLinks, 1)
	require.Greater(t, second.ShortLinks[0].Name, first.ShortLinks[0].Name)
	third, err := svc.ListShortLinks(ctx, &v1alpha.ListShortLinksRequest{Parent: "domains/-", PageSize: 1, PageToken: second.NextPageToken})
	require.NoError(t, err)
	require.Len(t, third.ShortLinks, 1)
	require.Empty(t, third.NextPageToken)
	_, err = svc.ListShortLinks(ctx, &v1alpha.ListShortLinksRequest{Parent: "domains/a.example", PageSize: 1, PageToken: first.NextPageToken})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = svc.ListShortLinks(ctx, &v1alpha.ListShortLinksRequest{Parent: "domains/-", PageSize: -1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	resource := first.ShortLinks[0]
	applied, err := svc.UpdateShortLink(ctx, &v1alpha.UpdateShortLinkRequest{
		ShortLink: &v1alpha.ShortLink{Name: resource.Name, DestinationUrl: resource.DestinationUrl},
	})
	require.NoError(t, err)
	require.Equal(t, resource.Etag, applied.Etag)
	require.Equal(t, resource.UpdateTime, applied.UpdateTime)

	missing := name(t, "a.example", "/missing")
	created, err := svc.UpdateShortLink(ctx, &v1alpha.UpdateShortLinkRequest{
		ShortLink:    &v1alpha.ShortLink{Name: missing, DestinationUrl: "https://new.example"},
		AllowMissing: true,
	})
	require.NoError(t, err)
	require.Equal(t, missing, created.Name)
	_, err = svc.DeleteShortLink(ctx, &v1alpha.DeleteShortLinkRequest{Name: missing})
	require.NoError(t, err)
	_, err = svc.DeleteShortLink(ctx, &v1alpha.DeleteShortLinkRequest{Name: missing, AllowMissing: true})
	require.NoError(t, err)
}

func TestValidateOnlyAndInvalidInputs(t *testing.T) {
	store := memory.NewHashTable()
	svc := management.New(store)
	ctx := caller("alice")
	path := "/"
	proposed, err := svc.CreateShortLink(ctx, &v1alpha.CreateShortLinkRequest{
		Parent:       "domains/example.com",
		ShortLink:    &v1alpha.ShortLink{Path: &path, DestinationUrl: "https://destination.example"},
		RequestId:    "validation-only",
		ValidateOnly: true,
	})
	require.NoError(t, err)
	_, err = svc.GetShortLink(ctx, &v1alpha.GetShortLinkRequest{Name: proposed.Name})
	require.Equal(t, codes.NotFound, status.Code(err))
	_, err = svc.CreateShortLink(ctx, &v1alpha.CreateShortLinkRequest{
		Parent:    "domains/example.com",
		ShortLink: &v1alpha.ShortLink{Path: &path, DestinationUrl: "https://another.example"},
		RequestId: "validation-only",
	})
	require.NoError(t, err)

	for _, req := range []*v1alpha.CreateShortLinkRequest{
		{Parent: "domains/example.com:443", ShortLink: &v1alpha.ShortLink{Path: &path, DestinationUrl: "https://good.example"}},
		{Parent: "domains/example.com", ShortLink: &v1alpha.ShortLink{Path: &path, DestinationUrl: "ftp://bad.example"}},
		{Parent: "domains/example.com", ShortLink: &v1alpha.ShortLink{Path: &path, DestinationUrl: "relative"}},
		{Parent: "domains/example.com", ShortLink: &v1alpha.ShortLink{Path: &path, DestinationUrl: "https://good.example"}, RequestId: strings.Repeat("x", 37)},
	} {
		_, err := svc.CreateShortLink(ctx, req)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}
	_, err = svc.UpdateShortLink(ctx, &v1alpha.UpdateShortLinkRequest{
		ShortLink:  &v1alpha.ShortLink{Name: proposed.Name, DestinationUrl: "https://valid.example"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"uid"}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestManagementRenamesRequestSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer := provider.Tracer("test")
	svc := management.New(memory.NewHashTable())
	path := "/observed"
	linkName := name(t, "example.com", path)
	for _, operation := range []struct {
		name string
		call func(context.Context) error
	}{
		{"create_link", func(ctx context.Context) error {
			_, err := svc.CreateShortLink(ctx, &v1alpha.CreateShortLinkRequest{Parent: "domains/example.com", ShortLink: &v1alpha.ShortLink{Path: &path, DestinationUrl: "https://one.example"}})
			return err
		}},
		{"get_link", func(ctx context.Context) error {
			_, err := svc.GetShortLink(ctx, &v1alpha.GetShortLinkRequest{Name: linkName})
			return err
		}},
		{"list_links", func(ctx context.Context) error {
			_, err := svc.ListShortLinks(ctx, &v1alpha.ListShortLinksRequest{Parent: "domains/example.com"})
			return err
		}},
		{"update_link", func(ctx context.Context) error {
			_, err := svc.UpdateShortLink(ctx, &v1alpha.UpdateShortLinkRequest{ShortLink: &v1alpha.ShortLink{Name: linkName, DestinationUrl: "https://two.example"}})
			return err
		}},
		{"delete_link", func(ctx context.Context) error {
			_, err := svc.DeleteShortLink(ctx, &v1alpha.DeleteShortLinkRequest{Name: linkName})
			return err
		}},
	} {
		ctx, span := tracer.Start(caller("alice"), "transport")
		require.NoError(t, operation.call(ctx))
		span.End()
		ended := recorder.Ended()
		require.Equal(t, operation.name, ended[len(ended)-1].Name())
		require.Contains(t, ended[len(ended)-1].Attributes(), attribute.String("x40.link.domain", "example.com"))
	}
}
