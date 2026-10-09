// Package api wires the canonical, separately versioned x40-link API into
// the application's transport and authentication boundary.
package api

import (
	"crypto/x509"
	"errors"
	"fmt"
	"sort"

	"github.com/andrewhowdencom/x40.link/api/management"
	"github.com/andrewhowdencom/x40.link/storage"
	v1alpha "github.com/x40-link/api/gen/x40/link/v1alpha"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	descpb "google.golang.org/protobuf/types/descriptorpb"
)

var (
	ErrCannotDialServer    = errors.New("cannot connect to grpc server")
	ErrMissingCertificates = errors.New("cannot get system certificates")
)

// Client is the versioned management API client used by the CLI.
type Client interface{ v1alpha.ShortLinkServiceClient }

// ReflectionPermissions permits reflection without an access token.
func ReflectionPermissions() map[string]string {
	return map[string]string{
		"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo": "",
	}
}

// X40Permissions extracts method scopes from the pinned canonical proto.
func X40Permissions() map[string]string {
	permissions := map[string]string{}
	protoregistry.GlobalFiles.RangeFilesByPackage(protoreflect.FullName("x40.link.v1alpha"), func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Services().Len(); i++ {
			svc := fd.Services().Get(i)
			if string(svc.Name()) != "ShortLinkService" {
				continue
			}
			for j := 0; j < svc.Methods().Len(); j++ {
				method := svc.Methods().Get(j)
				options := method.Options().(*descpb.MethodOptions)
				scope, _ := proto.GetExtension(options, v1alpha.E_Oauth2Scope).(string)
				permissions["/"+string(svc.FullName())+"/"+string(method.Name())] = scope
			}
		}
		return true
	})
	return permissions
}

// X40PermissionsList returns all nonempty scopes in stable order.
func X40PermissionsList() []string {
	list := make([]string, 0, 5)
	for _, scope := range X40Permissions() {
		if scope != "" {
			list = append(list, scope)
		}
	}
	sort.Strings(list)
	return list
}

// NewGRPCMux serves only the canonical v1alpha ShortLinkService.
func NewGRPCMux(store storage.ManagedStore, opts ...grpc.ServerOption) *grpc.Server {
	return NewGRPCMuxWithService(management.New(store), opts...)
}

// NewGRPCMuxWithService shares a service with the generated HTTP gateway.
func NewGRPCMuxWithService(service v1alpha.ShortLinkServiceServer, opts ...grpc.ServerOption) *grpc.Server {
	server := grpc.NewServer(opts...)
	v1alpha.RegisterShortLinkServiceServer(server, service)
	reflection.Register(server)
	return server
}

// NewGRPCClient creates a TLS client for the canonical v1alpha API.
func NewGRPCClient(addr string, opts ...grpc.DialOption) (Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMissingCertificates, err)
	}
	opts = append(opts, grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(pool, "")))
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCannotDialServer, err)
	}
	return v1alpha.NewShortLinkServiceClient(conn), nil
}
