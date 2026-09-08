package clouds

import (
	"crypto/tls"
	"encoding/json"

	"github.com/gophercloud/gophercloud/v2"
	tokens3 "github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
)

// CloudConfig contains the selected cloud and the options needed to create an
// Identity v3 ProviderClient and service clients.
type CloudConfig struct {
	Cloud            Cloud
	IdentityEndpoint string
	AuthOptions      tokens3.AuthOptionsBuilder
	EndpointOptions  gophercloud.EndpointOpts
	TLSConfig        *tls.Config
}

// Clouds represents a collection of Cloud entries in a clouds.yaml file.
// The format of clouds.yaml is documented at
// https://docs.openstack.org/os-client-config/latest/user/configuration.html.
type Clouds struct {
	Clouds map[string]Cloud `yaml:"clouds" json:"clouds"`
}

type PublicClouds struct {
	Clouds map[string]Cloud `yaml:"public-clouds" json:"public-clouds"`
}

// Cloud represents an entry in a clouds.yaml/public-clouds.yaml/secure.yaml file.
type Cloud struct {
	Cloud      string    `yaml:"cloud,omitempty" json:"cloud,omitempty"`
	Profile    string    `yaml:"profile,omitempty" json:"profile,omitempty"`
	AuthInfo   *AuthInfo `yaml:"auth,omitempty" json:"auth,omitempty"`
	AuthType   AuthType  `yaml:"auth_type,omitempty" json:"auth_type,omitempty"`
	RegionName string    `yaml:"region_name,omitempty" json:"region_name,omitempty"`
	Regions    []Region  `yaml:"regions,omitempty" json:"regions,omitempty"`

	// EndpointType and Interface both specify whether to use the public, internal,
	// or admin interface of a service. They should be considered synonymous, but
	// EndpointType will take precedence when both are specified.
	EndpointType string `yaml:"endpoint_type,omitempty" json:"endpoint_type,omitempty"`
	Interface    string `yaml:"interface,omitempty" json:"interface,omitempty"`

	// API Version overrides.
	IdentityAPIVersion string `yaml:"identity_api_version,omitempty" json:"identity_api_version,omitempty"`
	VolumeAPIVersion   string `yaml:"volume_api_version,omitempty" json:"volume_api_version,omitempty"`

	// Verify whether or not SSL API requests should be verified.
	Verify *bool `yaml:"verify,omitempty" json:"verify,omitempty"`

	// CACertFile a path to a CA Cert bundle that can be used as part of
	// verifying SSL API requests.
	CACertFile string `yaml:"cacert,omitempty" json:"cacert,omitempty"`

	// ClientCertFile a path to a client certificate to use as part of the SSL
	// transaction.
	ClientCertFile string `yaml:"cert,omitempty" json:"cert,omitempty"`

	// ClientKeyFile a path to a client key to use as part of the SSL
	// transaction.
	ClientKeyFile string `yaml:"key,omitempty" json:"key,omitempty"`

	// IdentityProvider and Protocol may appear outside auth in clouds.yaml.
	IdentityProvider string `yaml:"identity_provider,omitempty" json:"identity_provider,omitempty"`
	Protocol         string `yaml:"protocol,omitempty" json:"protocol,omitempty"`
}

