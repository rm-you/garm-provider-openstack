package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
)

type oidcDiscoveryDocument struct {
	TokenEndpoint       string   `json:"token_endpoint"`
	GrantTypesSupported []string `json:"grant_types_supported"`
}

// V3OIDCClientCredentialsOpts contains OIDC client-credentials authentication options.
type V3OIDCClientCredentialsOpts struct {
	// AuthURL is the Keystone identity endpoint.
	AuthURL string
	// IdentityProviderName is the Keystone federation identity provider.
	IdentityProviderName string `required:"true"`

	// Protocol is the Keystone federation protocol.
	Protocol string `required:"true"`

	// ClientID is the OAuth 2.0 client identifier.
	ClientID string `required:"true"`

	// ClientSecret is the OAuth 2.0 client secret.
	ClientSecret string

	// AccessTokenEndpoint is the identity provider's token endpoint.
	AccessTokenEndpoint string `or:"DiscoveryEndpoint"`

	// AccessTokenType selects the token response field. It defaults to "access_token".
	AccessTokenType string

	// DiscoveryEndpoint is used when AccessTokenEndpoint is empty.
	DiscoveryEndpoint string

	// OIDCScope is the OAuth 2.0 scope. It defaults to "openid".
	OIDCScope string

	// AllowReauth enables automatic reauthentication.
	AllowReauth bool

	// Scope controls the resulting Keystone token scope.
	Scope *Scope
}

// authenticateOIDC exchanges an identity provider access token for a
// Keystone token, rescoping it when opts requests a scope.
func authenticateOIDC(ctx context.Context, c *gophercloud.ServiceClient, oidcOpts *V3OIDCClientCredentialsOpts) (*AuthResult, error) {
	if _, err := gophercloud.BuildRequestBody(oidcOpts, ""); err != nil {
		return nil, err
	}

	accessTokenEndpoint, err := resolveAccessTokenEndpoint(ctx, c, oidcOpts)
	if err != nil {
		return nil, fmt.Errorf("OIDC access token endpoint resolution failed: %w", err)
	}

	accessToken, err := fetchAccessToken(ctx, c, oidcOpts, accessTokenEndpoint)
	if err != nil {
		return nil, fmt.Errorf("OIDC IdP token request failed: %w", err)
	}

	unscopedToken, unscopedBody, unscopedHeader, err := exchangeForKeystoneToken(ctx, c, oidcOpts, accessToken)
	if err != nil {
		return nil, fmt.Errorf("OIDC federation auth failed: %w", err)
	}

	scope, err := oidcOpts.Scope.ToScopeMap()
	if err != nil {
		return nil, err
	}

	if scope == nil {
		return v3AuthResult(gophercloud.Result{Body: unscopedBody, Header: unscopedHeader}, false)
	}
	rescope := AuthOptionsV3{AuthURL: c.Endpoint, Auth: V3TokenOpts{Token: unscopedToken, Scope: oidcOpts.Scope}}
	return rescope.Authenticate(ctx, c.ProviderClient)
}

func resolveAccessTokenEndpoint(ctx context.Context, c *gophercloud.ServiceClient, opts *V3OIDCClientCredentialsOpts) (string, error) {
	if opts.AccessTokenEndpoint != "" {
		if err := checkOIDCEndpoint("access token endpoint", opts.AccessTokenEndpoint, true); err != nil {
			return "", err
		}
		return opts.AccessTokenEndpoint, nil
	}

	// The discovery document says where the client secret is sent, so it
	// must not be readable or replaceable in transit.
	if err := checkOIDCEndpoint("discovery endpoint", opts.DiscoveryEndpoint, true); err != nil {
		return "", err
	}
	discovery, err := fetchDiscoveryDocument(ctx, c, opts.DiscoveryEndpoint)
	if err != nil {
		return "", err
	}

	if len(discovery.GrantTypesSupported) > 0 && !slices.Contains(discovery.GrantTypesSupported, "client_credentials") {
		return "", fmt.Errorf("IdP does not support client_credentials grant type (supported: %v)", discovery.GrantTypesSupported)
	}

	if discovery.TokenEndpoint == "" {
		return "", fmt.Errorf("discovery document does not contain a valid token_endpoint")
	}
	// A remote identity provider must not point the client secret at a
	// plaintext port on this machine.
	discoveryURL, _ := url.Parse(opts.DiscoveryEndpoint)
	if err := checkOIDCEndpoint("discovery document token_endpoint", discovery.TokenEndpoint, isLoopbackHost(discoveryURL.Hostname())); err != nil {
		return "", err
	}

	return discovery.TokenEndpoint, nil
}

