// Package di provides utilities to help resolve dependencies into valid objects
package di

import (
	"errors"
	"fmt"

	"github.com/andrewhowdencom/x40.link/api/auth/jwts"
	"github.com/andrewhowdencom/x40.link/cfg"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
)

// ErrDependencyFailure means that, for some reason, the dependency required didn't work
var ErrDependencyFailure = errors.New("dependency failure")

// OptsFromViper reads the configuration and returns transport options and
// the authorizer shared by native gRPC and generated HTTP gateway routes.
func OptsFromViper() ([]grpc.ServerOption, *jwts.ServerInterceptor, error) {
	opts := []grpc.ServerOption{}

	// otelgrpc emits the parent span per RPC and provides server-side
	// metrics. NewServerHandler in v0.59.0 is a unified stats handler
	// that handles both tracing and metrics — the legacy
	// UnaryServerInterceptor / StreamServerInterceptor are deprecated.
	opts = append(opts,
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	)

	// The interceptor is a soft dependency — it can fail. Here, we're indicating that failure through the
	// cfg.ErrMissingOptions
	icept, err := jwts.WireServerInterceptor()
	if err != nil && !errors.Is(err, cfg.ErrMissingOptions) {
		return nil, nil, fmt.Errorf("%w: %s", ErrDependencyFailure, err)
	} else if err == nil {
		opts = append(
			opts,
			grpc.StreamInterceptor(icept.StreamServerInterceptor),
			grpc.UnaryInterceptor(icept.UnaryServerInterceptor),
		)
	}

	return opts, icept, nil
}
