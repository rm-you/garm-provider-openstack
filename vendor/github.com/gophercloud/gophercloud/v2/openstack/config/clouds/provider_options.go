package clouds

import (
	"fmt"
	"os"
	"strconv"

	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/oauth2mtls"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/oidc"
	tokens3 "github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/websso"
)

// ParseV3 fetches a clouds.yaml file and returns Identity v3 authentication,
// endpoint, and TLS options. Its IdentityEndpoint and AuthOptions fields can
// be passed to config.NewProviderClientV3.
func ParseV3(opts ...ParseOption) (CloudConfig, error) {
	parsed, err := parseCloud(opts...)
	if err != nil {
		return CloudConfig{}, err
	}

	cloudConfig, err := providerOptions(parsed)
	if err != nil {
		return CloudConfig{}, err
	}

	return cloudConfig, nil
}

func providerOptions(parsed *parsedCloud) (CloudConfig, error) {
	cloud := parsed.cloud
	authInfo := *cloud.AuthInfo
	authInfo.AuthURL = coalesce(authInfo.AuthURL, os.Getenv("OS_AUTH_URL"))
	authInfo.Username = coalesce(authInfo.Username, os.Getenv("OS_USERNAME"))
	authInfo.UserID = coalesce(authInfo.UserID, os.Getenv("OS_USER_ID"))
	authInfo.Password = coalesce(authInfo.Password, os.Getenv("OS_PASSWORD"))
	authInfo.Token = coalesce(authInfo.Token, os.Getenv("OS_AUTH_TOKEN"), os.Getenv("OS_TOKEN"))
	authInfo.ProjectID = coalesce(authInfo.ProjectID, os.Getenv("OS_PROJECT_ID"), os.Getenv("OS_TENANT_ID"))
	authInfo.ProjectName = coalesce(authInfo.ProjectName, os.Getenv("OS_PROJECT_NAME"), os.Getenv("OS_TENANT_NAME"))
	authInfo.DomainID = coalesce(authInfo.DomainID, os.Getenv("OS_DOMAIN_ID"))
	authInfo.DomainName = coalesce(authInfo.DomainName, os.Getenv("OS_DOMAIN_NAME"))
	authInfo.DefaultDomain = coalesce(authInfo.DefaultDomain, os.Getenv("OS_DEFAULT_DOMAIN"))
	authInfo.UserDomainID = coalesce(authInfo.UserDomainID, os.Getenv("OS_USER_DOMAIN_ID"))
	authInfo.UserDomainName = coalesce(authInfo.UserDomainName, os.Getenv("OS_USER_DOMAIN_NAME"))
	authInfo.ProjectDomainID = coalesce(authInfo.ProjectDomainID, os.Getenv("OS_PROJECT_DOMAIN_ID"))
	authInfo.ProjectDomainName = coalesce(authInfo.ProjectDomainName, os.Getenv("OS_PROJECT_DOMAIN_NAME"))
	authInfo.ApplicationCredentialID = coalesce(authInfo.ApplicationCredentialID, os.Getenv("OS_APPLICATION_CREDENTIAL_ID"))
	authInfo.ApplicationCredentialName = coalesce(authInfo.ApplicationCredentialName, os.Getenv("OS_APPLICATION_CREDENTIAL_NAME"))
	authInfo.ApplicationCredentialSecret = coalesce(authInfo.ApplicationCredentialSecret, os.Getenv("OS_APPLICATION_CREDENTIAL_SECRET"))
	authInfo.SystemScope = coalesce(authInfo.SystemScope, os.Getenv("OS_SYSTEM_SCOPE"))
	authInfo.TrustID = coalesce(authInfo.TrustID, os.Getenv("OS_TRUST_ID"))
	cloud.AuthInfo = &authInfo
	parsed.cloud = cloud
	authType := AuthType(coalesce(os.Getenv("OS_AUTH_TYPE"), string(cloud.AuthType)))
	identityEndpoint := coalesce(parsed.options.authURL, authInfo.AuthURL, os.Getenv("OS_AUTH_URL"))

	var authOptions tokens3.AuthOptionsBuilder
	switch authType {
	case AuthV3OIDCClientCredentials:
		authOptions = oidcAuthOptions(parsed)
	case AuthV3WebSSO:
		redirectPort, err := webSSORedirectPort(authInfo.RedirectPort)
		if err != nil {
			return CloudConfig{}, err
		}
		namespace := parsed.options.tokenCacheNamespace
		if parsed.options.tokenCache != nil && namespace == "" {
			namespace = parsed.options.cloudName
		}
		authOptions = &websso.AuthOptions{
			IdentityProviderName: coalesce(authInfo.IdentityProvider, cloud.IdentityProvider, os.Getenv("OS_IDENTITY_PROVIDER")),
			Protocol:             coalesce(authInfo.Protocol, cloud.Protocol, os.Getenv("OS_PROTOCOL")),
			AllowReauth:          authInfo.AllowReauth,
			RedirectPort:         redirectPort,
			RedirectHost:         coalesce(authInfo.RedirectHost, os.Getenv("OS_REDIRECT_HOST")),
			Timeout:              parsed.options.webSSOTimeout,
			BrowserOpener:        parsed.options.webSSOBrowserOpener,
			TokenCache:           parsed.options.tokenCache,
			CacheNamespace:       namespace,
		}
	case AuthV3OAuth2MTLSClientCredential:
		authOptions = &oauth2mtls.AuthOptions{
			OAuth2Endpoint: coalesce(authInfo.OAuth2Endpoint, os.Getenv("OS_OAUTH2_ENDPOINT")),
			ClientID:       coalesce(authInfo.OAuth2ClientID, authInfo.ClientID, os.Getenv("OS_OAUTH2_CLIENT_ID"), os.Getenv("OS_CLIENT_ID")),
			AllowReauth:    authInfo.AllowReauth,
		}
	case "", AuthPassword, AuthToken, AuthV3Password, AuthV3Token, AuthV3ApplicationCredential:
		authOptions = standardV3AuthOptions(parsed, authType)
	case AuthV2Password, AuthV2Token:
		return CloudConfig{}, fmt.Errorf("auth type %q requires Identity v2", authType)
	default:
		return CloudConfig{}, fmt.Errorf("unsupported auth type %q", authType)
	}

	var scope *tokens3.Scope
	domainScope := false
	switch opts := authOptions.(type) {
	case *tokens3.AuthOptions:
		// Application credentials carry their own project scope.
		if opts.Password != "" || opts.TokenID != "" ||
			(opts.ApplicationCredentialID == "" && opts.ApplicationCredentialName == "") {
			scope = &opts.Scope
		}
	case *oidc.AuthOptions:
		scope, domainScope = &opts.Scope, true
	case *websso.AuthOptions:
		scope, domainScope = &opts.Scope, true
	}
	if scope != nil {
		var err error
		*scope, err = authScope(parsed, domainScope)
		if err != nil {
			return CloudConfig{}, err
		}
	}

	return CloudConfig{
		Cloud:            cloud,
		IdentityEndpoint: identityEndpoint,
		AuthOptions:      authOptions,
		EndpointOptions:  parsed.endpointOpts,
		TLSConfig:        parsed.tlsConfig,
	}, nil
}

