package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrewhowdencom/x40.link/api"
	"github.com/andrewhowdencom/x40.link/api/management"
	"github.com/andrewhowdencom/x40.link/storage"
	"github.com/andrewhowdencom/x40.link/storage/memory"
	"github.com/stretchr/testify/require"
	v1alpha "github.com/x40-link/api/gen/x40/link/v1alpha"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestGeneratedGatewaySharesManagementContract(t *testing.T) {
	service := management.New(memory.NewHashTable())
	authorize := func(ctx context.Context, method string) (context.Context, error) {
		if !strings.HasPrefix(method, "/x40.link.v1alpha.ShortLinkService/") {
			return ctx, status.Error(codes.PermissionDenied, "unknown method")
		}
		md, _ := metadata.FromIncomingContext(ctx)
		if len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer valid" {
			return ctx, status.Error(codes.Unauthenticated, "token required")
		}
		return context.WithValue(ctx, storage.CtxKeyAgent, "alice"), nil
	}
	handler, err := api.NewGateway(service, authorize)
	require.NoError(t, err)
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	path := "/v1alpha/domains/example.com/shortLinks"
	unauthorized := request(http.MethodPost, path, `{"path":"/foo","destinationUrl":"https://destination.example/one"}`, "")
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)
	created := request(http.MethodPost, path, `{"path":"/foo","destinationUrl":"https://destination.example/one"}`, "Bearer valid")
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	var link v1alpha.ShortLink
	require.NoError(t, protojson.Unmarshal(created.Body.Bytes(), &link))
	require.Equal(t, "https://example.com/foo", link.ShortUrl)
	got := request(http.MethodGet, path+"/"+strings.TrimPrefix(link.Name, "domains/example.com/shortLinks/"), "", "Bearer valid")
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	patched := request(http.MethodPatch, path+"/"+strings.TrimPrefix(link.Name, "domains/example.com/shortLinks/")+"?updateMask=destinationUrl", `{"destinationUrl":"https://destination.example/two"}`, "Bearer valid")
	require.Equal(t, http.StatusOK, patched.Code, patched.Body.String())
	require.Contains(t, patched.Body.String(), "https://destination.example/two")
	listed := request(http.MethodGet, path, "", "Bearer valid")
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	require.Contains(t, listed.Body.String(), link.Name)
	deleted := request(http.MethodDelete, path+"/"+strings.TrimPrefix(link.Name, "domains/example.com/shortLinks/"), "", "Bearer valid")
	require.Equal(t, http.StatusOK, deleted.Code, deleted.Body.String())
	missing := request(http.MethodGet, path+"/"+strings.TrimPrefix(link.Name, "domains/example.com/shortLinks/"), "", "Bearer valid")
	require.Equal(t, http.StatusNotFound, missing.Code)
}
