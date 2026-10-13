package client

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/stretchr/testify/assert"

	"github.com/cloudbase/garm-provider-openstack/config"
)

func TestNewClientAuthenticatesOnceAndSharesProvider(t *testing.T) {
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()
	t.Setenv("OS_REGION_NAME", "RegionTwo")
	t.Setenv("OS_INTERFACE", "internal")

	var authRequests atomic.Int32
	fakeServer.Mux.HandleFunc("/v3/auth/tokens", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, http.MethodPost)
		authRequests.Add(1)
		w.Header().Set("X-Subject-Token", "provider-token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{
			"token": {
				"methods": ["password"],
				"expires_at": "2035-01-01T00:00:00Z",
				"user": {"id":"user-id","name":"user","domain":{"id":"default","name":"Default"}},
				"project": {"id":"project-id","name":"project","domain":{"id":"default","name":"Default"}},
				"catalog": [
					{"type":"compute","name":"nova","endpoints":[{"interface":"internal","region":"RegionTwo","url":%q}]},
					{"type":"image","name":"glance","endpoints":[{"interface":"internal","region":"RegionTwo","url":%q}]},
					{"type":"network","name":"neutron","endpoints":[{"interface":"internal","region":"RegionTwo","url":%q}]},
					{"type":"volumev2","name":"cinderv2","endpoints":[{"interface":"internal","region":"RegionTwo","url":%q}]}
				]
			}
		}`, fakeServer.Endpoint()+"compute/v2.1", fakeServer.Endpoint()+"image/v2", fakeServer.Endpoint()+"network/v2.0", fakeServer.Endpoint()+"volume/v2/project-id")
	})

	cloudsPath := filepath.Join(t.TempDir(), "clouds.yaml")
	cloudsYAML := fmt.Sprintf(`clouds:
  test:
    volume_api_version: 2
    auth_type: v3password
    auth:
      auth_url: %s
      username: user
      password: password
      project_id: project-id
      user_domain_id: default
`, fakeServer.Endpoint()+"v3/")
	assert.NoError(t, os.WriteFile(cloudsPath, []byte(cloudsYAML), 0o600))

	openstackClient, err := NewClient(context.Background(), &config.Config{
		Cloud:            "test",
		Credentials:      config.Credentials{Clouds: cloudsPath},
		DefaultNetworkID: "network-id",
	}, "controller-id")
	assert.NoError(t, err)
	if !assert.NotNil(t, openstackClient) {
		return
	}
	assert.EqualValues(t, 1, authRequests.Load(), "all service clients must share one authentication")
	assert.Same(t, openstackClient.compute.ProviderClient, openstackClient.image.ProviderClient)
	assert.Same(t, openstackClient.compute.ProviderClient, openstackClient.network.ProviderClient)
	assert.Same(t, openstackClient.compute.ProviderClient, openstackClient.volume.ProviderClient)
	assert.Equal(t, fakeServer.Endpoint()+"volume/v2/project-id/", openstackClient.volume.Endpoint)
}
