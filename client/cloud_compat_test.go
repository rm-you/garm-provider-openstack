package client

import (
	"encoding/pem"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/auth"
	"github.com/gophercloud/gophercloud/v2/openstack/config/clouds"
	"github.com/stretchr/testify/assert"

	"github.com/cloudbase/garm-provider-openstack/config"
)

func TestCompatibleCloud(t *testing.T) {
	tests := []struct {
		name     string
		authType auth.AuthType
		auth     map[string]any
		env      map[string]string
		wantType auth.AuthType
		want     map[string]any
	}{
		{
			name:     "environment fills unset settings",
			authType: auth.AuthV3Password,
			auth:     map[string]any{"username": "user", "project_id": "project"},
			env:      map[string]string{"OS_PASSWORD": "secret", "OS_USERNAME": "other"},
			wantType: auth.AuthV3Password,
			want:     map[string]any{"username": "user", "password": "secret", "project_id": "project"},
		},
		{
			name:     "token variables in order",
			authType: auth.AuthV3Token,
			env:      map[string]string{"OS_AUTH_TOKEN": "first", "OS_TOKEN": "second"},
			wantType: auth.AuthV3Token,
			want:     map[string]any{"token": "first"},
		},
		{
			name:     "OS_AUTH_TYPE fills an unset auth type",
			auth:     map[string]any{"application_credential_id": "id", "application_credential_secret": "secret"},
			env:      map[string]string{"OS_AUTH_TYPE": "v3applicationcredential"},
			wantType: auth.AuthV3ApplicationCredential,
			want:     map[string]any{"application_credential_id": "id", "application_credential_secret": "secret"},
		},
		{
			name:     "clouds.yaml auth type wins over OS_AUTH_TYPE",
			authType: auth.AuthV3Password,
			auth:     map[string]any{"user_id": "user", "password": "password", "project_id": "project"},
			env:      map[string]string{"OS_AUTH_TYPE": "v3applicationcredential"},
			wantType: auth.AuthV3Password,
			want:     map[string]any{"user_id": "user", "password": "password", "project_id": "project"},
		},
		{
			name:     "token without auth type",
			auth:     map[string]any{"token": "token", "username": "user", "password": "password"},
			wantType: auth.AuthV3Token,
			want:     map[string]any{"token": "token", "username": "user", "password": "password"},
		},
		{
			name:     "token wins over password auth type",
			authType: auth.AuthV3Password,
			auth:     map[string]any{"token": "token", "username": "user", "password": "password"},
			wantType: auth.AuthV3Token,
			want:     map[string]any{"token": "token", "username": "user", "password": "password"},
		},
		{
			name:     "token with generic password type",
			authType: auth.AuthPassword,
			auth:     map[string]any{"token": "token"},
			wantType: auth.AuthToken,
			want:     map[string]any{"token": "token"},
		},
		{
			name:     "domain fills project and user domains",
			authType: auth.AuthV3Password,
			auth:     map[string]any{"username": "user", "password": "password", "project_name": "project", "domain_name": "domain"},
			wantType: auth.AuthV3Password,
			want:     map[string]any{"username": "user", "password": "password", "project_name": "project", "user_domain_name": "domain", "project_domain_name": "domain"},
		},
		{
			name:     "domain fills each unset setting",
			authType: auth.AuthV3Password,
			auth:     map[string]any{"username": "user", "password": "password", "project_name": "project", "user_domain_name": "users", "project_domain_id": "projects", "domain_id": "domain"},
			wantType: auth.AuthV3Password,
			want:     map[string]any{"username": "user", "password": "password", "project_name": "project", "user_domain_name": "users", "user_domain_id": "domain", "project_domain_id": "projects"},
		},
		{
			name:     "default domain",
			authType: auth.AuthV3Password,
			auth:     map[string]any{"username": "user", "password": "password", "project_name": "project", "default_domain": "default"},
			wantType: auth.AuthV3Password,
			want:     map[string]any{"username": "user", "password": "password", "project_name": "project", "user_domain_id": "default", "project_domain_id": "default"},
		},
		{
			name:     "domain scope without a project",
			authType: auth.AuthV3Password,
			auth:     map[string]any{"username": "user", "password": "password", "user_domain_name": "users", "domain_name": "domain"},
			wantType: auth.AuthV3Password,
			want:     map[string]any{"username": "user", "password": "password", "user_domain_name": "users", "domain_name": "domain"},
		},
		{
			name:     "default domain unused without a project",
			authType: auth.AuthV3Password,
			auth:     map[string]any{"username": "user", "password": "password", "user_domain_id": "users", "default_domain": "default"},
			wantType: auth.AuthV3Password,
			want:     map[string]any{"username": "user", "password": "password", "user_domain_id": "users"},
		},
		{
			name:     "application credential domains",
			authType: auth.AuthV3ApplicationCredential,
			auth:     map[string]any{"application_credential_name": "app", "application_credential_secret": "secret", "username": "user", "domain_name": "domain"},
			wantType: auth.AuthV3ApplicationCredential,
			want:     map[string]any{"application_credential_name": "app", "application_credential_secret": "secret", "username": "user", "user_domain_name": "domain", "project_domain_name": "domain"},
		},
		{
			name:     "federated domain scope",
			authType: auth.AuthV3OIDCClientCredentials,
			auth:     map[string]any{"client_id": "client", "domain_name": "domain"},
			wantType: auth.AuthV3OIDCClientCredentials,
			want:     map[string]any{"client_id": "client", "domain_name": "domain"},
		},
		{
			name:     "federated project scope",
			authType: auth.AuthV3OIDCClientCredentials,
			auth:     map[string]any{"client_id": "client", "project_name": "project", "domain_id": "domain"},
			wantType: auth.AuthV3OIDCClientCredentials,
			want:     map[string]any{"client_id": "client", "project_name": "project", "user_domain_id": "domain", "project_domain_id": "domain"},
		},
		{
			name:     "trust before every other scope",
			authType: auth.AuthV3Password,
			auth:     map[string]any{"user_id": "user", "password": "password", "trust_id": "trust", "system_scope": "all", "project_name": "project", "project_domain_id": "projects", "domain_id": "domain"},
			wantType: auth.AuthV3Password,
			want:     map[string]any{"user_id": "user", "password": "password", "trust_id": "trust"},
		},
		{
			name:     "project before system scope",
			authType: auth.AuthV3Password,
			auth:     map[string]any{"user_id": "user", "password": "password", "system_scope": "all", "project_id": "project"},
			wantType: auth.AuthV3Password,
			want:     map[string]any{"user_id": "user", "password": "password", "project_id": "project"},
		},
		{
			name:     "system scope before a domain",
			authType: auth.AuthV3Password,
			auth:     map[string]any{"user_id": "user", "password": "password", "system_scope": "all", "domain_id": "domain"},
			wantType: auth.AuthV3Password,
			want:     map[string]any{"user_id": "user", "password": "password", "system_scope": "all"},
		},
		{
			name:     "OAuth2 mTLS client ID",
			authType: auth.AuthV3OAuth2MTLSClientCredential,
			auth:     map[string]any{"client_id": "client"},
			wantType: auth.AuthV3OAuth2MTLSClientCredential,
			want:     map[string]any{"client_id": "client", "oauth2_client_id": "client"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OS_AUTH_TYPE", "")
			for _, fallback := range envFallbacks {
				for _, name := range fallback.vars {
					t.Setenv(name, "")
				}
			}
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			original := maps.Clone(tt.auth)

			got := compatibleCloud(clouds.Cloud{AuthType: tt.authType, Auth: tt.auth})
			assert.Equal(t, tt.wantType, got.AuthType)
			assert.Equal(t, tt.want, got.Auth)
			assert.Equal(t, original, tt.auth, "the parsed cloud must not change")
		})
	}
}

