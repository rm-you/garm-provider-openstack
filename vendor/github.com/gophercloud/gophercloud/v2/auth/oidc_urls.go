package auth

import "github.com/gophercloud/gophercloud/v2"

func oidcAuthURL(c *gophercloud.ServiceClient, idpID, protocolID string) string {
	return c.ServiceURL("OS-FEDERATION", "identity_providers", idpID, "protocols", protocolID, "auth")
}
