package management

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	v1alpha "github.com/x40-link/api/gen/x40/link/v1alpha"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	defaultPageSize = 50
	maximumPageSize = 500
)

func (s *Service) GetShortLink(ctx context.Context, req *v1alpha.GetShortLinkRequest) (*v1alpha.ShortLink, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	domain, _, err := validName(req.Name)
	if err != nil {
		return nil, err
	}
	operation(ctx, "get_link", domain)
	owner, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	link, err := s.store.GetManaged(ctx, req.Name, owner)
	if err != nil {
		return nil, storageError(err)
	}
	return toProto(link), nil
}

type pageCursor struct {
	Parent   string `json:"p"`
	PageSize int32  `json:"s"`
	After    string `json:"a"`
	Check    string `json:"c"`
}

func cursorCheck(owner string, cursor pageCursor) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%d\n%s", owner, cursor.Parent, cursor.PageSize, cursor.After)))
	return hex.EncodeToString(hash[:])
}

func encodeCursor(owner string, cursor pageCursor) string {
	cursor.Check = cursorCheck(owner, cursor)
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(token, owner, parent string, size int32) (string, error) {
	if len(token) > 4096 {
		return "", status.Error(codes.InvalidArgument, "invalid page_token")
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", status.Error(codes.InvalidArgument, "invalid page_token")
	}
	var cursor pageCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Parent != parent || cursor.PageSize != size || cursor.After == "" || cursor.Check != cursorCheck(owner, cursor) {
		return "", status.Error(codes.InvalidArgument, "page_token does not match request")
	}
	if _, _, err := validName(cursor.After); err != nil {
		return "", status.Error(codes.InvalidArgument, "invalid page_token")
	}
	return cursor.After, nil
}

// ListShortLinks uses name order and an exclusive keyset cursor. A deletion
// between pages cannot repeat an item; a newly inserted later name may appear
// on a subsequent page. The token binds parent, requested size, and caller.
func (s *Service) ListShortLinks(ctx context.Context, req *v1alpha.ListShortLinksRequest) (*v1alpha.ListShortLinksResponse, error) {
	if req == nil || req.PageSize < 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid list request")
	}
	domain, err := domainParent(req.Parent, true)
	if err != nil {
		return nil, err
	}
	operation(ctx, "list_links", domain)
	owner, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	limit := int(req.PageSize)
	if limit == 0 {
		limit = defaultPageSize
	} else if limit > maximumPageSize {
		limit = maximumPageSize
	}
	parent := "domains/-"
	if domain != "" {
		parent = "domains/" + domain
	}
	after := ""
	if req.PageToken != "" {
		after, err = decodeCursor(req.PageToken, owner, parent, req.PageSize)
		if err != nil {
			return nil, err
		}
		if domain != "" && !strings.HasPrefix(after, parent+"/shortLinks/") {
			return nil, status.Error(codes.InvalidArgument, "page_token does not match parent")
		}
	}
	links, err := s.store.ListManaged(ctx, owner, domain)
	if err != nil {
		return nil, storageError(err)
	}
	response := &v1alpha.ListShortLinksResponse{ShortLinks: make([]*v1alpha.ShortLink, 0, limit)}
	more := false
	for _, link := range links {
		if link.Name <= after {
			continue
		}
		if len(response.ShortLinks) == limit {
			more = true
			break
		}
		response.ShortLinks = append(response.ShortLinks, toProto(link))
	}
	if more {
		response.NextPageToken = encodeCursor(owner, pageCursor{
			Parent: parent, PageSize: req.PageSize,
			After: response.ShortLinks[len(response.ShortLinks)-1].Name,
		})
	}
	return response, nil
}
