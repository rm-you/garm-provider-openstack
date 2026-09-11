package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/stretchr/testify/assert"
)

func TestCreateBootVolumeRecoveryFailures(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		createCode, listCode int
		wantLookup           bool
	}{
		{"rejected create", http.StatusForbidden, 0, false},
		{"recovery lookup fails", 0, http.StatusForbidden, true},
		{"volume never appears", 0, http.StatusOK, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				queries := 0
				transport := volumeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					w := httptest.NewRecorder()
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodPost {
						if tt.createCode == 0 {
							return nil, io.ErrUnexpectedEOF
						}
						w.WriteHeader(tt.createCode)
					} else {
						queries++
						w.WriteHeader(tt.listCode)
						fmt.Fprint(w, `{"volumes":[]}`)
					}
					response := w.Result()
					response.Request = r
					return response, nil
				})
				cli := &OpenstackClient{controllerID: "controller", volume: &gophercloud.ServiceClient{Endpoint: "http://cinder.test/", ProviderClient: &gophercloud.ProviderClient{HTTPClient: http.Client{Transport: transport}}}}
				_, err := cli.CreateBootVolume(context.Background(), "runner-root", "image", "fast", "az", 20)
				assert.Error(t, err)
				assert.Equal(t, tt.wantLookup, queries > 0)
				if tt.createCode == 0 {
					assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
					assert.ErrorContains(t, err, "create ID")
					if tt.listCode == http.StatusOK {
						assert.ErrorIs(t, err, context.DeadlineExceeded)
						assert.Equal(t, 5*time.Minute, time.Since(start))
					}
				} else {
					assert.True(t, gophercloud.ResponseCodeIs(err, tt.createCode))
					assert.Equal(t, time.Duration(0), time.Since(start))
				}
			})
		})
	}
}

func TestCreateBootVolumeAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cli := &OpenstackClient{}
	_, err := cli.CreateBootVolume(ctx, "root", "image", "", "", 10)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestCleanupBootVolumesContinuesAfterFailure(t *testing.T) {
	var deletes []string
	transport := volumeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/volumes/detail":
			fmt.Fprint(w, `{"volumes":[
				{"id":"first","name":"runner-root","metadata":{"garm-controller-id":"controller","garm-create-id":"request1"}},
				{"id":"second","name":"runner-root","metadata":{"garm-controller-id":"controller","garm-create-id":"request2"}}
			]}`)
		case r.Method == http.MethodDelete:
			deletes = append(deletes, r.URL.Path)
			if strings.HasSuffix(r.URL.Path, "first") {
				w.WriteHeader(http.StatusServiceUnavailable)
			} else {
				w.WriteHeader(http.StatusAccepted)
			}
		default:
			fmt.Fprint(w, `{"volume":{"status":"available"}}`)
		}
		response := w.Result()
		response.Request = r
		return response, nil
	})
	cli := &OpenstackClient{controllerID: "controller", volume: &gophercloud.ServiceClient{Endpoint: "http://cinder.test/", ProviderClient: &gophercloud.ProviderClient{HTTPClient: http.Client{Transport: transport}}}}
	assert.Error(t, cli.CleanupBootVolumes(context.Background(), "runner"))
	assert.Equal(t, []string{"/volumes/first", "/volumes/second"}, deletes)
}
