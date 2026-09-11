// Copyright 2026 Cloudbase Solutions SRL
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

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cloudbase/garm-provider-common/params"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/stretchr/testify/assert"

	"github.com/cloudbase/garm-provider-openstack/client"
	"github.com/cloudbase/garm-provider-openstack/config"
)

func TestCreateInstanceCleansUpFailedBootFromVolume(t *testing.T) {
	for _, tt := range []createCleanupScenario{
		{"Nova rejects create", false, false, 0},
		{"Nova rejects create and volume cleanup fails", false, true, 0},
		{"attached server fails", true, false, 0},
		{"attached server and cleanup fail", true, true, 0},
		{"slow detach", true, false, 31 * time.Second},
		{"detach exceeds cleanup budget", true, false, 6 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				api := &createCleanupAPI{t: t, scenario: tt}

				tool := params.RunnerApplicationDownload{OS: Ptr("linux"), Architecture: Ptr("x64"), DownloadURL: Ptr("http://example.com/runner"), Filename: Ptr("runner.tar.gz"), SHA256Checksum: Ptr("checksum")}
				previousFetch := DefaultToolFetch
				DefaultToolFetch = func(params.OSType, params.OSArch, []params.RunnerApplicationDownload) (params.RunnerApplicationDownload, error) {
					return tool, nil
				}
				t.Cleanup(func() { DefaultToolFetch = previousFetch })
				serviceClient := &gophercloud.ServiceClient{
					Endpoint: "http://openstack.test/",
					ProviderClient: &gophercloud.ProviderClient{
						TokenID: "token",
						HTTPClient: http.Client{Transport: cleanupRoundTripFunc(func(r *http.Request) (*http.Response, error) {
							if err := r.Context().Err(); err != nil {
								return nil, err
							}
							// Avoid network I/O so synctest can advance the cleanup timers.
							recorder := httptest.NewRecorder()
							api.ServeHTTP(recorder, r)
							response := recorder.Result()
							response.Request = r
							return response, nil
						})},
					},
				}
				provider := &openstackProvider{
					cfg:          &config.Config{DefaultNetworkID: cleanupNetworkID},
					cli:          client.NewTestOpenStackClient(serviceClient, "controller"),
					controllerID: "controller",
				}
				instance, err := provider.CreateInstance(context.Background(), params.BootstrapInstance{
					Name: "runner", Flavor: "small", Image: "linux", OSType: params.Linux, OSArch: params.Amd64,
					ExtraSpecs: json.RawMessage(`{"boot_from_volume":true,"boot_disk_size":20}`),
				})
				if !assert.Error(t, err) {
					return
				}
				assert.Empty(t, instance.ProviderID)
				assert.EqualValues(t, 1, api.volumeCreates.Load())
				if tt.detachDelay > 5*time.Minute {
					assert.Zero(t, api.volumeDeletes.Load())
					assert.ErrorIs(t, err, context.DeadlineExceeded)
					assert.Equal(t, 5*time.Minute, time.Since(api.cleanupStarted))
				} else {
					assert.EqualValues(t, 1, api.volumeDeletes.Load())
					if tt.detachDelay > 0 {
						assert.GreaterOrEqual(t, time.Since(api.cleanupStarted), tt.detachDelay)
					}
				}
				if tt.serverCreated {
					assert.ErrorContains(t, err, "instance in ERROR state")
					assert.EqualValues(t, 1, api.serverDeletes.Load())
				} else {
					assert.ErrorContains(t, err, "Nova rejected create")
					assert.Zero(t, api.serverDeletes.Load())
				}
				if tt.cleanupFails {
					assert.ErrorContains(t, err, "volume cleanup failed")
					assert.ErrorContains(t, err, "volume-id")
					if tt.serverCreated {
						assert.ErrorContains(t, err, "server cleanup failed")
					}
				} else if tt.detachDelay <= 5*time.Minute {
					assert.NotContains(t, err.Error(), "failed to clean up")
				}
			})
		})
	}
}

type cleanupRoundTripFunc func(*http.Request) (*http.Response, error)

func (f cleanupRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

const (
	cleanupServerID  = "d9072956-1560-487c-97f2-18bdf65ec749"
	cleanupNetworkID = "542b68dd-4b3d-459d-8531-34d5e779d4d6"
)

type createCleanupScenario struct {
	name          string
	serverCreated bool
	cleanupFails  bool
	detachDelay   time.Duration
}

type createCleanupAPI struct {
	t        *testing.T
	scenario createCleanupScenario

	cleanupStarted  time.Time
	createAttempted atomic.Bool
	serverDeletes   atomic.Int32
	volumeCreates   atomic.Int32
	volumeDeletes   atomic.Int32
	cleanupPolls    atomic.Int32
	deletePolls     atomic.Int32
}

func (a *createCleanupAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/flavors/small":
		w.WriteHeader(http.StatusNotFound)
	case "/flavors/detail":
		fmt.Fprint(w, `{"flavors":[{"id":"flavor-id","name":"small","disk":10}]}`)
	case "/networks/" + cleanupNetworkID:
		fmt.Fprintf(w, `{"network":{"id":%q}}`, cleanupNetworkID)
	case "/images":
		fmt.Fprint(w, `{"images":[{"id":"image-id","name":"linux","status":"active"}]}`)
	case "/volumes":
		assert.Equal(a.t, http.MethodPost, r.Method)
		a.volumeCreates.Add(1)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"volume":{"id":"volume-id","status":"available"}}`)
	case "/volumes/volume-id":
		if r.Method == http.MethodDelete {
			a.volumeDeletes.Add(1)
			if a.scenario.cleanupFails {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":"volume cleanup failed"}`)
				return
			}
			assert.True(a.t, a.createAttempted.Load())
			if a.scenario.serverCreated && a.cleanupPolls.Load() < 3 {
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		state := "available"
		if a.createAttempted.Load() && a.scenario.serverCreated && !a.scenario.cleanupFails {
			assert.EqualValues(a.t, 1, a.serverDeletes.Load())
			switch a.cleanupPolls.Add(1) {
			case 1:
				state = "in-use"
			case 2:
				state = "detaching"
			}
		}
		if a.scenario.detachDelay > 0 && a.createAttempted.Load() {
			if a.cleanupStarted.IsZero() {
				a.cleanupStarted = time.Now()
			}
			if time.Since(a.cleanupStarted) < a.scenario.detachDelay {
				state = "detaching"
			}
		}
		fmt.Fprintf(w, `{"volume":{"id":"volume-id","status":%q}}`, state)
	case "/servers":
		assert.Equal(a.t, http.MethodPost, r.Method)
		a.createAttempted.Store(true)
		if !a.scenario.serverCreated {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, `{"error":"Nova rejected create"}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"server":{"id":%q}}`, cleanupServerID)
	case "/servers/detail":
		assert.False(a.t, a.scenario.serverCreated)
		fmt.Fprint(w, `{"servers":[]}`)
	case "/servers/" + cleanupServerID:
		if a.serverDeletes.Load() > 0 && a.deletePolls.Add(1) > 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"server":{"id":%q,"status":"ERROR","tags":["garm-controller-id=controller"]}}`, cleanupServerID)
	case "/servers/" + cleanupServerID + "/action":
		assert.Equal(a.t, http.MethodPost, r.Method)
		a.serverDeletes.Add(1)
		if a.scenario.cleanupFails {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":"server cleanup failed"}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	default:
		a.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}
