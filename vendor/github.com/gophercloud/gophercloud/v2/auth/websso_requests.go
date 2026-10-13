package auth

import (
	"context"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/auth/tokencache"
)

const (
	webSSODefaultRedirectPort = 9990
	webSSODefaultTimeout      = 5 * time.Minute
)

// V3WebSSOOpts contains WebSSO authentication options.
type V3WebSSOOpts struct {
	// AuthURL is the Keystone identity endpoint.
	AuthURL string
	// IdentityProviderName is the Keystone federation identity provider.
	IdentityProviderName string `required:"true"`

	// Protocol is the Keystone federation protocol.
	Protocol string `required:"true"`

	// AllowReauth allows Gophercloud to reauthenticate automatically when the
	// token expires.
	AllowReauth bool

	// Scope controls the resulting Keystone token scope.
	Scope *Scope

	// RedirectHost is the loopback callback host. It defaults to localhost.
	RedirectHost string

	// RedirectPort is the callback listener port. It defaults to 9990.
	RedirectPort int

	// Timeout limits the browser flow. It defaults to five minutes.
	Timeout time.Duration

	// BrowserOpener opens the WebSSO URL. It defaults to the OS browser.
	BrowserOpener func(string) error `json:"-"`

	// TokenCache enables unscoped token reuse and requires CacheNamespace.
	TokenCache tokencache.Cache `json:"-"`

	// CacheNamespace identifies the expected browser login profile.
	CacheNamespace string
}

// WebSSOCacheKey returns the identity-isolated cache key for opts.
func WebSSOCacheKey(identityEndpoint string, opts *V3WebSSOOpts) string {
	return tokencache.Key(tokencache.KeyOptions{
		Flow:             "websso",
		Principal:        opts.CacheNamespace,
		IdentityEndpoint: identityEndpoint,
		IdentityProvider: opts.IdentityProviderName,
		Protocol:         opts.Protocol,
	})
}

// authenticateWebSSO reuses a cached unscoped token or captures a new one
// from the browser, then rescopes it when opts requests a scope.
func authenticateWebSSO(ctx context.Context, client *gophercloud.ServiceClient, opts *V3WebSSOOpts) (*AuthResult, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	scope, err := opts.Scope.ToScopeMap()
	if err != nil {
		return nil, err
	}

	var unscoped *AuthResult
	cacheKey := ""
	if opts.TokenCache != nil {
		cacheKey = WebSSOCacheKey(client.Endpoint, opts)
		if cachedToken, ok := tokencache.Load(opts.TokenCache, cacheKey, client.Endpoint); ok {
			cached, err := validateToken(ctx, client, cachedToken)
			switch {
			case err == nil:
				unscoped = cached
			case gophercloud.ResponseCodeIs(err, http.StatusUnauthorized),
				gophercloud.ResponseCodeIs(err, http.StatusNotFound):
				_ = opts.TokenCache.Delete(cacheKey)
			default:
				return nil, err
			}
		}
	}
	if unscoped == nil {
		port := opts.RedirectPort
		if port == 0 {
			port = webSSODefaultRedirectPort
		}
		timeout := opts.Timeout
		if timeout == 0 {
			timeout = webSSODefaultTimeout
		}

		token, err := captureToken(ctx, client, opts, port, timeout)
		if err != nil {
			return nil, err
		}
		unscoped, err = validateToken(ctx, client, token)
		if err != nil {
			return nil, err
		}
		if opts.TokenCache != nil {
			tokencache.Persist(opts.TokenCache, cacheKey, client.Endpoint, unscoped.TokenID, unscoped.ExpiresAt)
		}
	}

	if scope == nil {
		return unscoped, nil
	}
	rescope := AuthOptionsV3{AuthURL: client.Endpoint, Auth: V3RescopeTokenOpts{Token: unscoped.TokenID, Scope: opts.Scope}}
	return rescope.Authenticate(ctx, client.ProviderClient)
}

// validateToken retrieves the token's details, authenticating with the token
// itself.
func validateToken(ctx context.Context, client *gophercloud.ServiceClient, tokenID string) (*AuthResult, error) {
	validationClient := newAuthClient(client.ProviderClient, client.Endpoint)
	var result gophercloud.Result
	resp, err := validationClient.Get(ctx, validationClient.ServiceURL("auth", "tokens"), &result.Body, &gophercloud.RequestOpts{
		MoreHeaders: map[string]string{"X-Auth-Token": tokenID, "X-Subject-Token": tokenID},
		OkCodes:     []int{http.StatusOK, http.StatusNonAuthoritativeInfo},
	})
	_, result.Header, result.Err = gophercloud.ParseResponse(resp, err)
	if result.Err == nil {
		result.Header.Set("X-Subject-Token", tokenID)
	}
	return v3AuthResult(result, false)
}

func (opts *V3WebSSOOpts) validate() error {
	if _, err := gophercloud.BuildRequestBody(opts, ""); err != nil {
		return err
	}
	if opts.RedirectPort < 0 || opts.RedirectPort > 65535 {
		return fmt.Errorf("websso: RedirectPort must be 0 or between 1 and 65535")
	}
	if opts.Timeout < 0 {
		return fmt.Errorf("websso: Timeout must not be negative")
	}
	if opts.TokenCache != nil && opts.CacheNamespace == "" {
		return fmt.Errorf("websso: CacheNamespace is required when TokenCache is enabled")
	}
	if opts.RedirectHost != "" && opts.RedirectHost != "localhost" {
		ip := net.ParseIP(opts.RedirectHost)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("websso: RedirectHost must be a loopback address")
		}
	}
	return nil
}

