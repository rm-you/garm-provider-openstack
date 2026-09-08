package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudbase/garm-provider-openstack/config"
)

func TestNewClientAuthCompatibility(t *testing.T) {
	tests := []struct {
		name, authType, fields, envPassword, method string
		noScope                                     bool
	}{
		{"environment password", "v3password", "      username: user\n      user_domain_id: default\n      project_id: project\n", "environment-secret", "password", false},
		{"explicit token", "v3token", "      token: restricted-token\n      username: admin\n      password: old-password\n      user_domain_id: default\n      project_id: project\n", "", "token", false},
		{"inferred token", "", "      token: restricted-token\n      username: admin\n      password: old-password\n      user_domain_id: default\n      project_id: project\n", "", "token", false},
		{"application credential scope", "v3applicationcredential", "      application_credential_id: app-id\n      application_credential_secret: secret\n      project_id: project\n", "", "application_credential", true},
		{"named domains", "v3password", "      username: user\n      password: password\n      default_domain: default\n      user_domain_name: users\n      project_name: project\n      project_domain_name: projects\n", "", "password", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OS_AUTH_TYPE", "")
			t.Setenv("OS_PASSWORD", tt.envPassword)
			t.Setenv("OS_REGION_NAME", "RegionOne")
			t.Setenv("OS_INTERFACE", "public")
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v3/auth/tokens" {
					http.NotFound(w, r)
					return
				}
				var body map[string]any
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				auth := body["auth"].(map[string]any)
				identity := auth["identity"].(map[string]any)
				if !assert.Equal(t, []any{tt.method}, identity["methods"]) {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if tt.noScope {
					assert.NotContains(t, auth, "scope")
				}
				if tt.envPassword != "" {
					user := identity["password"].(map[string]any)["user"].(map[string]any)
					assert.Equal(t, tt.envPassword, user["password"])
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Subject-Token", "test-token")
				w.WriteHeader(http.StatusCreated)
				fmt.Fprintf(w, `{"token":{"expires_at":"2035-01-01T00:00:00Z","catalog":[
                    {"type":"compute","name":"nova","endpoints":[{"interface":"public","region":"RegionOne","url":%q}]},
                    {"type":"image","name":"glance","endpoints":[{"interface":"public","region":"RegionOne","url":%q}]},
                    {"type":"network","name":"neutron","endpoints":[{"interface":"public","region":"RegionOne","url":%q}]},
                    {"type":"volumev3","name":"cinderv3","endpoints":[{"interface":"public","region":"RegionOne","url":%q}]}]}}`,
					server.URL+"/compute/v2.1", server.URL+"/image/v2", server.URL+"/network/v2.0", server.URL+"/volume/v3/project")
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "clouds.yaml")
			content := fmt.Sprintf("clouds:\n  test:\n    auth_type: %s\n    auth:\n      auth_url: %s/v3\n%s", tt.authType, server.URL, tt.fields)
			if !assert.NoError(t, os.WriteFile(path, []byte(content), 0o600)) {
				return
			}
			_, err := NewClient(context.Background(), &config.Config{Cloud: "test", Credentials: config.Credentials{Clouds: path}, DefaultNetworkID: "network"}, "controller")
			assert.NoError(t, err)
		})
	}
}
