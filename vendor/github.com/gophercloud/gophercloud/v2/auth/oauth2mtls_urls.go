package auth

import "github.com/gophercloud/gophercloud/v2"

func oauth2TokenURL(c *gophercloud.ServiceClient) string {
	return c.ServiceURL("OS-OAUTH2", "token")
}
