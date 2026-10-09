package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	v1alpha "github.com/x40-link/api/gen/x40/link/v1alpha"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Authorizer applies the same per-method scope checks to gateway requests
// that the gRPC server interceptor applies to native gRPC requests.
type Authorizer func(context.Context, string) (context.Context, error)

type authorizedGateway struct {
	v1alpha.UnimplementedShortLinkServiceServer
	inner v1alpha.ShortLinkServiceServer
	auth  Authorizer
}

func (s authorizedGateway) CreateShortLink(ctx context.Context, req *v1alpha.CreateShortLinkRequest) (*v1alpha.ShortLink, error) {
	ctx, err := s.auth(ctx, v1alpha.ShortLinkService_CreateShortLink_FullMethodName)
	if err != nil {
		return nil, err
	}
	return s.inner.CreateShortLink(ctx, req)
}
func (s authorizedGateway) GetShortLink(ctx context.Context, req *v1alpha.GetShortLinkRequest) (*v1alpha.ShortLink, error) {
	ctx, err := s.auth(ctx, v1alpha.ShortLinkService_GetShortLink_FullMethodName)
	if err != nil {
		return nil, err
	}
	return s.inner.GetShortLink(ctx, req)
}
func (s authorizedGateway) ListShortLinks(ctx context.Context, req *v1alpha.ListShortLinksRequest) (*v1alpha.ListShortLinksResponse, error) {
	ctx, err := s.auth(ctx, v1alpha.ShortLinkService_ListShortLinks_FullMethodName)
	if err != nil {
		return nil, err
	}
	return s.inner.ListShortLinks(ctx, req)
}
func (s authorizedGateway) UpdateShortLink(ctx context.Context, req *v1alpha.UpdateShortLinkRequest) (*v1alpha.ShortLink, error) {
	ctx, err := s.auth(ctx, v1alpha.ShortLinkService_UpdateShortLink_FullMethodName)
	if err != nil {
		return nil, err
	}
	return s.inner.UpdateShortLink(ctx, req)
}
func (s authorizedGateway) DeleteShortLink(ctx context.Context, req *v1alpha.DeleteShortLinkRequest) (*emptypb.Empty, error) {
	ctx, err := s.auth(ctx, v1alpha.ShortLinkService_DeleteShortLink_FullMethodName)
	if err != nil {
		return nil, err
	}
	return s.inner.DeleteShortLink(ctx, req)
}

// NewGateway mounts the generated HTTP/JSON handlers on the same service
// instance used by gRPC. The wrapper enforces every method's OAuth scope.
func NewGateway(service v1alpha.ShortLinkServiceServer, authorize Authorizer) (http.Handler, error) {
	if service == nil || authorize == nil {
		return nil, errors.New("gateway requires a service and authorizer")
	}
	mux := runtime.NewServeMux()
	if err := v1alpha.RegisterShortLinkServiceHandlerServer(context.Background(), mux, authorizedGateway{inner: service, auth: authorize}); err != nil {
		return nil, err
	}
	return mux, nil
}
