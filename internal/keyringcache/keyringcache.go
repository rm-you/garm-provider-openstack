// Package keyringcache stores Gophercloud tokens in the operating system keyring.
package keyringcache

import (
	"errors"

	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokencache"
	"github.com/zalando/go-keyring"
)

const serviceName = "gophercloud-auth"

// Cache implements tokencache.Cache using the operating system keyring.
type Cache struct{}

var _ tokencache.Cache = (*Cache)(nil)

// New creates a new keyring-backed token cache.
func New() *Cache {
	return &Cache{}
}

// Get returns ("", nil) if the key does not exist.
func (c *Cache) Get(key string) (string, error) {
	val, err := keyring.Get(serviceName, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return val, err
}

// Set stores a value.
func (c *Cache) Set(key, value string) error {
	return keyring.Set(serviceName, key, value)
}

// Delete removes a value and ignores missing keys.
func (c *Cache) Delete(key string) error {
	err := keyring.Delete(serviceName, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