func captureToken(ctx context.Context, client *gophercloud.ServiceClient, opts *V3WebSSOOpts, port int, timeout time.Duration) (string, error) {
	const callbackPath = "/auth/websso/"
	keystoneOrigin, err := endpointOrigin(client.Endpoint)
	if err != nil {
		return "", fmt.Errorf("websso: invalid identity endpoint: %w", err)
	}
	host := opts.RedirectHost
	if host == "" {
		host = "localhost"
	}
	origin := "http://" + net.JoinHostPort(host, fmt.Sprintf("%d", port)) + callbackPath
	webSSOURL := webSSOAuthURL(client, opts.IdentityProviderName, opts.Protocol) + "?origin=" + url.QueryEscape(origin)

	listeners, err := listenLoopback(host, port)
	if err != nil {
		return "", err
	}

	tokenCh := make(chan string, 1)
	errCh := make(chan error, len(listeners))
	var accepted atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != callbackPath {
			http.NotFound(w, request)
			return
		}
		if request.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/x-www-form-urlencoded" {
			http.Error(w, "Unsupported Content-Type", http.StatusUnsupportedMediaType)
			return
		}
		if !validCallbackContext(request, keystoneOrigin) {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		request.Body = http.MaxBytesReader(w, request.Body, 64<<10)
		if err := request.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		token := request.PostFormValue("token")
		if token == "" {
			http.Error(w, "Missing token", http.StatusBadRequest)
			return
		}
		if !accepted.CompareAndSwap(false, true) {
			http.Error(w, "Token already received", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body><script>window.close()</script><h2>Authentication successful</h2><p>You may close this window.</p></body></html>")
		tokenCh <- token
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	for _, listener := range listeners {
		go func() {
			if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
				errCh <- fmt.Errorf("websso: callback server error: %w", err)
			}
		}()
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
	}()

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("websso: authentication cancelled: %w", err)
	}
	opener := opts.BrowserOpener
	if opener == nil {
		opener = openBrowser
	}
	if err := opener(webSSOURL); err != nil {
		return "", fmt.Errorf("websso: failed to open browser: %w", err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case token := <-tokenCh:
		return token, nil
	case err := <-errCh:
		return "", err
	case <-timer.C:
		return "", fmt.Errorf("websso: authentication timed out after %s", timeout)
	case <-ctx.Done():
		return "", fmt.Errorf("websso: authentication cancelled: %w", ctx.Err())
	}
}

// listenLoopback listens for the callback. Browsers may resolve localhost to
// either loopback address, so it listens on both; otherwise another local
// process could receive the token on the one left unused.
func listenLoopback(host string, port int) ([]net.Listener, error) {
	listen := func(host string) (net.Listener, error) {
		address := net.JoinHostPort(host, strconv.Itoa(port))
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return nil, fmt.Errorf("websso: failed to start callback server on %s: %w", address, err)
		}
		return listener, nil
	}

	if host != "localhost" {
		listener, err := listen(host)
		if err != nil {
			return nil, err
		}
		return []net.Listener{listener}, nil
	}

	ipv4, err := listen("127.0.0.1")
	if err != nil {
		return nil, err
	}
	ipv6, err := listen("::1")
	if err == nil {
		return []net.Listener{ipv4, ipv6}, nil
	}
	probe, probeErr := net.Listen("tcp", "[::1]:0")
	if probeErr != nil {
		// Without IPv6 loopback, browsers can only use 127.0.0.1.
		return []net.Listener{ipv4}, nil
	}
	_ = probe.Close()
	_ = ipv4.Close()
	return nil, err
}

func endpointOrigin(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("url must include a scheme and host")
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}

func validCallbackContext(request *http.Request, keystoneOrigin string) bool {
	if request.Header.Get("Sec-Fetch-Mode") != "navigate" ||
		request.Header.Get("Sec-Fetch-Dest") != "document" ||
		request.Header.Get("Sec-Fetch-Site") == "none" {
		return false
	}

	origin := request.Header.Get("Origin")
	if origin != "" && origin != "null" && !sameOrigin(origin, keystoneOrigin) {
		return false
	}
	referer := request.Header.Get("Referer")
	return referer == "" || sameOrigin(referer, keystoneOrigin)
}

func sameOrigin(rawURL, expected string) bool {
	origin, err := endpointOrigin(rawURL)
	return err == nil && origin == expected
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "linux":
		command = exec.Command("xdg-open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return fmt.Errorf("unsupported operating system %q", runtime.GOOS)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go func() {
		_ = command.Wait()
	}()
	return nil
}

// GetAuthURL returns the versioned Keystone endpoint.
func (opts *V3WebSSOOpts) GetAuthURL() string {
	return (AuthOptionsV3{AuthURL: opts.AuthURL}).GetAuthURL()
}

// Authenticate opens the browser when no reusable unscoped token is available.
func (opts *V3WebSSOOpts) Authenticate(ctx context.Context, provider *gophercloud.ProviderClient) (*AuthResult, error) {
	if opts == nil {
		return nil, fmt.Errorf("websso: expected non-nil options")
	}
	if opts.AuthURL == "" {
		return nil, gophercloud.ErrMissingInput{Argument: "AuthURL"}
	}
	result, err := authenticateWebSSO(ctx, newAuthClient(provider, opts.GetAuthURL()), opts)
	if err != nil {
		return nil, err
	}
	result.CanReauth = opts.AllowReauth
	return result, nil
}