// AuthInfo represents the auth section of a cloud entry or
// auth options entered explicitly in ClientOpts.
type AuthInfo struct {
	// AuthURL is the keystone/identity endpoint URL.
	AuthURL string `yaml:"auth_url,omitempty" json:"auth_url,omitempty"`

	// Token is a pre-generated authentication token.
	Token string `yaml:"token,omitempty" json:"token,omitempty"`

	// Username is the username of the user.
	Username string `yaml:"username,omitempty" json:"username,omitempty"`

	// UserID is the unique ID of a user.
	UserID string `yaml:"user_id,omitempty" json:"user_id,omitempty"`

	// Password is the password of the user.
	Password string `yaml:"password,omitempty" json:"password,omitempty"`

	// Application Credential ID to login with.
	ApplicationCredentialID string `yaml:"application_credential_id,omitempty" json:"application_credential_id,omitempty"`

	// Application Credential name to login with.
	ApplicationCredentialName string `yaml:"application_credential_name,omitempty" json:"application_credential_name,omitempty"`

	// Application Credential secret to login with.
	ApplicationCredentialSecret string `yaml:"application_credential_secret,omitempty" json:"application_credential_secret,omitempty"`

	// SystemScope is a system information to scope to.
	SystemScope string `yaml:"system_scope,omitempty" json:"system_scope,omitempty"`

	// ProjectName is the common/human-readable name of a project.
	// Users can be scoped to a project.
	// ProjectName on its own is not enough to ensure a unique scope. It must
	// also be combined with either a ProjectDomainName or ProjectDomainID.
	// ProjectName cannot be combined with ProjectID in a scope.
	ProjectName string `yaml:"project_name,omitempty" json:"project_name,omitempty"`

	// ProjectID is the unique ID of a project.
	// It can be used to scope a user to a specific project.
	ProjectID string `yaml:"project_id,omitempty" json:"project_id,omitempty"`

	// UserDomainName is the name of the domain where a user resides.
	// It is used to identify the source domain of a user.
	UserDomainName string `yaml:"user_domain_name,omitempty" json:"user_domain_name,omitempty"`

	// UserDomainID is the unique ID of the domain where a user resides.
	// It is used to identify the source domain of a user.
	UserDomainID string `yaml:"user_domain_id,omitempty" json:"user_domain_id,omitempty"`

	// ProjectDomainName is the name of the domain where a project resides.
	// It is used to identify the source domain of a project.
	// ProjectDomainName can be used in addition to a ProjectName when scoping
	// a user to a specific project.
	ProjectDomainName string `yaml:"project_domain_name,omitempty" json:"project_domain_name,omitempty"`

	// ProjectDomainID is the name of the domain where a project resides.
	// It is used to identify the source domain of a project.
	// ProjectDomainID can be used in addition to a ProjectName when scoping
	// a user to a specific project.
	ProjectDomainID string `yaml:"project_domain_id,omitempty" json:"project_domain_id,omitempty"`

	// DomainName is the name of a domain which can be used to identify the
	// source domain of either a user or a project.
	// If UserDomainName and ProjectDomainName are not specified, then DomainName
	// is used as a default choice.
	// It can also be used be used to specify a domain-only scope.
	DomainName string `yaml:"domain_name,omitempty" json:"domain_name,omitempty"`

	// DomainID is the unique ID of a domain which can be used to identify the
	// source domain of eitehr a user or a project.
	// If UserDomainID and ProjectDomainID are not specified, then DomainID is
	// used as a default choice.
	// It can also be used be used to specify a domain-only scope.
	DomainID string `yaml:"domain_id,omitempty" json:"domain_id,omitempty"`

	// DefaultDomain is the domain ID to fall back on if no other domain has
	// been specified and a domain is required for scope.
	DefaultDomain string `yaml:"default_domain,omitempty" json:"default_domain,omitempty"`

	// TrustID is the ID of the trust to use as a trustee.
	TrustID string `yaml:"trust_id,omitempty" json:"trust_id,omitempty"`

	// AllowReauth should be set to true if you grant permission for Gophercloud to
	// cache your credentials in memory, and to allow Gophercloud to attempt to
	// re-authenticate automatically if/when your token expires.  If you set it to
	// false, it will not cache these settings, but re-authentication will not be
	// possible.  This setting defaults to false.
	AllowReauth bool `yaml:"allow_reauth,omitempty" json:"allow_reauth,omitempty"`

	// ClientID is the OAuth 2.0 client ID.
	ClientID string `yaml:"client_id,omitempty" json:"client_id,omitempty"`

	// ClientSecret is the OAuth 2.0 client secret.
	ClientSecret string `yaml:"client_secret,omitempty" json:"client_secret,omitempty"`

	// OAuth2ClientID overrides ClientID for Keystone OAuth2 authentication.
	OAuth2ClientID string `yaml:"oauth2_client_id,omitempty" json:"oauth2_client_id,omitempty"`

	// OAuth2Endpoint is Keystone's OS-OAUTH2 token endpoint.
	OAuth2Endpoint string `yaml:"oauth2_endpoint,omitempty" json:"oauth2_endpoint,omitempty"`

	// AccessTokenEndpoint is the identity provider's token endpoint.
	AccessTokenEndpoint string `yaml:"access_token_endpoint,omitempty" json:"access_token_endpoint,omitempty"`

	// IdentityProvider is the Keystone federation identity provider.
	IdentityProvider string `yaml:"identity_provider,omitempty" json:"identity_provider,omitempty"`

	// Protocol is the Keystone federation protocol.
	Protocol string `yaml:"protocol,omitempty" json:"protocol,omitempty"`

	// AccessTokenType selects the token response field. It defaults to "access_token".
	AccessTokenType string `yaml:"access_token_type,omitempty" json:"access_token_type,omitempty"`

	// OpenIDScope is the OAuth 2.0 scope. It defaults to "openid".
	OpenIDScope string `yaml:"openid_scope,omitempty" json:"openid_scope,omitempty"`

	// DiscoveryEndpoint supplies AccessTokenEndpoint through OIDC discovery.
	DiscoveryEndpoint string `yaml:"discovery_endpoint,omitempty" json:"discovery_endpoint,omitempty"`

	// RedirectPort is the local WebSSO callback port. It defaults to 9990.
	RedirectPort int `yaml:"redirect_port,omitempty" json:"redirect_port,omitempty"`

	// RedirectHost is the loopback host used for the WebSSO callback.
	RedirectHost string `yaml:"redirect_host,omitempty" json:"redirect_host,omitempty"`
}

