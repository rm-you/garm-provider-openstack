package client

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"strconv"

	"github.com/gophercloud/gophercloud/v2/auth"
	"github.com/gophercloud/gophercloud/v2/openstack/config/clouds"

	"github.com/cloudbase/garm-provider-openstack/config"
)

// envFallbacks are the auth settings that OS_* variables supply when
// clouds.yaml leaves them unset. The first variable that is set wins.
var envFallbacks = []struct {
	key  string
	vars []string
}{
	{"auth_url", []string{"OS_AUTH_URL"}},
	{"username", []string{"OS_USERNAME"}},
	{"user_id", []string{"OS_USER_ID"}},
	{"password", []string{"OS_PASSWORD"}},
	{"token", []string{"OS_AUTH_TOKEN", "OS_TOKEN"}},
	{"project_id", []string{"OS_PROJECT_ID", "OS_TENANT_ID"}},
	{"project_name", []string{"OS_PROJECT_NAME", "OS_TENANT_NAME"}},
	{"domain_id", []string{"OS_DOMAIN_ID"}},
	{"domain_name", []string{"OS_DOMAIN_NAME"}},
	{"default_domain", []string{"OS_DEFAULT_DOMAIN"}},
	{"user_domain_id", []string{"OS_USER_DOMAIN_ID"}},
	{"user_domain_name", []string{"OS_USER_DOMAIN_NAME"}},
	{"project_domain_id", []string{"OS_PROJECT_DOMAIN_ID"}},
	{"project_domain_name", []string{"OS_PROJECT_DOMAIN_NAME"}},
	{"application_credential_id", []string{"OS_APPLICATION_CREDENTIAL_ID"}},
	{"application_credential_name", []string{"OS_APPLICATION_CREDENTIAL_NAME"}},
	{"application_credential_secret", []string{"OS_APPLICATION_CREDENTIAL_SECRET"}},
	{"system_scope", []string{"OS_SYSTEM_SCOPE"}},
	{"trust_id", []string{"OS_TRUST_ID"}},
	{"identity_provider", []string{"OS_IDENTITY_PROVIDER"}},
	{"protocol", []string{"OS_PROTOCOL"}},
	{"client_id", []string{"OS_CLIENT_ID"}},
	{"client_secret", []string{"OS_CLIENT_SECRET"}},
	{"access_token_endpoint", []string{"OS_ACCESS_TOKEN_ENDPOINT"}},
	{"access_token_type", []string{"OS_ACCESS_TOKEN_TYPE"}},
	{"discovery_endpoint", []string{"OS_DISCOVERY_ENDPOINT"}},
	{"openid_scope", []string{"OS_OPENID_SCOPE"}},
	{"oauth2_endpoint", []string{"OS_OAUTH2_ENDPOINT"}},
	{"oauth2_client_id", []string{"OS_OAUTH2_CLIENT_ID"}},
	{"redirect_host", []string{"OS_REDIRECT_HOST"}},
	{"redirect_port", []string{"OS_REDIRECT_PORT"}},
}

// compatibleCloud makes core Gophercloud authenticate with the clouds.yaml
// settings and OS_* variables the way gophercloud/utils did:
//
//   - OS_* variables fill auth settings that clouds.yaml leaves unset.
//   - A token selects token authentication, even when auth_type names
//     password authentication.
//   - For a project or an application credential, domain_id and domain_name
//     fill each unset user and project domain setting, and default_domain is
//     the last fallback for both.
//   - A trust wins over a project, a project over system scope, and system
//     scope over a domain. Without a project, domain_id or domain_name is the
//     domain scope.
//
// It also covers what gophercloud/utils did not support: OS_AUTH_TYPE sets an
// auth type that clouds.yaml leaves unset, the federated auth settings have
// OS_* fallbacks, and OAuth2 mTLS falls back to client_id for its client ID.
func compatibleCloud(cloud clouds.Cloud) clouds.Cloud {
	m := maps.Clone(cloud.Auth)
	if m == nil {
		m = map[string]any{}
	}
	for _, fallback := range envFallbacks {
		if authString(m, fallback.key) != "" {
			continue
		}
		for _, name := range fallback.vars {
			if value := os.Getenv(name); value != "" {
				m[fallback.key] = value
				break
			}
		}
	}

	authType := cloud.AuthType
	if authType == "" {
		authType = auth.AuthType(os.Getenv("OS_AUTH_TYPE"))
	}
	if authString(m, "token") != "" {
		switch authType {
		case "", auth.AuthV3Password, auth.AuthV3Token:
			authType = auth.AuthV3Token
		case auth.AuthPassword:
			authType = auth.AuthToken
		case auth.AuthV2Password:
			authType = auth.AuthV2Token
		}
	}

	project := authString(m, "project_id") != "" || authString(m, "project_name") != ""
	appCredential := authString(m, "application_credential_id") != "" ||
		authString(m, "application_credential_name") != "" ||
		authString(m, "application_credential_secret") != ""
	switch {
	case authString(m, "trust_id") != "":
		for _, key := range []string{"system_scope", "project_id", "project_name", "project_domain_id", "project_domain_name", "domain_id", "domain_name"} {
			delete(m, key)
		}
	case project || appCredential:
		setDomains(m)
		delete(m, "system_scope")
	case authString(m, "system_scope") != "":
		delete(m, "domain_id")
		delete(m, "domain_name")
	}
	delete(m, "default_domain")

	if authType == auth.AuthV3OAuth2MTLSClientCredential && authString(m, "oauth2_client_id") == "" {
		if clientID := authString(m, "client_id"); clientID != "" {
			m["oauth2_client_id"] = clientID
		}
	}

	cloud.Auth = m
	cloud.AuthType = authType
	return cloud
}

