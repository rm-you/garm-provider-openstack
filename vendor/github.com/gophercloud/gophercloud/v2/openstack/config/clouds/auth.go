package clouds

import "github.com/gophercloud/gophercloud/v2/auth"

// Implement the CloudSource interface in the auth package
func (c Cloud) GetAuthType() auth.AuthType {
	return c.AuthType
}

func (c Cloud) GetIdentityAPIVersion() string {
	return c.IdentityAPIVersion
}

func (c Cloud) GetAuthData() map[string]any {
	return c.Auth
}

// Build an Authenticator using parsed clouds.yaml. WebSSO token caching
// defaults to a namespace named after the cloud.
func (c Cloud) ToAuthOptions(opts ...auth.CloudOption) (auth.Authenticator, error) {
	if c.Name != "" {
		opts = append([]auth.CloudOption{auth.WithTokenCacheNamespace(c.Name)}, opts...)
	}
	return auth.AuthOptionsFromCloud(c, opts...)
}