// Region represents a region included as part of cloud in clouds.yaml
// According to Python-based openstacksdk, this can be either a struct (as defined)
// or a plain string. Custom unmarshallers handle both cases.
type Region struct {
	Name   string `yaml:"name,omitempty" json:"name,omitempty"`
	Values Cloud  `yaml:"values,omitempty" json:"values,omitempty"`
}

// UnmarshalJSON handles either a plain string acting as the Name property or
// a struct, mimicking the Python-based openstacksdk.
func (r *Region) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		r.Name = name
		return nil
	}

	type region Region
	var tmp region
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	r.Name = tmp.Name
	r.Values = tmp.Values

	return nil
}

// UnmarshalYAML handles either a plain string acting as the Name property or
// a struct, mimicking the Python-based openstacksdk.
func (r *Region) UnmarshalYAML(unmarshal func(any) error) error {
	var name string
	if err := unmarshal(&name); err == nil {
		r.Name = name
		return nil
	}

	type region Region
	var tmp region
	if err := unmarshal(&tmp); err != nil {
		return err
	}
	r.Name = tmp.Name
	r.Values = tmp.Values

	return nil
}

// AuthType respresents a valid method of authentication.
type AuthType string

const (
	// AuthPassword defines an unknown version of the password
	AuthPassword AuthType = "password"
	// AuthToken defined an unknown version of the token
	AuthToken AuthType = "token"

	// AuthV2Password defines version 2 of the password
	AuthV2Password AuthType = "v2password"
	// AuthV2Token defines version 2 of the token
	AuthV2Token AuthType = "v2token"

	// AuthV3Password defines version 3 of the password
	AuthV3Password AuthType = "v3password"
	// AuthV3Token defines version 3 of the token
	AuthV3Token AuthType = "v3token"

	// AuthV3ApplicationCredential defines version 3 of the application credential
	AuthV3ApplicationCredential AuthType = "v3applicationcredential"

	// AuthV3OIDCClientCredentials defines version 3 of the OIDC client credentials
	AuthV3OIDCClientCredentials AuthType = "v3oidcclientcredentials"

	// AuthV3WebSSO defines version 3 of the WebSSO browser-based federation auth
	AuthV3WebSSO AuthType = "v3websso"

	// AuthV3OAuth2MTLSClientCredential defines version 3 of the OAuth2 mTLS
	// client credentials authentication via OS-OAUTH2.
	AuthV3OAuth2MTLSClientCredential AuthType = "v3oauth2mtlsclientcredential"
)
