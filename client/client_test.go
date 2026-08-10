// Copyright 2023 Cloudbase Solutions SRL
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

package client

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/gophercloud/gophercloud/v2/testhelper/client"
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

func TestCreateServerFromImage(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for server creation
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "POST")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `
		{
		"server": {
			"id": "d9072956-1560-487c-97f2-18bdf65ec749",
			"name": "test-server",
			"status": "ACTIVE",
			"tags": ["garm-controller-id=my-controller-id"]
		}
		}`)
	})

	// Mock the response for server get by ID
	fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"server": {
			"id": "d9072956-1560-487c-97f2-18bdf65ec749",
			"name": "test-server",
			"status": "ACTIVE",
			"tags": ["garm-controller-id=my-controller-id"]
		}
		}`)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}

	tags := []string{"garm-controller-id=my-controller-id"}
	createOpts := servers.CreateOpts{
		Name:      "test-server",
		ImageRef:  "aee1d242-730f-431f-88c1-87630c0f07ba",
		FlavorRef: "flavor-uuid",
		Tags:      tags,
	}

	expectedServer := ServerWithExt{
		Server: servers.Server{
			ID:     "d9072956-1560-487c-97f2-18bdf65ec749",
			Name:   "test-server",
			Status: "ACTIVE",
			Tags:   &tags,
		},
	}

	server, err := osClient.CreateServerFromImage(ctx, createOpts)

	assert.NoError(t, err)
	assert.Equal(t, server, expectedServer)
}

func TestCreateServerFromImageFailed(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for server creation
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "POST")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}

	tags := []string{"garm-controller-id=my-controller-id"}
	createOpts := servers.CreateOpts{
		Name:      "test-server",
		ImageRef:  "aee1d242-730f-431f-88c1-87630c0f07ba",
		FlavorRef: "flavor-uuid",
		Tags:      tags,
	}

	expectedServer := ServerWithExt{}

	server, err := osClient.CreateServerFromImage(ctx, createOpts)

	assert.ErrorContains(t, err, "failed to create server")
	assert.Equal(t, server, expectedServer)
}

func TestCreateServerFromVolume(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for server creation
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "POST")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `
		{
		"server": {
			"id": "d9072956-1560-487c-97f2-18bdf65ec749",
			"name": "test-server",
			"status": "ACTIVE",
			"tags": ["garm-controller-id=my-controller-id"]
		}
		}`)
	})

	// Mock the response for server get by ID
	fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"server": {
			"id": "d9072956-1560-487c-97f2-18bdf65ec749",
			"name": "test-server",
			"status": "ACTIVE",
			"tags": ["garm-controller-id=my-controller-id"]
		}
		}`)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}
	createOpts := servers.CreateOpts{
		Name:      "test-server",
		FlavorRef: "flavor-uuid",
		ImageRef:  "aee1d242-730f-431f-88c1-87630c0f07ba",
		BlockDevice: []servers.BlockDevice{
			{
				BootIndex:           0,
				DeleteOnTermination: true,
				VolumeSize:          100,
				DeviceType:          "disk",
				DestinationType:     servers.DestinationLocal,
				SourceType:          servers.SourceImage,
				UUID:                "",
			},
		},
	}
	expectedServer := ServerWithExt{
		Server: servers.Server{
			ID:     "d9072956-1560-487c-97f2-18bdf65ec749",
			Name:   "test-server",
			Status: "ACTIVE",
			Tags:   &[]string{"garm-controller-id=my-controller-id"},
		},
	}

	server, err := osClient.CreateServerFromVolume(ctx, createOpts, "test-server")
	assert.NoError(t, err)
	assert.Equal(t, expectedServer, server)
}