func standardV3AuthOptions(parsed *parsedCloud, authType AuthType) *tokens3.AuthOptions {
	authInfo := parsed.cloud.AuthInfo
	domainID := coalesce(parsed.options.domainID, authInfo.UserDomainID, authInfo.DomainID)
	domainName := coalesce(parsed.options.domainName, authInfo.UserDomainName, authInfo.DomainName)
	if domainID == "" && domainName == "" {
		domainID = authInfo.DefaultDomain
	}
	opts := &tokens3.AuthOptions{
		Username:                    coalesce(parsed.options.username, authInfo.Username),
		UserID:                      coalesce(parsed.options.userID, authInfo.UserID),
		Password:                    coalesce(parsed.options.password, authInfo.Password),
		DomainID:                    domainID,
		DomainName:                  domainName,
		AllowReauth:                 authInfo.AllowReauth,
		TokenID:                     coalesce(parsed.options.token, authInfo.Token),
		ApplicationCredentialID:     coalesce(parsed.options.applicationCredentialID, authInfo.ApplicationCredentialID),
		ApplicationCredentialName:   coalesce(parsed.options.applicationCredentialName, authInfo.ApplicationCredentialName),
		ApplicationCredentialSecret: coalesce(parsed.options.applicationCredentialSecret, authInfo.ApplicationCredentialSecret),
	}
	if authType == "" && opts.TokenID != "" {
		authType = AuthV3Token
	}
	switch authType {
	case AuthToken, AuthV3Token:
		opts = &tokens3.AuthOptions{TokenID: opts.TokenID, AllowReauth: opts.AllowReauth}
	case AuthPassword, AuthV3Password:
		opts.TokenID = ""
		opts.ApplicationCredentialID = ""
		opts.ApplicationCredentialName = ""
		opts.ApplicationCredentialSecret = ""
	case AuthV3ApplicationCredential:
		opts.Password = ""
		opts.TokenID = ""
	}
	return opts
}

