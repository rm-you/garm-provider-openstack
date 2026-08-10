package oauth2mtls

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
)

// AuthOptions contains OAuth2 mTLS client-credentials options.
type AuthOptions struct {
	// OAuth2Endpoint specifies Keystone's OS-OAUTH2 token endpoint. When empty,
	// it is derived from the identity ServiceClient endpoint.
	OAuth2Endpoint string

	// ClientID is the Keystone user ID associated with the client certificate.
	ClientID string

	// AllowReauth enables automatic reauthentication.
	AllowReauth bool
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// ToTokenV3ScopeMap implements tokens.AuthOptionsBuilder.
func (opts *AuthOptions) ToTokenV3ScopeMap() (map[string]any, error) {
	return nil, nil
}

// ToTokenV3HeadersMap implements tokens.AuthOptionsBuilder.
func (opts *AuthOptions) ToTokenV3HeadersMap(map[string]any) (map[string]string, error) {
	return nil, nil
}

// ToTokenV3CreateMap implements tokens.AuthOptionsBuilder.
func (opts *AuthOptions) ToTokenV3CreateMap(map[string]any) (map[string]any, error) {
	return nil, nil
}

// CanReauth reports whether automatic reauthentication is enabled.
func (opts *AuthOptions) CanReauth() bool {
	return opts.AllowReauth
}

func (opts *AuthOptions) validate() error {
	if opts.ClientID == "" {
		return fmt.Errorf("oauth2mtls: missing required field ClientID (user_id)")
	}
	return nil
}

// Create authenticates with OAuth2 mTLS client credentials.
func Create(ctx context.Context, c *gophercloud.ServiceClient, opts tokens.AuthOptionsBuilder) (r tokens.CreateResult) {
	mtlsOpts, ok := opts.(*AuthOptions)
	if !ok || mtlsOpts == nil {
		r.Err = fmt.Errorf("oauth2mtls: expected non-nil *oauth2mtls.AuthOptions, got %T", opts)
		return
	}

	if err := mtlsOpts.validate(); err != nil {
		r.Err = err
		return
	}

	if c == nil || c.ProviderClient == nil {
		r.Err = fmt.Errorf("oauth2mtls: ServiceClient or ProviderClient is nil")
		return
	}

	tokenURL := mtlsOpts.OAuth2Endpoint
	if tokenURL == "" {
		tokenURL = c.ServiceURL("OS-OAUTH2", "token")
	}

	formData := url.Values{
		"grant_type": {"client_credentials"},
		"client_id":  {mtlsOpts.ClientID},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(formData.Encode()))
	if err != nil {
		r.Err = fmt.Errorf("oauth2mtls: creating token request: %w", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	httpClient := c.HTTPClient
	resp, err := httpClient.Do(req)
	if err != nil {
		r.Err = fmt.Errorf("oauth2mtls: token request to %s: %w", tokenURL, err)
		return
	}
	defer resp.Body.Close()

	const maxResponseSize = 1 << 20 // 1 MiB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		r.Err = fmt.Errorf("oauth2mtls: reading token response: %w", err)
		return
	}
	if int64(len(body)) > maxResponseSize {
		r.Err = fmt.Errorf("oauth2mtls: token response exceeds %d bytes", maxResponseSize)
		return
	}

	if resp.StatusCode != http.StatusOK {
		r.Err = fmt.Errorf("oauth2mtls: token request returned HTTP %d: %s", resp.StatusCode, extractError(body))
		return
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		r.Err = fmt.Errorf("oauth2mtls: decoding token response: %w", err)
		return
	}

	if tokenResp.AccessToken == "" {
		r.Err = fmt.Errorf("oauth2mtls: token response missing access_token field")
		return
	}
	if !strings.EqualFold(tokenResp.TokenType, "Bearer") {
		r.Err = fmt.Errorf("oauth2mtls: token response has unsupported token_type %q", tokenResp.TokenType)
		return
	}

	// The OS-OAUTH2 response has no catalog, so retrieve the full token.
	tokenBody, validateHeader, err := validateToken(ctx, c, tokenResp.AccessToken)
	if err != nil {
		r.Err = fmt.Errorf("oauth2mtls: token issued but validation failed (no service catalog available): %w", err)
		return
	}

	r.Header = validateHeader
	r.Header.Set("X-Subject-Token", tokenResp.AccessToken)
	r.Body = tokenBody

	return
}

func validateToken(ctx context.Context, c *gophercloud.ServiceClient, accessToken string) (any, http.Header, error) {
	if c == nil || c.ProviderClient == nil {
		return nil, nil, fmt.Errorf("oauth2mtls: ServiceClient or ProviderClient is nil")
	}

	validateURL := c.ServiceURL("auth", "tokens")

	req, err := http.NewRequestWithContext(ctx, "GET", validateURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("oauth2mtls: creating validate request: %w", err)
	}
	req.Header.Set("X-Auth-Token", accessToken)
	req.Header.Set("X-Subject-Token", accessToken)
	req.Header.Set("Accept", "application/json")

	httpClient := c.HTTPClient
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("oauth2mtls: validate token request: %w", err)
	}
	defer resp.Body.Close()

	const maxResponseSize = 1 << 20 // 1 MiB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("oauth2mtls: reading validate response: %w", err)
	}
	if int64(len(body)) > maxResponseSize {
		return nil, nil, fmt.Errorf("oauth2mtls: validate response exceeds %d bytes", maxResponseSize)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("oauth2mtls: validate token returned HTTP %d: %s", resp.StatusCode, extractError(body))
	}

	var tokenBody any
	if err := json.Unmarshal(body, &tokenBody); err != nil {
		return nil, nil, fmt.Errorf("oauth2mtls: decoding validate response: %w", err)
	}

	return tokenBody, resp.Header, nil
}

func extractError(body []byte) string {
	var errResp struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.ErrorDescription != "" {
		return errResp.ErrorDescription
	}
	if errResp.Error != "" {
		return errResp.Error
	}

	var keystoneErr struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &keystoneErr); err == nil && keystoneErr.Error.Message != "" {
		return keystoneErr.Error.Message
	}

	const maxRawLen = 200
	raw := strings.TrimSpace(string(body))
	if raw == "" {
		return "(empty response body)"
	}
	if len(raw) > maxRawLen {
		return raw[:maxRawLen] + "..."
	}
	return raw
}
