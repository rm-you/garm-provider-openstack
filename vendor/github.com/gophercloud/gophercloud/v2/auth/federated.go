package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/auth/tokencache"
)

// federatedKeys are the settings read for federated authentication types.
// The environment supplies each as OS_<KEY>.
var federatedKeys = []string{
	"identity_provider", "protocol",
	"client_id", "client_secret", "access_token_endpoint", "access_token_type",
	"discovery_endpoint", "openid_scope",
	"oauth2_endpoint", "oauth2_client_id",
	"redirect_host", "redirect_port",
}

// WithTokenCache supplies storage for reusable unscoped WebSSO tokens.
func WithTokenCache(cache tokencache.Cache) CloudOption {
	return func(co *CloudOptions) { co.TokenCache = cache }
}

// WithTokenCacheNamespace identifies the browser login whose tokens are
// cached. Clouds sharing a namespace share unscoped tokens.
func WithTokenCacheNamespace(namespace string) CloudOption {
	return func(co *CloudOptions) { co.TokenCacheNamespace = namespace }
}

// WithWebSSOBrowserOpener replaces the system browser launcher.
func WithWebSSOBrowserOpener(open func(string) error) CloudOption {
	return func(co *CloudOptions) { co.WebSSOBrowserOpener = open }
}

// WithWebSSOTimeout limits how long WebSSO waits for the browser.
func WithWebSSOTimeout(timeout time.Duration) CloudOption {
	return func(co *CloudOptions) { co.WebSSOTimeout = timeout }
}

func federatedAuthOptionsFromCloud(c CloudSource, cloudOpts ...CloudOption) (Authenticator, error) {
	m, authURL := mergedAuth(c, cloudOpts)
	if authURL == "" {
		return nil, gophercloud.ErrMissingInput{Argument: "AuthURL"}
	}

	authType := c.GetAuthType()
	co := ResolveCloudOptions(cloudOpts...)
	var scope *Scope
	if authType != AuthV3OAuth2MTLSClientCredential {
		var err error
		if scope, err = cloudScope(m, co); err != nil {
			return nil, err
		}
	}

	return federatedAuthOptions(authType, authURL, m, scope, co)
}

func federatedAuthOptionsFromEnv(authType AuthType) (Authenticator, error) {
	authURL := os.Getenv("OS_AUTH_URL")
	if authURL == "" {
		return nil, gophercloud.ErrMissingEnvironmentVariable{
			EnvironmentVariable: "OS_AUTH_URL",
		}
	}

	var scope *Scope
	if authType != AuthV3OAuth2MTLSClientCredential {
		var err error
		if scope, err = envScope(); err != nil {
			return nil, err
		}
		normalizeProjectDomain(scope)
	}

	m := map[string]any{"allow_reauth": true}
	for _, key := range federatedKeys {
		setIfNotEmpty(m, key, os.Getenv("OS_"+strings.ToUpper(key)))
	}

	return federatedAuthOptions(authType, authURL, m, scope, CloudOptions{})
}

// normalizeProjectDomain makes the generic domain the project's domain when
// a project is requested, as the environment loader does for other types.
func normalizeProjectDomain(scope *Scope) {
	if scope.ProjectID == "" && scope.ProjectName == "" {
		return
	}
	if scope.ProjectDomainID == "" {
		scope.ProjectDomainID = scope.DomainID
	}
	if scope.ProjectDomainName == "" {
		scope.ProjectDomainName = scope.DomainName
	}
	scope.DomainID = ""
	scope.DomainName = ""
}

func federatedAuthOptions(authType AuthType, authURL string, m map[string]any, scope *Scope, co CloudOptions) (Authenticator, error) {
	var settings struct {
		AllowReauth bool `json:"allow_reauth"`
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		return nil, err
	}

	switch authType {
	case AuthV3OAuth2MTLSClientCredential:
		return &V3OAuth2MTLSOpts{
			AuthURL:        authURL,
			OAuth2Endpoint: str(m, "oauth2_endpoint"),
			ClientID:       str(m, "oauth2_client_id"),
			AllowReauth:    settings.AllowReauth,
		}, nil
	case AuthV3OIDCClientCredentials:
		return &V3OIDCClientCredentialsOpts{
			AuthURL:              authURL,
			IdentityProviderName: str(m, "identity_provider"),
			Protocol:             str(m, "protocol"),
			ClientID:             str(m, "client_id"),
			ClientSecret:         str(m, "client_secret"),
			AccessTokenEndpoint:  str(m, "access_token_endpoint"),
			AccessTokenType:      str(m, "access_token_type"),
			DiscoveryEndpoint:    str(m, "discovery_endpoint"),
			OIDCScope:            str(m, "openid_scope"),
			AllowReauth:          settings.AllowReauth,
			Scope:                scope,
		}, nil
	case AuthV3WebSSO:
		var redirectPort int
		if value, ok := m["redirect_port"]; ok {
			redirectPort, err = strconv.Atoi(fmt.Sprint(value))
			if err != nil {
				return nil, fmt.Errorf("invalid redirect_port %q: %w", fmt.Sprint(value), err)
			}
		}

		return &V3WebSSOOpts{
			AuthURL:              authURL,
			IdentityProviderName: str(m, "identity_provider"),
			Protocol:             str(m, "protocol"),
			AllowReauth:          settings.AllowReauth,
			Scope:                scope,
			RedirectHost:         str(m, "redirect_host"),
			RedirectPort:         redirectPort,
			Timeout:              co.WebSSOTimeout,
			BrowserOpener:        co.WebSSOBrowserOpener,
			TokenCache:           co.TokenCache,
			CacheNamespace:       co.TokenCacheNamespace,
		}, nil
	}

	return nil, gophercloud.ErrUnsupportedAuthType{AuthType: string(authType)}
}