func oidcAuthOptions(parsed *parsedCloud) *oidc.AuthOptions {
	authInfo := parsed.cloud.AuthInfo
	return &oidc.AuthOptions{
		ClientID:             coalesce(authInfo.ClientID, os.Getenv("OS_CLIENT_ID")),
		ClientSecret:         coalesce(authInfo.ClientSecret, os.Getenv("OS_CLIENT_SECRET")),
		AccessTokenEndpoint:  coalesce(authInfo.AccessTokenEndpoint, os.Getenv("OS_ACCESS_TOKEN_ENDPOINT")),
		DiscoveryEndpoint:    coalesce(authInfo.DiscoveryEndpoint, os.Getenv("OS_DISCOVERY_ENDPOINT")),
		IdentityProviderName: coalesce(authInfo.IdentityProvider, parsed.cloud.IdentityProvider, os.Getenv("OS_IDENTITY_PROVIDER")),
		Protocol:             coalesce(authInfo.Protocol, parsed.cloud.Protocol, os.Getenv("OS_PROTOCOL")),
		AccessTokenType:      coalesce(authInfo.AccessTokenType, os.Getenv("OS_ACCESS_TOKEN_TYPE")),
		AllowReauth:          authInfo.AllowReauth,
		OIDCScope:            coalesce(authInfo.OpenIDScope, os.Getenv("OS_OPENID_SCOPE")),
	}
}

func authScope(parsed *parsedCloud, domainScope bool) (tokens3.Scope, error) {
	if parsed.options.scope != nil {
		return tokens3.Scope(*parsed.options.scope), nil
	}

	authInfo := parsed.cloud.AuthInfo
	if authInfo.TrustID != "" {
		return tokens3.Scope{TrustID: authInfo.TrustID}, nil
	}
	if authInfo.SystemScope != "" {
		if authInfo.SystemScope != "all" {
			return tokens3.Scope{}, fmt.Errorf("only system scope of all is supported")
		}
		return tokens3.Scope{System: true}, nil
	}
	if projectID := coalesce(parsed.options.projectID, authInfo.ProjectID); projectID != "" {
		return tokens3.Scope{ProjectID: projectID}, nil
	}
	if projectName := coalesce(parsed.options.projectName, authInfo.ProjectName); projectName != "" {
		domainID := coalesce(parsed.options.domainID, authInfo.ProjectDomainID, authInfo.DomainID)
		domainName := coalesce(parsed.options.domainName, authInfo.ProjectDomainName, authInfo.DomainName)
		if domainID == "" && domainName == "" {
			domainID = authInfo.DefaultDomain
		}
		return tokens3.Scope{
			ProjectName: projectName,
			DomainID:    domainID,
			DomainName:  domainName,
		}, nil
	}
	if domainScope {
		if domainID := coalesce(parsed.options.domainID, authInfo.DomainID); domainID != "" {
			return tokens3.Scope{DomainID: domainID}, nil
		}
		if domainName := coalesce(parsed.options.domainName, authInfo.DomainName); domainName != "" {
			return tokens3.Scope{DomainName: domainName}, nil
		}
	}
	return tokens3.Scope{}, nil
}

func webSSORedirectPort(configured int) (int, error) {
	if configured != 0 {
		return configured, nil
	}
	value := os.Getenv("OS_REDIRECT_PORT")
	if value == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("failed to parse OS_REDIRECT_PORT: %w", err)
	}
	return port, nil
}
