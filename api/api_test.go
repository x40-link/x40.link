package api_test

import (
	"testing"

	"github.com/andrewhowdencom/x40.link/api"
	"github.com/andrewhowdencom/x40.link/storage/memory"
	"github.com/stretchr/testify/assert"
	v1alpha "github.com/x40-link/api/gen/x40/link/v1alpha"
)

func TestV1AlphaRegistrationAndPermissions(t *testing.T) {
	permissions := api.X40Permissions()
	for _, method := range []string{"CreateShortLink", "GetShortLink", "ListShortLinks", "UpdateShortLink", "DeleteShortLink"} {
		assert.Equal(t, "api.x40.link/scopes/x40.link.v1alpha.ShortLinkService."+method,
			permissions["/x40.link.v1alpha.ShortLinkService/"+method])
	}
	assert.Len(t, permissions, 5)
	services := api.NewGRPCMux(memory.NewHashTable()).GetServiceInfo()
	assert.Contains(t, services, v1alpha.ShortLinkService_ServiceDesc.ServiceName)
	assert.NotContains(t, services, "x40.dev.url.ManageURLs")
}
