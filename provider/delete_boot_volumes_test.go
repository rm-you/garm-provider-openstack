package provider

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/gophercloud/gophercloud/v2/testhelper"
	thclient "github.com/gophercloud/gophercloud/v2/testhelper/client"
	"github.com/stretchr/testify/assert"

	"github.com/cloudbase/garm-provider-openstack/client"
)

func TestDeleteInstanceCleansUpBootVolumeWithoutServer(t *testing.T) {
	for _, failDelete := range []bool{false, true} {
		t.Run(fmt.Sprint(failDelete), func(t *testing.T) {
			server := testhelper.SetupHTTP()
			defer server.Teardown()
			var deletes atomic.Int32
			server.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"servers":[]}`)
			})
			server.Mux.HandleFunc("/volumes/detail", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				assert.Equal(t, "runner-root", r.URL.Query().Get("name"))
				fmt.Fprint(w, `{"volumes":[
					{"id":"unowned","name":"runner-root"},
					{"id":"different-controller","name":"runner-root","metadata":{"garm-controller-id":"other","garm-create-id":"request"}},
					{"id":"different-name","name":"other-root","metadata":{"garm-controller-id":"controller","garm-create-id":"request"}},
					{"id":"no-create-id","name":"runner-root","metadata":{"garm-controller-id":"controller"}},
					{"id":"owned","name":"runner-root","metadata":{"garm-controller-id":"controller","garm-create-id":"request"}}
				]}`)
			})
			server.Mux.HandleFunc("/volumes/owned", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodDelete {
					deletes.Add(1)
					if failDelete {
						w.WriteHeader(http.StatusServiceUnavailable)
					} else {
						w.WriteHeader(http.StatusAccepted)
					}
					return
				}
				fmt.Fprint(w, `{"volume":{"id":"owned","status":"available"}}`)
			})
			server.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				w.WriteHeader(http.StatusNotFound)
			})
			p := &openstackProvider{cli: client.NewTestOpenStackClient(thclient.ServiceClient(server), "controller")}
			err := p.DeleteInstance(context.Background(), "runner")
			if failDelete {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.EqualValues(t, 1, deletes.Load())
		})
	}
}
