package management

import (
	"context"
	"errors"
	"strings"

	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/andrewhowdencom/x40.link/storage"
	v1alpha "github.com/x40-link/api/gen/x40/link/v1alpha"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateShortLink claims an explicit address or retries generated suffix
// collisions. Request deduplication is checked before suffix generation.
func (s *Service) CreateShortLink(ctx context.Context, req *v1alpha.CreateShortLinkRequest) (*v1alpha.ShortLink, error) {
	if req == nil || req.ShortLink == nil {
		return nil, status.Error(codes.InvalidArgument, "short_link is required")
	}
	domain, err := domainParent(req.Parent, false)
	if err != nil {
		return nil, err
	}
	operation(ctx, "create_link", domain)
	owner, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := validDestination(req.ShortLink.DestinationUrl); err != nil {
		return nil, err
	}
	if err := validRequestID(req.RequestId); err != nil {
		return nil, err
	}
	var explicitPath *string
	if req.ShortLink.Path != nil {
		path, err := shortlink.CanonicalPath(*req.ShortLink.Path)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid source path")
		}
		explicitPath = &path
	}
	requestHash := fingerprint(domain, explicitPath, req.ShortLink.DestinationUrl, req.ShortLink.Annotations)
	now := s.clock().UTC()
	if req.RequestId != "" && !req.ValidateOnly {
		retained, err := s.store.GetRequest(ctx, owner, req.RequestId, requestHash, now)
		if err == nil {
			return toProto(retained), nil
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return nil, storageError(err)
		}
	}

	for attempt := 0; attempt < 64; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		path := ""
		if explicitPath != nil {
			path = *explicitPath
		} else {
			suffix, err := s.suffix()
			if err != nil {
				return nil, internalError(err)
			}
			if suffix == "" || strings.ContainsAny(suffix, "/?#") {
				return nil, status.Error(codes.Internal, "suffix generator produced an invalid path")
			}
			path, err = shortlink.CanonicalPath("/" + suffix)
			if err != nil {
				return nil, internalError(err)
			}
		}
		name, err := shortlink.ResourceName(domain, path)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid source address")
		}
		proposed, err := newRecord(name, path, req.ShortLink.DestinationUrl, owner, req.ShortLink.Annotations, now)
		if err != nil {
			return nil, internalError(err)
		}
		if req.ValidateOnly {
			_, err := s.store.GetManaged(ctx, name, owner)
			if errors.Is(err, storage.ErrNotFound) {
				return toProto(proposed), nil
			}
			if explicitPath == nil && (err == nil || errors.Is(err, storage.ErrUnauthorized)) {
				continue
			}
			if err == nil {
				return nil, status.Error(codes.AlreadyExists, "short link already exists")
			}
			return nil, storageError(err)
		}

		created, err := s.store.CreateManaged(ctx, proposed, req.RequestId, requestHash, now)
		if err == nil {
			return toProto(created), nil
		}
		if explicitPath == nil && (errors.Is(err, storage.ErrAlreadyExists) || errors.Is(err, storage.ErrUnauthorized)) {
			continue
		}
		return nil, storageError(err)
	}
	return nil, status.Error(codes.ResourceExhausted, "unable to allocate an unused short path")
}
