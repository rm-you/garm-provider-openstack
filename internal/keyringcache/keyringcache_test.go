package keyringcache

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestCache(t *testing.T) {
	keyring.MockInit()
	cache := New()

	value, err := cache.Get("profile")
	if err != nil {
		t.Fatalf("Get returned an error for a missing key: %v", err)
	}
	if value != "" {
		t.Fatalf("Get returned %q for a missing key", value)
	}

	if err := cache.Set("profile", "token"); err != nil {
		t.Fatalf("Set returned an error: %v", err)
	}
	value, err = cache.Get("profile")
	if err != nil {
		t.Fatalf("Get returned an error: %v", err)
	}
	if value != "token" {
		t.Fatalf("Get returned %q, want %q", value, "token")
	}

	if err := cache.Delete("profile"); err != nil {
		t.Fatalf("Delete returned an error: %v", err)
	}
	if err := cache.Delete("profile"); err != nil {
		t.Fatalf("Delete returned an error for a missing key: %v", err)
	}
}

func TestCacheErrors(t *testing.T) {
	wantErr := errors.New("keyring unavailable")
	keyring.MockInitWithError(wantErr)
	cache := New()

	if _, err := cache.Get("profile"); !errors.Is(err, wantErr) {
		t.Fatalf("Get returned %v, want %v", err, wantErr)
	}
	if err := cache.Set("profile", "token"); !errors.Is(err, wantErr) {
		t.Fatalf("Set returned %v, want %v", err, wantErr)
	}
	if err := cache.Delete("profile"); !errors.Is(err, wantErr) {
		t.Fatalf("Delete returned %v, want %v", err, wantErr)
	}
}