func TestCreateServerFromVolumeFailed(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for server creation
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "POST")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `
		{
		"server": {
			"id": "d9072956-1560-487c-97f2-18bdf65ec749",
			"name": "test-server",
			"status": "ACTIVE",
			"tags": ["garm-controller-id=my-controller-id"]
		}
		}`)
	})

	// Mock the response for server get by ID
	fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}
	createOpts := servers.CreateOpts{
		Name:      "test-server",
		FlavorRef: "flavor-uuid",
		ImageRef:  "aee1d242-730f-431f-88c1-87630c0f07ba",
		BlockDevice: []servers.BlockDevice{
			{
				BootIndex:           0,
				DeleteOnTermination: true,
				VolumeSize:          100,
				DeviceType:          "disk",
				DestinationType:     servers.DestinationLocal,
				SourceType:          servers.SourceImage,
				UUID:                "",
			},
		},
	}
	expectedServer := ServerWithExt{
		Server: servers.Server{
			ID:     "d9072956-1560-487c-97f2-18bdf65ec749",
			Name:   "test-server",
			Status: "ACTIVE",
			Tags:   &[]string{"garm-controller-id=my-controller-id"},
		},
	}

	server, err := osClient.CreateServerFromVolume(ctx, createOpts, "test-server")
	assert.ErrorContains(t, err, "server did not reach ACTIVE state after 600 seconds")
	assert.Equal(t, expectedServer, server)
}

func TestCreateServerCleansUpAfterContextCancellation(t *testing.T) {
	tests := []struct {
		name   string
		create func(context.Context, *OpenstackClient) (ServerWithExt, error)
	}{
		{
			name: "image",
			create: func(ctx context.Context, osClient *OpenstackClient) (ServerWithExt, error) {
				return osClient.CreateServerFromImage(ctx, servers.CreateOpts{
					Name:      "test-server",
					ImageRef:  "image-id",
					FlavorRef: "flavor-id",
					Tags:      []string{"garm-controller-id=my-controller-id"},
				})
			},
		},
		{
			name: "volume",
			create: func(ctx context.Context, osClient *OpenstackClient) (ServerWithExt, error) {
				return osClient.CreateServerFromVolume(ctx, servers.CreateOpts{
					Name:      "test-server",
					FlavorRef: "flavor-id",
					Tags:      []string{"garm-controller-id=my-controller-id"},
					BlockDevice: []servers.BlockDevice{{
						BootIndex:           0,
						DeleteOnTermination: true,
						DestinationType:     servers.DestinationVolume,
						SourceType:          servers.SourceVolume,
						UUID:                "volume-id",
					}},
				}, "test-server")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeServer := testhelper.SetupHTTP()
			defer fakeServer.Teardown()

			ctx, cancel := context.WithCancel(context.Background())
			var getRequests atomic.Int32
			var deleted atomic.Bool
			deleteCalled := make(chan struct{}, 1)

			fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
				testhelper.TestMethod(t, r, http.MethodPost)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				fmt.Fprint(w, `{"server":{"id":"d9072956-1560-487c-97f2-18bdf65ec749","name":"test-server","status":"BUILD","tags":["garm-controller-id=my-controller-id"]}}`)
			})
			fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749", func(w http.ResponseWriter, r *http.Request) {
				testhelper.TestMethod(t, r, http.MethodGet)
				if deleted.Load() {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if getRequests.Add(1) == 1 {
					cancel()
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"server":{"id":"d9072956-1560-487c-97f2-18bdf65ec749","name":"test-server","status":"BUILD","tags":["garm-controller-id=my-controller-id"]}}`)
			})
			fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749/action", func(w http.ResponseWriter, r *http.Request) {
				testhelper.TestMethod(t, r, http.MethodPost)
				deleted.Store(true)
				deleteCalled <- struct{}{}
				w.WriteHeader(http.StatusAccepted)
			})

			osClient := &OpenstackClient{
				compute:      client.ServiceClient(fakeServer),
				controllerID: "my-controller-id",
			}
			_, err := tt.create(ctx, osClient)
			assert.Error(t, err)

			select {
			case <-deleteCalled:
			case <-time.After(time.Second):
				t.Fatal("server was not deleted after context cancellation")
			}
		})
	}
}

func TestCreateBootVolumeCleansUpAfterContextCancellation(t *testing.T) {
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	ctx, cancel := context.WithCancel(context.Background())
	deleteCalled := make(chan struct{}, 1)

	fakeServer.Mux.HandleFunc("/volumes", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, http.MethodPost)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"volume":{"id":"volume-id","status":"creating"}}`)
	})
	fakeServer.Mux.HandleFunc("/volumes/volume-id", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			cancel()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"volume":{"id":"volume-id","status":"creating"}}`)
		case http.MethodDelete:
			deleteCalled <- struct{}{}
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})

	osClient := &OpenstackClient{volume: client.ServiceClient(fakeServer)}
	_, err := osClient.CreateBootVolume(ctx, "root", "image-id", "fast", "az1", 20)
	assert.Error(t, err)

	select {
	case <-deleteCalled:
	case <-time.After(time.Second):
		t.Fatal("boot volume was not deleted after context cancellation")
	}
}

func TestGetServer(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for server get by tags
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"servers": [
			{
				"id": "d9072956-1560-487c-97f2-18bdf65ec749",
				"name": "test-server",
				"status": "ACTIVE",
				"tags": ["garm-controller-id=my-controller-id"]
			}
		]
		}`)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}

	expectedServer := ServerWithExt{
		Server: servers.Server{
			ID:     "d9072956-1560-487c-97f2-18bdf65ec749",
			Name:   "test-server",
			Status: "ACTIVE",
			Tags:   &[]string{"garm-controller-id=my-controller-id"},
		},
	}

	server, err := osClient.GetServer(ctx, "test-server")
	assert.NoError(t, err)
	assert.Equal(t, expectedServer, server)
}

