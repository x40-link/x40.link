// Package management implements the versioned ShortLink management contract.
// Its storage interface keeps authorization and conditional writes in the
// backend, while the separate public redirect reads the same records.
package management

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/andrewhowdencom/x40.link/storage"
	"github.com/google/uuid"
	v1alpha "github.com/x40-link/api/gen/x40/link/v1alpha"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service implements all five synchronous v1alpha management RPCs.
type Service struct {
	v1alpha.UnimplementedShortLinkServiceServer
	store  storage.ManagedStore
	clock  func() time.Time
	suffix func() (string, error)
}

var _ v1alpha.ShortLinkServiceServer = (*Service)(nil)

// Option changes a service dependency for testing or deployment.
type Option func(*Service)

func WithClock(clock func() time.Time) Option {
	return func(s *Service) { s.clock = clock }
}

func WithSuffixGenerator(generator func() (string, error)) Option {
	return func(s *Service) { s.suffix = generator }
}

func New(store storage.ManagedStore, options ...Option) *Service {
	s := &Service{store: store, clock: time.Now, suffix: randomSuffix}
	for _, option := range options {
		option(s)
	}
	return s
}

func randomSuffix() (string, error) {
	var data [8]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(data[:])), nil
}

func caller(ctx context.Context) (string, error) {
	owner, _ := ctx.Value(storage.CtxKeyAgent).(string)
	if owner == "" {
		return "", status.Error(codes.Unauthenticated, "authentication required")
	}
	return owner, nil
}

func domainParent(parent string, allowAll bool) (string, error) {
	const prefix = "domains/"
	if !strings.HasPrefix(parent, prefix) {
		return "", status.Error(codes.InvalidArgument, "parent must be domains/{domain}")
	}
	domain := strings.TrimPrefix(parent, prefix)
	if allowAll && domain == "-" {
		return "", nil
	}
	canonical, err := shortlink.CanonicalDomain(domain)
	if err != nil {
		return "", status.Error(codes.InvalidArgument, "invalid parent domain")
	}
	return canonical, nil
}

func validName(name string) (string, string, error) {
	domain, path, err := shortlink.ParseResourceName(name)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, "invalid short link name")
	}
	return domain, path, nil
}

func validDestination(value string) error {
	u, err := url.Parse(value)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" {
		return status.Error(codes.InvalidArgument, "destination_url must be an absolute HTTP or HTTPS URL")
	}
	return nil
}

func validRequestID(value string) error {
	if len(value) > 36 {
		return status.Error(codes.InvalidArgument, "request_id exceeds 36 ASCII characters")
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 32 || value[i] > 126 {
			return status.Error(codes.InvalidArgument, "request_id must contain printable ASCII")
		}
	}
	return nil
}

func newRecord(name, path, destination, owner string, annotations map[string]string, at time.Time) (storage.ManagedLink, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return storage.ManagedLink{}, err
	}
	link := storage.ManagedLink{
		Name: name, Path: path, DestinationURL: destination,
		Annotations: cloneAnnotations(annotations), Owner: owner,
		UID: id.String(), CreateTime: at.UTC(), UpdateTime: at.UTC(),
	}
	link.ETag = etag(link)
	return link, nil
}

func etag(link storage.ManagedLink) string {
	data, _ := json.Marshal(struct {
		UID         string
		Destination string
		Annotations map[string]string
		Updated     time.Time
	}{link.UID, link.DestinationURL, link.Annotations, link.UpdateTime})
	hash := sha256.Sum256(data)
	return `"` + hex.EncodeToString(hash[:]) + `"`
}

func fingerprint(parent string, path *string, destination string, annotations map[string]string) string {
	if len(annotations) == 0 {
		annotations = nil
	}
	data, _ := json.Marshal(struct {
		Parent      string
		Path        *string
		Destination string
		Annotations map[string]string
	}{parent, path, destination, annotations})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func cloneAnnotations(annotations map[string]string) map[string]string {
	if annotations == nil {
		return nil
	}
	result := make(map[string]string, len(annotations))
	for key, value := range annotations {
		result[key] = value
	}
	return result
}

func toProto(link storage.ManagedLink) *v1alpha.ShortLink {
	domain, _, _ := shortlink.ParseResourceName(link.Name)
	path := link.Path
	return &v1alpha.ShortLink{
		Name: link.Name, Path: &path, DestinationUrl: link.DestinationURL,
		Annotations: cloneAnnotations(link.Annotations), Uid: link.UID,
		CreateTime: timestamppb.New(link.CreateTime), UpdateTime: timestamppb.New(link.UpdateTime),
		ShortUrl: "https://" + domain + path, Etag: link.ETag,
	}
}

func storageError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrNotFound):
		return status.Error(codes.NotFound, "short link not found")
	case errors.Is(err, storage.ErrUnauthorized):
		return status.Error(codes.PermissionDenied, "short link is not visible to caller")
	case errors.Is(err, storage.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, "short link already exists")
	case errors.Is(err, storage.ErrAborted):
		return status.Error(codes.Aborted, "short link changed")
	case errors.Is(err, storage.ErrRequestIDConflict), errors.Is(err, storage.ErrInvalidSource):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, storage.ErrReadOnlyStorage):
		return status.Error(codes.FailedPrecondition, "storage is read only")
	default:
		return status.Error(codes.Internal, "storage operation failed")
	}
}

func operation(ctx context.Context, name, domain string) {
	span := trace.SpanFromContext(ctx)
	span.SetName(name)
	if domain != "" {
		span.SetAttributes(attribute.String("x40.link.domain", domain))
	}
}

func internalError(err error) error {
	return status.Error(codes.Internal, fmt.Sprintf("short link operation failed: %v", err))
}