// setDomains moves domain_id and domain_name into each unset user and project
// domain setting, then falls back to default_domain, as gophercloud/utils did.
func setDomains(m map[string]any) {
	for _, generic := range []struct{ key, user, project string }{
		{"domain_id", "user_domain_id", "project_domain_id"},
		{"domain_name", "user_domain_name", "project_domain_name"},
	} {
		value := authString(m, generic.key)
		if value == "" {
			continue
		}
		for _, key := range []string{generic.user, generic.project} {
			if authString(m, key) == "" {
				m[key] = value
			}
		}
		delete(m, generic.key)
	}

	defaultDomain := authString(m, "default_domain")
	if defaultDomain == "" {
		return
	}
	if authString(m, "user_domain_id") == "" && authString(m, "user_domain_name") == "" {
		m["user_domain_id"] = defaultDomain
	}
	if authString(m, "project_domain_id") == "" && authString(m, "project_domain_name") == "" {
		m["project_domain_id"] = defaultDomain
	}
}

func authString(m map[string]any, key string) string {
	if value, ok := m[key]; ok && value != nil {
		return fmt.Sprint(value)
	}
	return ""
}

// cloudParseOptions returns the clouds.Parse options for the provider's
// configuration. A configured secure.yaml or clouds-public.yaml that does not
// exist is skipped, as the config package and gophercloud/utils skip it.
func cloudParseOptions(cfg *config.Config) ([]clouds.ParseOption, error) {
	cloudsYAML, err := os.ReadFile(cfg.Credentials.Clouds)
	if err != nil {
		return nil, fmt.Errorf("failed to read clouds.yaml: %w", err)
	}
	opts := []clouds.ParseOption{
		clouds.WithCloudName(cfg.Cloud),
		clouds.WithCloudsYAML(bytes.NewReader(cloudsYAML)),
	}
	for _, file := range []struct {
		name, path string
		option     func(io.Reader) clouds.ParseOption
	}{
		{"secure.yaml", cfg.Credentials.SecureClouds, clouds.WithSecureYAML},
		{"clouds-public.yaml", cfg.Credentials.PublicClouds, clouds.WithCloudsPublicYAML},
	} {
		if file.path == "" {
			continue
		}
		data, err := os.ReadFile(file.path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", file.name, err)
		}
		opts = append(opts, file.option(bytes.NewReader(data)))
	}

	fileOpts, err := fileOverEnvironment(cfg.Credentials, cfg.Cloud)
	if err != nil {
		return nil, err
	}
	return append(opts, fileOpts...), nil
}

// fileOverEnvironment returns clouds.Parse options that give the cloud's
// region, interface and TLS settings in clouds.yaml, secure.yaml and
// clouds-public.yaml precedence over OS_REGION_NAME, OS_INTERFACE, OS_CACERT,
// OS_CERT, OS_KEY and OS_INSECURE, as gophercloud/utils did. On its own,
// clouds.Parse prefers those variables over the files.
func fileOverEnvironment(creds config.Credentials, cloudName string) ([]clouds.ParseOption, error) {
	cloudsYAML, err := creds.LoadCloudsYAML()
	if err != nil {
		return nil, err
	}
	secureYAML, err := creds.LoadSecureCloudsYAML()
	if err != nil {
		return nil, err
	}
	publicYAML, err := creds.LoadPublicCloudsYAML()
	if err != nil {
		return nil, err
	}

	// Earlier entries win, as when clouds.Parse merges the files.
	var entries []clouds.Cloud
	for _, source := range []map[string]clouds.Cloud{secureYAML, cloudsYAML} {
		if entry, ok := source[cloudName]; ok {
			entries = append(entries, entry)
		}
	}
	value := func(get func(clouds.Cloud) string) string {
		for _, entry := range entries {
			if v := get(entry); v != "" {
				return v
			}
		}
		return ""
	}
	profile := firstNonEmpty(value(func(c clouds.Cloud) string { return c.Profile }), value(func(c clouds.Cloud) string { return c.Cloud }))
	if entry, ok := publicYAML[profile]; ok && profile != "" {
		entries = append(entries, entry)
	}

	opts := []clouds.ParseOption{
		clouds.WithRegion(firstNonEmpty(value(func(c clouds.Cloud) string { return c.RegionName }), os.Getenv("OS_REGION_NAME"))),
		clouds.WithEndpointType(firstNonEmpty(
			value(func(c clouds.Cloud) string { return c.EndpointType }),
			value(func(c clouds.Cloud) string { return c.Interface }),
			os.Getenv("OS_INTERFACE"),
		)),
	}
	if path := firstNonEmpty(value(func(c clouds.Cloud) string { return c.CACertFile }), os.Getenv("OS_CACERT")); path != "" {
		opts = append(opts, clouds.WithCACertPath(path))
	}
	if path := firstNonEmpty(value(func(c clouds.Cloud) string { return c.ClientCertFile }), os.Getenv("OS_CERT")); path != "" {
		opts = append(opts, clouds.WithClientCertPath(path))
	}
	if path := firstNonEmpty(value(func(c clouds.Cloud) string { return c.ClientKeyFile }), os.Getenv("OS_KEY")); path != "" {
		opts = append(opts, clouds.WithClientKeyPath(path))
	}

	verifySet := false
	for _, entry := range entries {
		verifySet = verifySet || entry.Verify != nil
	}
	if insecure := os.Getenv("OS_INSECURE"); insecure != "" && !verifySet {
		value, err := strconv.ParseBool(insecure)
		if err != nil {
			return nil, fmt.Errorf("failed to parse OS_INSECURE: %w", err)
		}
		opts = append(opts, clouds.WithInsecure(value))
	}
	return opts, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
