package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/stretchr/testify/assert"
)

type volumeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f volumeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCreateBootVolumeRecoversLostResponse(t *testing.T) {
	for _, mode := range []string{"cancellation", "missing ID", "gateway error"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var metadata map[string]string
				var queries, deletes int
				transport := volumeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					w := httptest.NewRecorder()
					w.Header().Set("Content-Type", "application/json")
					switch r.Method + " " + r.URL.Path {
					case "POST /volumes":
						var body struct {
							Volume volumes.CreateOpts `json:"volume"`
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Fatal(err)
						}
						metadata = body.Volume.Metadata
						assert.Equal(t, "controller", metadata["garm-controller-id"])
						assert.NotEmpty(t, metadata["garm-create-id"])
						if mode == "cancellation" {
							cancel()
							return nil, context.Canceled
						}
						if mode == "missing ID" {
							w.WriteHeader(http.StatusAccepted)
							fmt.Fprint(w, `{"volume":{}}`)
						} else {
							w.WriteHeader(http.StatusBadGateway)
						}
					case "GET /volumes/detail":
						assert.NoError(t, r.Context().Err(), "recovery must outlive cancellation")
						assert.Equal(t, "runner-root", r.URL.Query().Get("name"))
						queries++
						if queries == 1 {
							fmt.Fprint(w, `{"volumes":[]}`)
						} else {
							json.NewEncoder(w).Encode(map[string]any{"volumes": []volumes.Volume{
								{ID: "unowned", Name: "runner-root", Status: "available"},
								{ID: "other-attempt", Name: "runner-root", Metadata: map[string]string{"garm-controller-id": "controller", "garm-create-id": "other"}},
								{ID: "owned", Name: "runner-root", Status: "available", Metadata: metadata},
							}})
						}
					case "GET /volumes/owned":
						fmt.Fprint(w, `{"volume":{"id":"owned","status":"available"}}`)
					case "DELETE /volumes/owned":
						deletes++
						w.WriteHeader(http.StatusAccepted)
					default:
						t.Errorf("unexpected request: %s %s", r.Method, r.URL)
						w.WriteHeader(http.StatusNotFound)
					}
					response := w.Result()
					response.Request = r
					return response, nil
				})
				cli := &OpenstackClient{controllerID: "controller", volume: &gophercloud.ServiceClient{Endpoint: "http://cinder.test/", ProviderClient: &gophercloud.ProviderClient{HTTPClient: http.Client{Transport: transport}}}}
				vol, err := cli.CreateBootVolume(ctx, "runner-root", "image", "fast", "az", 20)
				switch mode {
				case "cancellation":
					assert.ErrorIs(t, err, context.Canceled)
				case "missing ID":
					assert.ErrorContains(t, err, "no ID")
				case "gateway error":
					assert.True(t, gophercloud.ResponseCodeIs(err, http.StatusBadGateway))
				}
				assert.Nil(t, vol)
				assert.Equal(t, 1, deletes)
				assert.Equal(t, 2, queries, "retry an empty lookup after an ambiguous create")
			})
		})
	}
}

func TestVolumePollingInterval(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprint(cleanup), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var polls []time.Time
				transport := volumeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					polls = append(polls, time.Now())
					state, code := volumeCreating, http.StatusOK
					if len(polls) == 2 {
						time.Sleep(12 * time.Second)
					}
					if len(polls) == 3 {
						state = "available"
						if cleanup {
							code = http.StatusNotFound
						}
					}
					return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"volume":{"id":"volume","status":%q}}`, state)))}, nil
				})
				cli := &OpenstackClient{volume: &gophercloud.ServiceClient{Endpoint: "http://cinder.test/", ProviderClient: &gophercloud.ProviderClient{HTTPClient: http.Client{Transport: transport}}}}
				var err error
				if cleanup {
					err = cli.CleanupVolume(context.Background(), "volume")
				} else {
					err = cli.waitForVolumeStatus(context.Background(), "volume", "available", 60)
				}
				assert.NoError(t, err)
				if assert.Len(t, polls, 3) {
					assert.Equal(t, 5*time.Second, polls[1].Sub(polls[0]))
					assert.Equal(t, 17*time.Second, polls[2].Sub(polls[1]))
				}
			})
		})
	}
}