func TestListServersWithTags(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for server list
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"servers": [
			{
				"id": "d9072956-1560-487c-97f2-18bdf65ec749",
				"name": "test-server",
				"status": "ACTIVE",
				"tags": ["garm-controller-id=my-controller-id"]
			},
			{
				"id": "d9072956-1560-487c-10f2-18bdf65ec749",
				"name": "test-server-2",
				"status": "ACTIVE",
				"tags": ["garm-controller-id=my-controller-id"]
			}
		]
		}`)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}

	expectedServers := []ServerWithExt{
		{
			Server: servers.Server{
				ID:     "d9072956-1560-487c-97f2-18bdf65ec749",
				Name:   "test-server",
				Status: "ACTIVE",
				Tags:   &[]string{"garm-controller-id=my-controller-id"},
			},
		},
		{
			Server: servers.Server{
				ID:     "d9072956-1560-487c-10f2-18bdf65ec749",
				Name:   "test-server-2",
				Status: "ACTIVE",
				Tags:   &[]string{"garm-controller-id=my-controller-id"},
			},
		},
	}

	servers, err := osClient.ListServersWithTags(ctx, []string{"garm-controller-id=my-controller-id"})
	assert.NoError(t, err)
	assert.Equal(t, expectedServers, servers)
}

func TestListServers(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for server get by pool-id tags
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"servers": [
			{
				"id": "d9072956-1560-487c-97f2-18bdf65ec749",
				"name": "test-server",
				"status": "ACTIVE",
				"tags": ["garm-controller-id=my-controller-id",
				"garm-pool-id=my-pool-id"]
			},
			{
				"id": "d9072956-1560-487c-10f2-18bdf65ec749",
				"name": "test-server-2",
				"status": "ACTIVE",
				"tags": ["garm-controller-id=my-controller-id",
				"garm-pool-id=my-pool-id"]
			}
		]
		}`)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}

	expectedServer := []ServerWithExt{
		{
			Server: servers.Server{
				ID:     "d9072956-1560-487c-97f2-18bdf65ec749",
				Name:   "test-server",
				Status: "ACTIVE",
				Tags: &[]string{
					"garm-controller-id=my-controller-id",
					"garm-pool-id=my-pool-id",
				},
			},
		},
		{
			Server: servers.Server{
				ID:     "d9072956-1560-487c-10f2-18bdf65ec749",
				Name:   "test-server-2",
				Status: "ACTIVE",
				Tags: &[]string{
					"garm-controller-id=my-controller-id",
					"garm-pool-id=my-pool-id",
				},
			},
		},
	}

	server, err := osClient.ListServers(ctx, "my-pool-id")

	assert.NoError(t, err)
	assert.Equal(t, expectedServer, server)
}

