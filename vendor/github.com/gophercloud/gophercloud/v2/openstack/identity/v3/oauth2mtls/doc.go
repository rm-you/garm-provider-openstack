/*
Package oauth2mtls authenticates to Keystone using OAuth 2.0 Mutual-TLS Client
Authentication (RFC 8705).

This implements the v3oauth2mtlsclientcredential authentication flow, which
uses a client TLS certificate to authenticate directly to Keystone's
OS-OAUTH2 token endpoint. The client certificate's Subject DN CN must be
listed in Keystone's oauth2mtls-mapping, and the user must have a
default_project_id set.

The returned token is project-scoped to the user's default_project_id.

Example to Authenticate a Client Using OAuth2 mTLS

	client, err := openstack.NewClient("https://keystone.example.com:5000/v3")
	if err != nil {
		panic(err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caCertPool,
	}
	client.HTTPClient.Transport = &http.Transport{TLSClientConfig: tlsConfig}

	authOptions := &oauth2mtls.AuthOptions{
		OAuth2Endpoint: "https://keystone.example.com:5000/v3/OS-OAUTH2/token",
		ClientID:       "6c3145f4-313d-4910-b3a8-9dfc72da9e75",
		AllowReauth:    true,
	}

	err = openstack.AuthenticateV3(context.TODO(), client, authOptions, gophercloud.EndpointOpts{})
	if err != nil {
		panic(err)
	}

Reference:
https://opendev.org/openstack/keystoneauth/src/branch/stable/2025.1/keystoneauth1/identity/v3/oauth2_mtls.py
*/
package oauth2mtls
