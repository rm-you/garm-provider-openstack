package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
)

// V3OAuth2MTLSOpts contains OAuth2 mTLS client-credentials options.
type V3OAuth2MTLSOpts struct {
	// AuthURL is the Keystone identity endpoint.
	AuthURL string
	// OAuth2Endpoint specifies Keystone's OS-OAUTH2 token endpoint. When empty,
	// it is derived from the identity ServiceClient endpoint.
	OAuth2Endpoint string

	// ClientID is the Keystone user ID associated with the client certificate.
	ClientID string `required:"true"`

	// AllowReauth enables automatic reauthentication.
	AllowReauth bool
}

type oauth2TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// authenticateOAuth2MTLS obtains an OAuth2 access token with the client
// certificate and retrieves the Keystone token it represents.
func authenticateOAuth2MTLS(ctx context.Context, c *gophercloud.ServiceClient, mtlsOpts *V3OAuth2MTLSOpts) (*AuthResult, error) {
	if _, err := gophercloud.BuildRequestBody(mtlsOpts, ""); err != nil {
		return nil, err
	}

	oauth2Endpoint := mtlsOpts.OAuth2Endpoint
	if oauth2Endpoint == "" {
		oauth2Endpoint = oauth2TokenURL(c)
	}

	formData := url.Values{
		"grant_type": {"client_credentials"},
		"client_id":  {mtlsOpts.ClientID},
	}

	var tokenResp oauth2TokenResponse
	resp, err := c.Post(ctx, oauth2Endpoint, strings.NewReader(formData.Encode()), &tokenResp, &gophercloud.RequestOpts{
		MoreHeaders: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		OkCodes:     []int{http.StatusOK},
	})
	if _, _, err := gophercloud.ParseResponse(resp, err); err != nil {
		return nil, err
	}

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("oauth2mtls: token response missing access_token field")
	}
	if !strings.EqualFold(tokenResp.TokenType, "Bearer") {
		return nil, fmt.Errorf("oauth2mtls: token response has unsupported token_type %q", tokenResp.TokenType)
	}

	// The OS-OAUTH2 response has no catalog, so retrieve the full token.
	var result gophercloud.Result
	resp, err = c.Get(ctx, c.ServiceURL("auth", "tokens"), &result.Body, &gophercloud.RequestOpts{
		MoreHeaders: map[string]string{
			"X-Auth-Token":    tokenResp.AccessToken,
			"X-Subject-Token": tokenResp.AccessToken,
		},
	})
	_, result.Header, result.Err = gophercloud.ParseResponse(resp, err)
	if result.Err == nil {
		result.Header.Set("X-Subject-Token", tokenResp.AccessToken)
	}
	return v3AuthResult(result, false)
}

// GetAuthURL returns the versioned Keystone endpoint.
func (opts *V3OAuth2MTLSOpts) GetAuthURL() string {
	return (AuthOptionsV3{AuthURL: opts.AuthURL}).GetAuthURL()
}

// Authenticate uses the HTTP client's certificate to obtain a bearer token.
func (opts *V3OAuth2MTLSOpts) Authenticate(ctx context.Context, provider *gophercloud.ProviderClient) (*AuthResult, error) {
	if opts == nil {
		return nil, fmt.Errorf("oauth2mtls: expected non-nil options")
	}
	if opts.AuthURL == "" {
		return nil, gophercloud.ErrMissingInput{Argument: "AuthURL"}
	}
	result, err := authenticateOAuth2MTLS(ctx, newAuthClient(provider, opts.GetAuthURL()), opts)
	if err != nil {
		return nil, err
	}
	result.TokenType = "Bearer"
	result.CanReauth = opts.AllowReauth
	return result, nil
}