func TestDeleteServer(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for server get by ID
	fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"server": {
			"id": "d9072956-1560-487c-97f2-18bdf65ec749",
			"name": "test-server",
			"status": "DELETED",
			"tags": ["garm-controller-id=my-controller-id"],
			"forceDelete": true
		}
		}`)
	})

	// Mock the response for server deletion
	fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749/action", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "POST")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `
		{
		"server": {
			"id": "d9072956-1560-487c-97f2-18bdf65ec749",
			"name": "test-server",
			"status": "DELETED",
			"tags": ["garm-controller-id=my-controller-id"],
			"forceDelete": true
		}
		}`)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}

	err := osClient.DeleteServer(ctx, "d9072956-1560-487c-97f2-18bdf65ec749", true)
	assert.NoError(t, err)
}

func TestDeleteServerWithoutWaiting(t *testing.T) {
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	var getRequests atomic.Int32
	fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, http.MethodGet)
		if getRequests.Add(1) > 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"server":{"id":"d9072956-1560-487c-97f2-18bdf65ec749","name":"test-server","status":"ACTIVE","tags":["garm-controller-id=my-controller-id"]}}`)
	})
	fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749/action", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, http.MethodPost)
		w.WriteHeader(http.StatusAccepted)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}
	err := osClient.DeleteServer(context.Background(), "d9072956-1560-487c-97f2-18bdf65ec749", false)
	assert.NoError(t, err)
	assert.EqualValues(t, 1, getRequests.Load())
}

func TestDeleteServerNotFound(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for server get by ID
	fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}

	err := osClient.DeleteServer(ctx, "d9072956-1560-487c-97f2-18bdf65ec749", true)
	assert.NoError(t, err)
}

func TestGetFlavorWithID(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for flavor get by ID
	fakeServer.Mux.HandleFunc("/flavors/flavor-uuid", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"flavor": {
			"id": "flavor-uuid",
			"name": "test-flavor",
			"ram": 1024,
			"vcpus": 1,
			"disk": 10
		}
		}`)
	})

	osClient := &OpenstackClient{
		compute: client.ServiceClient(fakeServer),
	}

	expectedFlavor := flavors.Flavor{
		ID:    "flavor-uuid",
		Name:  "test-flavor",
		RAM:   1024,
		VCPUs: 1,
		Disk:  10,
	}

	flavor, err := osClient.GetFlavor(ctx, "flavor-uuid")
	assert.NoError(t, err)
	assert.Equal(t, expectedFlavor, *flavor)
}

func TestGetFlavorWithName(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for flavor list
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"flavors": [
			{
				"id": "flavor-uuid",
				"name": "test-flavor",
				"ram": 1024,
				"vcpus": 1,
				"disk": 10
			}
		]
		}`)
	})

	osClient := &OpenstackClient{
		compute: client.ServiceClient(fakeServer),
	}

	expectedFlavor := flavors.Flavor{
		ID:    "flavor-uuid",
		Name:  "test-flavor",
		RAM:   1024,
		VCPUs: 1,
		Disk:  10,
	}

	flavor, err := osClient.GetFlavor(ctx, "test-flavor")
	assert.NoError(t, err)
	assert.Equal(t, expectedFlavor, *flavor)
}

func TestGetImageWithID(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for image get by ID
	fakeServer.Mux.HandleFunc("/images/aee1d242-730f-431f-88c1-87630c0f07ba", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"name": "test-image",
		"id": "aee1d242-730f-431f-88c1-87630c0f07ba",
		"status": "ACTIVE"
		}`)
	})

	osClient := &OpenstackClient{
		image: client.ServiceClient(fakeServer),
	}

	expectedImage := images.Image{
		ID:         "aee1d242-730f-431f-88c1-87630c0f07ba",
		Name:       "test-image",
		Properties: map[string]interface{}{},
		Status:     "ACTIVE",
	}

	image, err := osClient.GetImage(ctx, "aee1d242-730f-431f-88c1-87630c0f07ba", "")
	assert.NoError(t, err)
	assert.Equal(t, expectedImage, *image)
}

func TestGetImageWithName(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for image list
	fakeServer.Mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"images": [
			{
				"name": "test-image",
				"id": "aee1d242-730f-431f-88c1-87630c0f07ba",
				"status": "ACTIVE",
				"visibility": "public"
			}
		]
		}`)
	})

	osClient := &OpenstackClient{
		image: client.ServiceClient(fakeServer),
	}

	expectedImage := images.Image{
		ID:         "aee1d242-730f-431f-88c1-87630c0f07ba",
		Name:       "test-image",
		Visibility: "public",
		Properties: map[string]interface{}{},
		Status:     "ACTIVE",
	}

	image, err := osClient.GetImage(ctx, "test-image", "")
	assert.NoError(t, err)
	assert.Equal(t, expectedImage, *image)
}

