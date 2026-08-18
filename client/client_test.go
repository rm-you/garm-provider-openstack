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
	"sync/atomic"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/gophercloud/gophercloud/v2/testhelper/client"
	"github.com/stretchr/testify/assert"
)

func TestCreateServerFromImage(t *testing.T) {
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

	server, err := osClient.CreateServerFromImage(context.Background(), createOpts)

	assert.NoError(t, err)
	assert.Equal(t, server, expectedServer)
}

func TestCreateServerFromImageFailed(t *testing.T) {
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

	server, err := osClient.CreateServerFromImage(context.Background(), createOpts)

	assert.ErrorContains(t, err, "failed to create server")
	assert.Equal(t, server, expectedServer)
}

func TestCreateServerFromImageCleansUpAfterCancellation(t *testing.T) {
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const serverID = "d9072956-1560-487c-97f2-18bdf65ec749"
	var gets atomic.Int32
	var deleted atomic.Bool

	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, http.MethodPost)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"server":{"id":%q}}`, serverID)
	})
	fakeServer.Mux.HandleFunc("/servers/"+serverID, func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, http.MethodGet)
		if gets.Add(1) == 1 {
			cancel()
			return
		}
		if deleted.Load() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"server":{"id":%q,"status":"ACTIVE","tags":["garm-controller-id=my-controller-id"]}}`, serverID)
	})
	fakeServer.Mux.HandleFunc("/servers/"+serverID+"/action", func(w http.ResponseWriter, r *http.Request) {
		testhelper.TestMethod(t, r, http.MethodPost)
		deleted.Store(true)
		w.WriteHeader(http.StatusAccepted)
	})

	osClient := &OpenstackClient{
		compute:      client.ServiceClient(fakeServer),
		controllerID: "my-controller-id",
	}
	_, err := osClient.CreateServerFromImage(ctx, servers.CreateOpts{
		Name:      "test-server",
		ImageRef:  "aee1d242-730f-431f-88c1-87630c0f07ba",
		FlavorRef: "flavor-uuid",
	})
	assert.Error(t, err)

	assert.True(t, deleted.Load(), "server was not deleted after caller cancellation")
}

func TestCreateServerFromVolume(t *testing.T) {
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

	server, err := osClient.CreateServerFromVolume(context.Background(), createOpts, "test-server")
	assert.NoError(t, err)
	assert.Equal(t, expectedServer, server)
}

func TestCreateServerFromVolumeFailed(t *testing.T) {
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

	server, err := osClient.CreateServerFromVolume(context.Background(), createOpts, "test-server")
	assert.ErrorContains(t, err, "server did not reach ACTIVE state after 120 seconds")
	assert.Equal(t, expectedServer, server)
}

func TestGetServer(t *testing.T) {
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

	server, err := osClient.GetServer(context.Background(), "test-server")
	assert.NoError(t, err)
	assert.Equal(t, expectedServer, server)
}

func TestListServersWithTags(t *testing.T) {
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

	servers, err := osClient.ListServersWithTags(context.Background(), []string{"garm-controller-id=my-controller-id"})
	assert.NoError(t, err)
	assert.Equal(t, expectedServers, servers)
}

func TestListServers(t *testing.T) {
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

	server, err := osClient.ListServers(context.Background(), "my-pool-id")

	assert.NoError(t, err)
	assert.Equal(t, expectedServer, server)
}

func TestDeleteServer(t *testing.T) {
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

	err := osClient.DeleteServer(context.Background(), "d9072956-1560-487c-97f2-18bdf65ec749", true)
	assert.NoError(t, err)
}

func TestDeleteServerNotFound(t *testing.T) {
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

	err := osClient.DeleteServer(context.Background(), "d9072956-1560-487c-97f2-18bdf65ec749", true)
	assert.NoError(t, err)
}

func TestGetFlavorWithID(t *testing.T) {
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

	flavor, err := osClient.GetFlavor(context.Background(), "flavor-uuid")
	assert.NoError(t, err)
	assert.Equal(t, expectedFlavor, *flavor)
}

func TestGetFlavorWithName(t *testing.T) {
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

	flavor, err := osClient.GetFlavor(context.Background(), "test-flavor")
	assert.NoError(t, err)
	assert.Equal(t, expectedFlavor, *flavor)
}

func TestGetImageWithID(t *testing.T) {
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

	image, err := osClient.GetImage(context.Background(), "aee1d242-730f-431f-88c1-87630c0f07ba", "")
	assert.NoError(t, err)
	assert.Equal(t, expectedImage, *image)
}

func TestGetImageWithName(t *testing.T) {
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

	image, err := osClient.GetImage(context.Background(), "test-image", "")
	assert.NoError(t, err)
	assert.Equal(t, expectedImage, *image)
}

func TestGetNetworkWithID(t *testing.T) {
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

	network, err := osClient.GetNetwork(context.Background(), "aee1d242-730f-431f-88c1-87630c0f20ca")
	assert.NoError(t, err)
	assert.Equal(t, expectedNetwork, *network)
}

func TestGetNetworkWithName(t *testing.T) {
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

	network, err := osClient.GetNetwork(context.Background(), "test-network")
	assert.NoError(t, err)
	assert.Equal(t, expectedNetwork, *network)
}

func TestStopServer(t *testing.T) {
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

	err := osClient.StopServer(context.Background(), "d9072956-1560-487c-97f2-18bdf65ec749")
	assert.NoError(t, err)
}

func TestStopServerNotFound(t *testing.T) {
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

	err := osClient.StopServer(context.Background(), "d9072956-1560-487c-97f2-18bdf65ec749")
	assert.ErrorContains(t, err, "failed to get server")
}

func TestStartServer(t *testing.T) {
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

	err := osClient.StartServer(context.Background(), "d9072956-1560-487c-97f2-18bdf65ec749")
	assert.NoError(t, err)
}