// checkOIDCEndpoint refuses an endpoint that would carry the client secret,
// or say where to send it, without TLS. Plain HTTP is allowed only on
// loopback, and only when allowLoopbackHTTP is set.
func checkOIDCEndpoint(name, endpoint string, allowLoopbackHTTP bool) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("oidc: invalid %s: %w", name, err)
	}
	if u.Scheme == "https" || (allowLoopbackHTTP && u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return nil
	}
	return fmt.Errorf("oidc: %s %q does not use https", name, endpoint)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func fetchDiscoveryDocument(ctx context.Context, c *gophercloud.ServiceClient, discoveryURL string) (oidcDiscoveryDocument, error) {
	var doc oidcDiscoveryDocument

	req, err := http.NewRequestWithContext(ctx, "GET", discoveryURL, nil)
	if err != nil {
		return doc, fmt.Errorf("failed to create discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := doOIDCRequest(c.HTTPClient, req)
	if err != nil {
		return doc, fmt.Errorf("failed to fetch discovery document from %s: %w", discoveryURL, err)
	}
	defer resp.Body.Close()

	const maxDiscoverySize = 1 << 20 // 1 MiB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoverySize+1))
	if err != nil {
		return doc, fmt.Errorf("failed to read discovery document response: %w", err)
	}
	if int64(len(body)) > maxDiscoverySize {
		return doc, fmt.Errorf("discovery document exceeds maximum allowed size of %d bytes", maxDiscoverySize)
	}

	if resp.StatusCode != http.StatusOK {
		return doc, fmt.Errorf("discovery endpoint returned HTTP %d", resp.StatusCode)
	}

	if err := json.Unmarshal(body, &doc); err != nil {
		return doc, fmt.Errorf("discovery document is not valid JSON: %w", err)
	}
	return doc, nil
}

func fetchAccessToken(ctx context.Context, c *gophercloud.ServiceClient, opts *V3OIDCClientCredentialsOpts, accessTokenEndpoint string) (string, error) {
	scope := opts.OIDCScope
	if scope == "" {
		scope = "openid"
	}

	formData := url.Values{
		"grant_type": {"client_credentials"},
		"scope":      {scope},
	}
	if opts.ClientSecret == "" {
		formData.Set("client_id", opts.ClientID)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", accessTokenEndpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	// Send the credentials as they are, like keystoneauth. Identity providers
	// that work with the OpenStack CLI then work here too.
	if opts.ClientSecret != "" {
		req.SetBasicAuth(opts.ClientID, opts.ClientSecret)
	}

	resp, err := doOIDCRequest(c.HTTPClient, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	const maxIDPResponseSize = 1 << 20 // 1 MiB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIDPResponseSize+1))
	if err != nil {
		return "", fmt.Errorf("failed to read IdP response: %w", err)
	}
	if int64(len(body)) > maxIDPResponseSize {
		return "", fmt.Errorf("IdP response body exceeded %d bytes", maxIDPResponseSize)
	}

	if resp.StatusCode != http.StatusOK {
		snippet := string(body)
		if len(snippet) > 512 {
			snippet = snippet[:512] + "..."
		}
		return "", fmt.Errorf("IdP returned HTTP %d: %s", resp.StatusCode, snippet)
	}

	var tokenResponse map[string]any
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		return "", fmt.Errorf("failed to parse IdP token response: %w", err)
	}

	tokenField := opts.AccessTokenType
	if tokenField == "" {
		tokenField = "access_token"
	}

	accessToken, ok := tokenResponse[tokenField].(string)
	if !ok || accessToken == "" {
		return "", fmt.Errorf("IdP response missing %q field", tokenField)
	}
	if tokenField == "access_token" {
		tokenType, ok := tokenResponse["token_type"].(string)
		if !ok || !strings.EqualFold(tokenType, "Bearer") {
			return "", fmt.Errorf("IdP response has unsupported token_type %q", tokenType)
		}
	}

	return accessToken, nil
}

func exchangeForKeystoneToken(ctx context.Context, c *gophercloud.ServiceClient, opts *V3OIDCClientCredentialsOpts, accessToken string) (string, any, http.Header, error) {
	federationURL := oidcAuthURL(c, opts.IdentityProviderName, opts.Protocol)

	var body any
	resp, err := c.Post(ctx, federationURL, nil, &body, &gophercloud.RequestOpts{
		MoreHeaders: map[string]string{
			"Authorization": "Bearer " + accessToken,
		},
		OkCodes:     []int{201},
		OmitHeaders: []string{"X-Auth-Token"},
	})
	if err != nil {
		return "", nil, nil, err
	}

	unscopedToken := resp.Header.Get("X-Subject-Token")
	if unscopedToken == "" {
		return "", nil, nil, fmt.Errorf("federation auth response missing X-Subject-Token header")
	}

	return unscopedToken, body, resp.Header, nil
}

// GetAuthURL returns the versioned Keystone endpoint.
func (opts *V3OIDCClientCredentialsOpts) GetAuthURL() string {
	return (AuthOptionsV3{AuthURL: opts.AuthURL}).GetAuthURL()
}

// Authenticate exchanges OIDC client credentials for a Keystone token.
func (opts *V3OIDCClientCredentialsOpts) Authenticate(ctx context.Context, provider *gophercloud.ProviderClient) (*AuthResult, error) {
	if opts == nil {
		return nil, fmt.Errorf("oidc: expected non-nil options")
	}
	if opts.AuthURL == "" {
		return nil, gophercloud.ErrMissingInput{Argument: "AuthURL"}
	}
	result, err := authenticateOIDC(ctx, newAuthClient(provider, opts.GetAuthURL()), opts)
	if err != nil {
		return nil, err
	}
	result.CanReauth = opts.AllowReauth
	return result, nil
}

// Token requests must stay on their original origin. Discovery may redirect
// across hosts, but neither request may downgrade HTTPS to plaintext.
func doOIDCRequest(client http.Client, req *http.Request) (*http.Response, error) {
	checkRedirect := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if checkRedirect != nil {
			if err := checkRedirect(next, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if via[len(via)-1].URL.Scheme == "https" && next.URL.Scheme != "https" {
			return fmt.Errorf("OIDC request cannot redirect from HTTPS to plaintext")
		}
		if req.Method == http.MethodPost {
			if oidcOrigin(req.URL) != oidcOrigin(next.URL) {
				return fmt.Errorf("OIDC token request cannot redirect to another origin")
			}
		}
		return nil
	}
	return client.Do(req)
}

func oidcOrigin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}