func TestGetNetworkWithID(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for network get by ID
	fakeServer.Mux.HandleFunc("/networks/aee1d242-730f-431f-88c1-87630c0f20ca", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"network": {
			"id": "aee1d242-730f-431f-88c1-87630c0f20ca",
			"name": "test-network",
			"status": "ACTIVE"
		}
		}`)
	})

	osClient := &OpenstackClient{
		network: client.ServiceClient(fakeServer),
	}

	expectedNetwork := networks.Network{
		ID:     "aee1d242-730f-431f-88c1-87630c0f20ca",
		Name:   "test-network",
		Status: "ACTIVE",
	}

	network, err := osClient.GetNetwork(ctx, "aee1d242-730f-431f-88c1-87630c0f20ca")
	assert.NoError(t, err)
	assert.Equal(t, expectedNetwork, *network)
}

func TestGetNetworkWithName(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for network list
	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"networks": [
			{
				"id": "aee1d242-730f-431f-88c1-87630c0f20ca",
				"name": "test-network",
				"status": "ACTIVE"
			}
		]
		}`)
	})

	osClient := &OpenstackClient{
		network: client.ServiceClient(fakeServer),
	}

	expectedNetwork := networks.Network{
		ID:     "aee1d242-730f-431f-88c1-87630c0f20ca",
		Name:   "test-network",
		Status: "ACTIVE",
	}

	network, err := osClient.GetNetwork(ctx, "test-network")
	assert.NoError(t, err)
	assert.Equal(t, expectedNetwork, *network)
}

func TestStopServer(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for server get by ID
	fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"server": {
			"id": "d9072956-1560-487c-97f2-18bdf65ec749",
			"name": "test-server",
			"status": "ACTIVE",
			"tags": ["garm-controller-id=my-controller-id"]
		}
		}`)
	})

	// Mock the response for server stop
	fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749/action", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "POST")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `
		{
		"server": {
			"id": "d9072956-1560-487c-97f2-18bdf65ec749",
			"name": "test-server",
			"status": "SHUTOFF",
			"tags": ["garm-controller-id=my-controller-id"]
		}
		}`)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}

	err := osClient.StopServer(ctx, "d9072956-1560-487c-97f2-18bdf65ec749")
	assert.NoError(t, err)
}

func TestStopServerNotFound(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for server get by ID
	fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}

	err := osClient.StopServer(ctx, "d9072956-1560-487c-97f2-18bdf65ec749")
	assert.ErrorContains(t, err, "failed to get server")
}

func TestStartServer(t *testing.T) {
	ctx := context.Background()
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	// Mock the response for server get by ID
	fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "GET")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		{
		"server": {
			"id": "d9072956-1560-487c-97f2-18bdf65ec749",
			"name": "test-server",
			"status": "SHUTOFF",
			"tags": ["garm-controller-id=my-controller-id"]
		}
		}`)
	})

	// Mock the response for server start
	fakeServer.Mux.HandleFunc("/servers/d9072956-1560-487c-97f2-18bdf65ec749/action", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, "POST")
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `
		{
		"server": {
			"id": "d9072956-1560-487c-97f2-18bdf65ec749",
			"name": "test-server",
			"status": "ACTIVE",
			"tags": ["garm-controller-id=my-controller-id"]
		}
		}`)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}

	err := osClient.StartServer(ctx, "d9072956-1560-487c-97f2-18bdf65ec749")
	assert.NoError(t, err)
}
