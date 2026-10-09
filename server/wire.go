package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/andrewhowdencom/x40.link/api"
	apiopts "github.com/andrewhowdencom/x40.link/api/di"
	"github.com/andrewhowdencom/x40.link/api/management"
	"github.com/andrewhowdencom/x40.link/cfg"
	"github.com/andrewhowdencom/x40.link/storage"
	strdi "github.com/andrewhowdencom/x40.link/storage/di"
)

var ErrDependencyFailure = errors.New("dependency failure")

// ResolveOptions constructs redirects and management over the same store.
// This matters for the in-memory backend and avoids competing BoltDB locks.
func ResolveOptions() ([]Option, error) {
	opts := []Option{}
	if cfg.OTELEnabled.Value() {
		opts = append(opts, WithOtel())
	}
	if addr := cfg.ServerListenAddress.Value(); addr != "" {
		opts = append(opts, WithListenAddress(addr))
	}
	storer, name, err := strdi.WireStorage()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDependencyFailure, err)
	}
	grpcOpts, authorizer, err := apiopts.OptsFromViper()
	if err != nil && !errors.Is(err, cfg.ErrMissingOptions) {
		return nil, fmt.Errorf("%w: %v", ErrDependencyFailure, err)
	}
	if err == nil {
		managed, ok := storer.(storage.ManagedStore)
		if !ok {
			return nil, fmt.Errorf("%w: selected storage does not support the management API", ErrDependencyFailure)
		}
		service := management.New(managed)
		opts = append(opts, WithGRPC(cfg.ServerAPIGRPCHost.Value(), api.NewGRPCMuxWithService(service, grpcOpts...)))
		gateway, err := api.NewGateway(service, authorizer.ValidateCtx)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDependencyFailure, err)
		}
		opts = append(opts, WithGateway(gateway))
	}
	opts = append(opts, WithStorage(storer, name))
	if cfg.ServerH2CEnabled.Value() {
		opts = append(opts, WithH2C())
	}
	return opts, nil
}

func WireServer() (*http.Server, error) {
	opts, err := ResolveOptions()
	if err != nil {
		return nil, err
	}
	return New(opts...)
}