func TestCloudParseOptionsPreferFilesOverEnvironment(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	if !assert.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600)) {
		return
	}

	tests := []struct {
		name       string
		cloud      string
		secure     string
		env        map[string]string
		region     string
		interface_ gophercloud.Availability
		insecure   bool
	}{
		{
			name:       "clouds.yaml wins",
			cloud:      "    region_name: RegionOne\n    interface: internal\n    cacert: " + caFile + "\n",
			env:        map[string]string{"OS_REGION_NAME": "RegionTwo", "OS_INTERFACE": "admin", "OS_CACERT": filepath.Join(dir, "missing.pem")},
			region:     "RegionOne",
			interface_: gophercloud.AvailabilityInternal,
		},
		{
			name:       "environment fills unset settings",
			env:        map[string]string{"OS_REGION_NAME": "RegionTwo", "OS_INTERFACE": "admin", "OS_CACERT": caFile},
			region:     "RegionTwo",
			interface_: gophercloud.AvailabilityAdmin,
		},
		{
			name:       "secure.yaml wins over clouds.yaml",
			cloud:      "    region_name: RegionOne\n",
			secure:     "    region_name: RegionThree\n",
			env:        map[string]string{"OS_REGION_NAME": "RegionTwo"},
			region:     "RegionThree",
			interface_: gophercloud.AvailabilityPublic,
		},
		{
			name:       "OS_INSECURE applies without verify",
			env:        map[string]string{"OS_INSECURE": "true"},
			interface_: gophercloud.AvailabilityPublic,
			insecure:   true,
		},
		{
			name:       "verify wins over OS_INSECURE",
			cloud:      "    verify: true\n",
			env:        map[string]string{"OS_INSECURE": "true"},
			interface_: gophercloud.AvailabilityPublic,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, name := range []string{"OS_CLOUD", "OS_CLIENT_CONFIG_FILE", "OS_REGION_NAME", "OS_INTERFACE", "OS_CACERT", "OS_CERT", "OS_KEY", "OS_INSECURE"} {
				t.Setenv(name, "")
			}
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			cfg := &config.Config{Cloud: "test", Credentials: config.Credentials{Clouds: filepath.Join(t.TempDir(), "clouds.yaml")}}
			if !assert.NoError(t, os.WriteFile(cfg.Credentials.Clouds, []byte("clouds:\n  test:\n"+tt.cloud+"    auth:\n      auth_url: https://keystone.example/v3\n"), 0o600)) {
				return
			}
			if tt.secure != "" {
				cfg.Credentials.SecureClouds = filepath.Join(t.TempDir(), "secure.yaml")
				if !assert.NoError(t, os.WriteFile(cfg.Credentials.SecureClouds, []byte("clouds:\n  test:\n"+tt.secure), 0o600)) {
					return
				}
			}

			opts, err := cloudParseOptions(cfg)
			if !assert.NoError(t, err) {
				return
			}
			_, endpointOpts, tlsConfig, err := clouds.Parse(opts...)
			if !assert.NoError(t, err) {
				return
			}
			assert.Equal(t, tt.region, endpointOpts.Region)
			assert.Equal(t, tt.interface_, endpointOpts.Availability)
			assert.Equal(t, tt.insecure, tlsConfig != nil && tlsConfig.InsecureSkipVerify)
		})
	}
}

func TestCloudParseOptionsSkipMissingOptionalFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Cloud: "test", Credentials: config.Credentials{
		Clouds:       filepath.Join(dir, "clouds.yaml"),
		SecureClouds: filepath.Join(dir, "missing-secure.yaml"),
		PublicClouds: filepath.Join(dir, "missing-public.yaml"),
	}}
	if !assert.NoError(t, os.WriteFile(cfg.Credentials.Clouds, []byte("clouds:\n  test:\n    auth:\n      auth_url: https://keystone.example/v3\n"), 0o600)) {
		return
	}

	opts, err := cloudParseOptions(cfg)
	if !assert.NoError(t, err) {
		return
	}
	_, _, _, err = clouds.Parse(opts...)
	assert.NoError(t, err)
}
