package management

import (
	"context"
	"errors"
	"maps"

	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/andrewhowdencom/x40.link/storage"
	v1alpha "github.com/x40-link/api/gen/x40/link/v1alpha"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func updateFields(req *v1alpha.UpdateShortLinkRequest, current storage.ManagedLink) (storage.ManagedLink, error) {
	next := current.Clone()
	changeDestination, changeAnnotations := false, false
	if req.UpdateMask == nil {
		changeDestination = req.ShortLink.DestinationUrl != ""
		changeAnnotations = len(req.ShortLink.Annotations) > 0
	} else {
		if len(req.UpdateMask.Paths) == 0 {
			return storage.ManagedLink{}, status.Error(codes.InvalidArgument, "empty update_mask")
		}
		for _, path := range req.UpdateMask.Paths {
			switch path {
			case "*":
				if len(req.UpdateMask.Paths) != 1 {
					return storage.ManagedLink{}, status.Error(codes.InvalidArgument, "* must be the only update_mask path")
				}
				changeDestination, changeAnnotations = true, true
			case "destination_url", "destinationUrl":
				changeDestination = true
			case "annotations":
				changeAnnotations = true
			default:
				return storage.ManagedLink{}, status.Error(codes.InvalidArgument, "update_mask may change only destination_url and annotations")
			}
		}
	}
	if changeDestination {
		if err := validDestination(req.ShortLink.DestinationUrl); err != nil {
			return storage.ManagedLink{}, err
		}
		next.DestinationURL = req.ShortLink.DestinationUrl
	}
	if changeAnnotations {
		next.Annotations = cloneAnnotations(req.ShortLink.Annotations)
	}
	return next, nil
}

// UpdateShortLink merges only mutable fields, then commits with a storage
// compare-and-swap. When the caller omits ETag, a concurrent change is read
// again and the desired fields are applied to the latest version.
func (s *Service) UpdateShortLink(ctx context.Context, req *v1alpha.UpdateShortLinkRequest) (*v1alpha.ShortLink, error) {
	if req == nil || req.ShortLink == nil {
		return nil, status.Error(codes.InvalidArgument, "short_link is required")
	}
	domain, path, err := validName(req.ShortLink.Name)
	if err != nil {
		return nil, err
	}
	operation(ctx, "update_link", domain)
	owner, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if req.ShortLink.Path != nil {
		provided, err := shortlink.CanonicalPath(*req.ShortLink.Path)
		if err != nil || provided != path {
			return nil, status.Error(codes.InvalidArgument, "path is immutable and must match name")
		}
	}
	// Validate the mask even on the allow_missing branch, where its contents
	// are ignored for the actual creation.
	if _, err := updateFields(req, storage.ManagedLink{DestinationURL: "https://placeholder.invalid"}); err != nil && req.UpdateMask != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		current, err := s.store.GetManaged(ctx, req.ShortLink.Name, owner)
		if errors.Is(err, storage.ErrNotFound) && req.AllowMissing {
			if req.ShortLink.Etag != "" {
				return nil, status.Error(codes.Aborted, "etag does not match a missing short link")
			}
			if err := validDestination(req.ShortLink.DestinationUrl); err != nil {
				return nil, err
			}
			proposed, err := newRecord(req.ShortLink.Name, path, req.ShortLink.DestinationUrl, owner, req.ShortLink.Annotations, s.clock())
			if err != nil {
				return nil, internalError(err)
			}
			if req.ValidateOnly {
				return toProto(proposed), nil
			}
			created, err := s.store.CreateManaged(ctx, proposed, "", "", s.clock())
			if errors.Is(err, storage.ErrAlreadyExists) {
				continue
			}
			if err != nil {
				return nil, storageError(err)
			}
			return toProto(created), nil
		}
		if err != nil {
			return nil, storageError(err)
		}
		if req.ShortLink.Etag != "" && req.ShortLink.Etag != current.ETag {
			return nil, status.Error(codes.Aborted, "etag does not match")
		}
		next, err := updateFields(req, current)
		if err != nil {
			return nil, err
		}
		if current.DestinationURL == next.DestinationURL && maps.Equal(current.Annotations, next.Annotations) {
			return toProto(current), nil
		}
		next.UpdateTime = s.clock().UTC()
		next.ETag = etag(next)
		if req.ValidateOnly {
			return toProto(next), nil
		}
		updated, err := s.store.UpdateManaged(ctx, next, current.ETag)
		if errors.Is(err, storage.ErrAborted) && req.ShortLink.Etag == "" {
			continue
		}
		if err != nil {
			return nil, storageError(err)
		}
		return toProto(updated), nil
	}
}

func (s *Service) DeleteShortLink(ctx context.Context, req *v1alpha.DeleteShortLinkRequest) (*emptypb.Empty, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	domain, _, err := validName(req.Name)
	if err != nil {
		return nil, err
	}
	operation(ctx, "delete_link", domain)
	owner, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if req.ValidateOnly {
		current, err := s.store.GetManaged(ctx, req.Name, owner)
		if errors.Is(err, storage.ErrNotFound) && req.AllowMissing {
			return &emptypb.Empty{}, nil
		}
		if err != nil {
			return nil, storageError(err)
		}
		if req.Etag != "" && req.Etag != current.ETag {
			return nil, status.Error(codes.Aborted, "etag does not match")
		}
		return &emptypb.Empty{}, nil
	}
	err = s.store.DeleteManaged(ctx, req.Name, owner, req.Etag)
	if errors.Is(err, storage.ErrNotFound) && req.AllowMissing {
		return &emptypb.Empty{}, nil
	}
	if err != nil {
		return nil, storageError(err)
	}
	return &emptypb.Empty{}, nil
}
