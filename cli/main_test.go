package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/andrewhowdencom/sysexits"
	"github.com/andrewhowdencom/x40.link/cfg"
	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/stretchr/testify/require"
	v1alpha "github.com/x40-link/api/gen/x40/link/v1alpha"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeClient struct {
	v1alpha.ShortLinkServiceClient
	get  func(context.Context, *v1alpha.GetShortLinkRequest) (*v1alpha.ShortLink, error)
	list func(context.Context, *v1alpha.ListShortLinksRequest) (*v1alpha.ListShortLinksResponse, error)
}

func (f *fakeClient) GetShortLink(ctx context.Context, req *v1alpha.GetShortLinkRequest, _ ...grpc.CallOption) (*v1alpha.ShortLink, error) {
	return f.get(ctx, req)
}
func (f *fakeClient) ListShortLinks(ctx context.Context, req *v1alpha.ListShortLinksRequest, _ ...grpc.CallOption) (*v1alpha.ListShortLinksResponse, error) {
	return f.list(ctx, req)
}

func TestBuildCreateRequestPreservesPathIdentity(t *testing.T) {
	for _, path := range []string{"/foo/bar", "/foo+bar", "/foo%2Fbar", "/foo//bar", "/"} {
		req, err := buildNewRequest([]string{"EXAMPLE.COM" + path, "destination.example"})
		require.NoError(t, err)
		require.Equal(t, "domains/example.com", req.Parent)
		require.Equal(t, path, req.ShortLink.GetPath())
		require.Equal(t, "https://destination.example", req.ShortLink.DestinationUrl)
	}
	req, err := buildNewRequest([]string{"https://destination.example"})
	require.NoError(t, err)
	require.Equal(t, "domains/x40.link", req.Parent)
	require.Nil(t, req.ShortLink.Path)
	req, err = buildNewRequest([]string{"example.com", "https://destination.example"})
	require.NoError(t, err)
	require.Nil(t, req.ShortLink.Path)
}

func TestResolveUsesResourceName(t *testing.T) {
	want, err := shortlink.ResourceName("example.com", "/foo%2Fbar")
	require.NoError(t, err)
	client := &fakeClient{get: func(_ context.Context, req *v1alpha.GetShortLinkRequest) (*v1alpha.ShortLink, error) {
		require.Equal(t, want, req.Name)
		return &v1alpha.ShortLink{DestinationUrl: "https://destination.example/path"}, nil
	}}
	got, err := doResolveWithClient(context.Background(), client, "EXAMPLE.COM/foo%2Fbar")
	require.NoError(t, err)
	require.Equal(t, "https://destination.example/path", got)
	_, err = doResolveWithClient(context.Background(), client, "example.com/foo?query=1")
	require.ErrorIs(t, err, sysexits.DataErr)
}

func TestListPaginatesAndFilters(t *testing.T) {
	called := 0
	client := &fakeClient{list: func(_ context.Context, req *v1alpha.ListShortLinksRequest) (*v1alpha.ListShortLinksResponse, error) {
		called++
		require.Equal(t, "domains/example.com", req.Parent)
		if called == 1 {
			require.Empty(t, req.PageToken)
			return &v1alpha.ListShortLinksResponse{ShortLinks: []*v1alpha.ShortLink{{ShortUrl: "https://example.com/a", DestinationUrl: "https://one.example"}}, NextPageToken: "page-2"}, nil
		}
		require.Equal(t, "page-2", req.PageToken)
		return &v1alpha.ListShortLinksResponse{ShortLinks: []*v1alpha.ShortLink{{ShortUrl: "https://example.com/b", DestinationUrl: "https://two.example"}}}, nil
	}}
	var output bytes.Buffer
	require.NoError(t, runList(context.Background(), client, "EXAMPLE.COM", &output))
	require.Equal(t, 2, called)
	require.Equal(t, "https://example.com/a  https://one.example\nhttps://example.com/b  https://two.example\n", output.String())
}

func TestResolveAndListErrors(t *testing.T) {
	client := &fakeClient{get: func(context.Context, *v1alpha.GetShortLinkRequest) (*v1alpha.ShortLink, error) {
		return nil, status.Error(codes.NotFound, "missing")
	}, list: func(context.Context, *v1alpha.ListShortLinksRequest) (*v1alpha.ListShortLinksResponse, error) {
		return nil, errors.New("transport failed")
	}}
	_, err := doResolveWithClient(context.Background(), client, "example.com/missing")
	require.ErrorIs(t, err, sysexits.DataErr)
	_, err = doListWithClient(context.Background(), client, "")
	require.ErrorIs(t, err, sysexits.NoHost)
}

func TestLoginAndResolveCommandFlags(t *testing.T) {
	command, _, err := Root.Find([]string{"login"})
	require.NoError(t, err)
	require.Same(t, loginCmd, command)
	require.NotNil(t, loginCmd.Flags().Lookup(cfg.OAuth2ClientID.Path))
	require.Nil(t, loginCmd.Flags().Lookup(cfg.APIEndpoint.Path))
	require.NotNil(t, resolveCmd.Flags().Lookup(cfg.APIEndpoint.Path))
	require.NotNil(t, resolveCmd.Flags().Lookup(cfg.OAuth2ClientID.Path))
	loginErr := errors.New("login failed")
	require.ErrorIs(t, func() error { return doLogin(context.Background(), func(context.Context) error { return loginErr }) }(), sysexits.Software)
	require.NoError(t, doLogin(context.Background(), func(context.Context) error { return nil }))
}
