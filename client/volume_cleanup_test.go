package client

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/gophercloud/gophercloud/v2/testhelper/client"
	"github.com/stretchr/testify/assert"
)

const (
	deleteFailure = "delete failure"
	missingVolume = "missing"
)

func TestCreateBootVolumeWaitsForCleanupAfterCancellation(t *testing.T) {
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls atomic.Int32
	var deleted atomic.Bool
	fakeServer.Mux.HandleFunc("/volumes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"volume":{"id":"volume-id","status":"creating"}}`)
	})
	fakeServer.Mux.HandleFunc("/volumes/volume-id", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			n := polls.Add(1)
			if n == 1 {
				cancel()
			}
			switch {
			case n <= 2:
				fmt.Fprint(w, `{"volume":{"id":"volume-id","status":"creating"}}`)
			case n == 3:
				fmt.Fprint(w, `{"volume":{"id":"volume-id","status":"downloading"}}`)
			default:
				fmt.Fprint(w, `{"volume":{"id":"volume-id","status":"available"}}`)
			}
		case http.MethodDelete:
			if polls.Load() <= 3 {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"badRequest":{"message":"Volume is still being created"}}`)
				return
			}
			deleted.Store(true)
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected request: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	osClient := &OpenstackClient{volume: client.ServiceClient(fakeServer)}
	_, err := osClient.CreateBootVolume(ctx, "root", "image-id", "fast", "az1", 20)
	assert.ErrorIs(t, err, context.Canceled)
	assert.True(t, deleted.Load(), "volume must be deleted once creation completes")
}

func TestCleanupVolumeHandlesStates(t *testing.T) {
	for _, status := range []string{"available", "error", missingVolume, "creating", "downloading", "in-use", "attaching", "detaching", "reserved", "deleting", deleteFailure} {
		t.Run(status, func(t *testing.T) {
			fakeServer := testhelper.SetupHTTP()
			defer fakeServer.Teardown()
			var deletes atomic.Int32
			fakeServer.Mux.HandleFunc("/volumes/volume-id", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodDelete {
					deletes.Add(1)
					if status == deleteFailure {
						w.WriteHeader(http.StatusInternalServerError)
					} else {
						w.WriteHeader(http.StatusAccepted)
					}
					return
				}
				if status == missingVolume {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				volumeStatus := status
				if status == deleteFailure {
					volumeStatus = "available"
				}
				fmt.Fprintf(w, `{"volume":{"id":"volume-id","status":%q}}`, volumeStatus)
			})
			osClient := &OpenstackClient{volume: client.ServiceClient(fakeServer)}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			err := osClient.CleanupVolume(ctx, "volume-id")
			switch status {
			case "creating", "downloading", "in-use", "attaching", "detaching", "reserved", "deleting":
				assert.ErrorIs(t, err, context.DeadlineExceeded)
				assert.Zero(t, deletes.Load())
			case deleteFailure:
				assert.ErrorContains(t, err, "volume-id")
				assert.EqualValues(t, 1, deletes.Load())
			case missingVolume:
				assert.NoError(t, err)
				assert.Zero(t, deletes.Load())
			default:
				assert.NoError(t, err)
				assert.EqualValues(t, 1, deletes.Load())
			}
		})
	}
}

func TestCleanupVolumeAttachmentTransitions(t *testing.T) {
	for _, tt := range []struct {
		name           string
		states         []string
		deleteConflict int
		wantDeletes    int32
	}{
		{"detach", []string{"in-use", "detaching", "available"}, 0, 1},
		{"reserved", []string{"reserved", "available"}, 0, 1},
		{"deleting", []string{"deleting", missingVolume}, 0, 0},
		{"conflict", []string{"available", "detaching", "available"}, http.StatusConflict, 2},
		{"bad request during detach", []string{"available", "detaching", "available"}, http.StatusBadRequest, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fakeServer := testhelper.SetupHTTP()
			defer fakeServer.Teardown()
			var polls, deletes atomic.Int32
			fakeServer.Mux.HandleFunc("/volumes/volume-id", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodDelete {
					n := deletes.Add(1)
					if n == 1 && tt.deleteConflict != 0 {
						w.WriteHeader(tt.deleteConflict)
						return
					}
					if int(polls.Load()) < len(tt.states) {
						t.Error("delete attempted before volume became available")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					w.WriteHeader(http.StatusAccepted)
					return
				}
				state := tt.states[min(int(polls.Add(1))-1, len(tt.states)-1)]
				if state == missingVolume {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				fmt.Fprintf(w, `{"volume":{"id":"volume-id","status":%q}}`, state)
			})
			osClient := &OpenstackClient{volume: client.ServiceClient(fakeServer)}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			assert.NoError(t, osClient.CleanupVolume(ctx, "volume-id"))
			assert.Equal(t, tt.wantDeletes, deletes.Load())
		})
	}
}

func TestCreateBootVolumeReturnsVolumeWhenCleanupFails(t *testing.T) {
	fakeServer := testhelper.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/volumes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"volume":{"id":"volume-id","status":"creating"}}`)
	})
	fakeServer.Mux.HandleFunc("/volumes/volume-id", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"volume":{"id":"volume-id","status":"error"}}`)
	})
	osClient := &OpenstackClient{volume: client.ServiceClient(fakeServer)}
	vol, err := osClient.CreateBootVolume(context.Background(), "root", "image-id", "fast", "az1", 20)
	assert.ErrorContains(t, err, "volume-id")
	if assert.NotNil(t, vol) {
		assert.Equal(t, "volume-id", vol.ID)
	}
}

func TestCleanupVolumeDoesNotRetryTerminalDeletionErrors(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusConflict, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			fakeServer := testhelper.SetupHTTP()
			defer fakeServer.Teardown()
			var deletes atomic.Int32
			fakeServer.Mux.HandleFunc("/volumes/volume-id", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodDelete {
					deletes.Add(1)
					w.WriteHeader(status)
					return
				}
				fmt.Fprint(w, `{"volume":{"id":"volume-id","status":"available"}}`)
			})
			osClient := &OpenstackClient{volume: client.ServiceClient(fakeServer)}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := osClient.CleanupVolume(ctx, "volume-id")
			assert.ErrorContains(t, err, "volume-id")
			assert.NotErrorIs(t, err, context.DeadlineExceeded)
			assert.EqualValues(t, 1, deletes.Load())
		})
	}
}
