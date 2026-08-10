package oidc

import "github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokencache"

// CacheKey returns the identity-isolated token cache key for opts.
func CacheKey(identityEndpoint string, opts *AuthOptions) string {
	authEndpoint := opts.AccessTokenEndpoint
	if authEndpoint == "" {
		authEndpoint = opts.DiscoveryEndpoint
	}
	return tokencache.Key(tokencache.KeyOptions{
		Flow:                   "oidc-client-credentials",
		Principal:              opts.ClientID,
		IdentityEndpoint:       identityEndpoint,
		IdentityProvider:       opts.IdentityProviderName,
		Protocol:               opts.Protocol,
		AuthenticationEndpoint: authEndpoint,
		Scope:                  opts.Scope,
	})
}
