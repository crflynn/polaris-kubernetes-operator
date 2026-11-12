/*
Copyright 2026 Polaris Kubernetes Operator contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package polaris

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultRealmHeader        = "Polaris-Realm"
	DefaultNamespaceSeparator = "\x1f"
	DefaultRequestTimeout     = 10 * time.Second
	catalogConfigPath         = "/v1/config"
	clientCredentialsGrant    = "client_credentials"
)

// AuthConfig contains one authentication method for a Polaris client.
type AuthConfig struct {
	ClientID     string
	ClientSecret string
	BearerToken  string
	TokenURL     string
	Scopes       []string
}

// Config contains resolved connection settings.
type Config struct {
	ManagementURL      string
	CatalogURL         string
	Realm              string
	RealmHeader        string
	NamespaceSeparator string
	Auth               AuthConfig
	HTTPClient         *http.Client
}

// Client performs authenticated Polaris API checks and catalog operations.
type Client struct {
	managementURL      *url.URL
	catalogURL         *url.URL
	realm              string
	realmHeader        string
	namespaceSeparator string
	auth               AuthConfig
	httpClient         *http.Client
}

// CheckResult contains non-sensitive observations from the connection check.
type CheckResult struct {
	ManagementStatusCode int
	CatalogStatusCode    int
	Capabilities         []string
	NamespaceSeparator   string
}

// HTTPError represents an HTTP failure without retaining a potentially sensitive response body.
type HTTPError struct {
	Operation  string
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Polaris %s returned HTTP %d", e.Operation, e.StatusCode)
}

// NewHTTPClient constructs a client with the supplied trust roots.
func NewHTTPClient(timeout time.Duration, caPEM []byte) (*http.Client, error) {
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if len(caPEM) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("custom CA bundle does not contain a valid certificate")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}

	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// NewClient validates URLs and creates a Polaris API client.
func NewClient(cfg Config) (*Client, error) {
	managementURL, err := parseBaseURL(cfg.ManagementURL, "management URL")
	if err != nil {
		return nil, err
	}

	var catalogURL *url.URL
	if cfg.CatalogURL != "" {
		catalogURL, err = parseBaseURL(cfg.CatalogURL, "catalog URL")
		if err != nil {
			return nil, err
		}
	}

	if cfg.Auth.BearerToken == "" {
		if cfg.Auth.ClientID == "" || cfg.Auth.ClientSecret == "" || cfg.Auth.TokenURL == "" {
			return nil, errors.New("client credentials require client ID, client secret, and token URL")
		}
		tokenURL, err := parseBaseURL(cfg.Auth.TokenURL, "token URL")
		if err != nil {
			return nil, err
		}
		cfg.Auth.TokenURL = tokenURL.String()
	}
	if cfg.Auth.BearerToken != "" && (cfg.Auth.ClientID != "" || cfg.Auth.ClientSecret != "" || cfg.Auth.TokenURL != "") {
		return nil, errors.New("bearer token authentication cannot be combined with client credentials")
	}

	if cfg.RealmHeader == "" {
		cfg.RealmHeader = DefaultRealmHeader
	}
	if cfg.NamespaceSeparator == "" {
		cfg.NamespaceSeparator = DefaultNamespaceSeparator
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient, err = NewHTTPClient(DefaultRequestTimeout, nil)
		if err != nil {
			return nil, err
		}
	}

	return &Client{
		managementURL:      managementURL,
		catalogURL:         catalogURL,
		realm:              cfg.Realm,
		realmHeader:        cfg.RealmHeader,
		namespaceSeparator: cfg.NamespaceSeparator,
		auth:               cfg.Auth,
		httpClient:         cfg.HTTPClient,
	}, nil
}

// Check validates authentication and probes the configured Polaris endpoints.
func (c *Client) Check(ctx context.Context) (CheckResult, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return CheckResult{}, err
	}

	result := CheckResult{NamespaceSeparator: c.namespaceSeparator}
	managementEndpoint := joinURLPath(c.managementURL, "catalogs")
	result.ManagementStatusCode, err = c.probe(ctx, managementEndpoint, token, "management API")
	if err != nil {
		return result, err
	}

	if c.catalogURL == nil {
		return result, nil
	}

	var config catalogConfig
	catalogEndpoint := joinURLPath(c.catalogURL, catalogConfigPath)
	result.CatalogStatusCode, err = c.probeJSON(ctx, catalogEndpoint, token, "catalog API", &config)
	if err != nil {
		return result, err
	}
	result.Capabilities = append([]string(nil), config.Endpoints...)
	namespaceSeparator, err := configuredNamespaceSeparator(config)
	if err != nil {
		return result, err
	}
	result.NamespaceSeparator = namespaceSeparator
	return result, nil
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	if c.auth.BearerToken != "" {
		return c.auth.BearerToken, nil
	}

	form := url.Values{
		"grant_type":    {clientCredentialsGrant},
		"client_id":     {c.auth.ClientID},
		"client_secret": {c.auth.ClientSecret},
	}
	if len(c.auth.Scopes) > 0 {
		form.Set("scope", strings.Join(c.auth.Scopes, " "))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.auth.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.realm != "" {
		req.Header.Set(c.realmHeader, c.realm)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request Polaris token: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", &HTTPError{Operation: "token endpoint", StatusCode: resp.StatusCode}
	}

	var token tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return "", fmt.Errorf("decode Polaris token response: %w", err)
	}
	if token.AccessToken == "" {
		return "", errors.New("polaris token response did not contain access_token")
	}
	return token.AccessToken, nil
}

// Catalog is a Polaris management-service catalog representation.
type Catalog struct {
	Name                 string                    `json:"name"`
	Type                 string                    `json:"type"`
	Properties           map[string]string         `json:"properties"`
	StorageConfigInfo    StorageConfig             `json:"storageConfigInfo"`
	ConnectionConfigInfo *ExternalConnectionConfig `json:"connectionConfigInfo,omitempty"`
	EntityVersion        int64                     `json:"entityVersion"`
}

// StorageConfig is the non-secret Polaris catalog storage configuration.
type StorageConfig struct {
	StorageType      string   `json:"storageType"`
	AllowedLocations []string `json:"allowedLocations,omitempty"`
	StorageName      string   `json:"storageName,omitempty"`
}

// ExternalConnectionConfig describes the remote Iceberg REST service.
type ExternalConnectionConfig struct {
	ConnectionType           string                 `json:"connectionType"`
	URI                      string                 `json:"uri,omitempty"`
	RemoteCatalogName        string                 `json:"remoteCatalogName,omitempty"`
	Properties               map[string]string      `json:"properties,omitempty"`
	AuthenticationParameters ExternalAuthentication `json:"authenticationParameters"`
}

// ExternalAuthentication contains credentials sent only to Polaris on catalog creation.
type ExternalAuthentication struct {
	AuthenticationType string   `json:"authenticationType"`
	TokenURI           string   `json:"tokenUri,omitempty"`
	ClientID           string   `json:"clientId,omitempty"`
	ClientSecret       string   `json:"clientSecret,omitempty"`
	Scopes             []string `json:"scopes,omitempty"`
	BearerToken        string   `json:"bearerToken,omitempty"`
}

type createCatalogRequest struct {
	Catalog Catalog `json:"catalog"`
}

type updateCatalogRequest struct {
	CurrentEntityVersion int64             `json:"currentEntityVersion"`
	Properties           map[string]string `json:"properties,omitempty"`
	StorageConfigInfo    StorageConfig     `json:"storageConfigInfo,omitempty"`
}

func (c *Client) GetCatalog(ctx context.Context, name string) (Catalog, error) {
	var result Catalog
	err := c.doJSON(ctx, http.MethodGet, catalogResourceURL(c.managementURL, name), nil, &result)
	return result, err
}

func (c *Client) CreateCatalog(ctx context.Context, catalog Catalog) (Catalog, error) {
	var result Catalog
	err := c.doJSON(ctx, http.MethodPost, joinURLPath(c.managementURL, "catalogs"), createCatalogRequest{Catalog: catalog}, &result)
	return result, err
}

func (c *Client) UpdateCatalog(ctx context.Context, catalog Catalog) (Catalog, error) {
	var result Catalog
	request := updateCatalogRequest{
		CurrentEntityVersion: catalog.EntityVersion,
		Properties:           catalog.Properties,
		StorageConfigInfo:    catalog.StorageConfigInfo,
	}
	err := c.doJSON(ctx, http.MethodPut, catalogResourceURL(c.managementURL, catalog.Name), request, &result)
	return result, err
}

func (c *Client) DeleteCatalog(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodDelete, catalogResourceURL(c.managementURL, name), nil, nil)
}

func catalogResourceURL(base *url.URL, name string) *url.URL {
	return objectResourceURL(base, "catalogs", name, "")
}

func objectResourceURL(base *url.URL, collection, name, action string) *url.URL {
	endpoint := *base
	suffix := "/" + collection + "/" + name
	escapedSuffix := "/" + collection + "/" + url.PathEscape(name)
	if action != "" {
		suffix += "/" + action
		escapedSuffix += "/" + action
	}
	endpoint.Path = strings.TrimRight(base.Path, "/") + suffix
	endpoint.RawPath = strings.TrimRight(base.EscapedPath(), "/") + escapedSuffix
	return &endpoint
}

func principalResourceURL(base *url.URL, name string) *url.URL {
	return objectResourceURL(base, "principals", name, "")
}

func principalRotateURL(base *url.URL, name string) *url.URL {
	return objectResourceURL(base, "principals", name, "rotate")
}
func (c *Client) doJSON(ctx context.Context, method string, endpoint *url.URL, input, output any) error {

	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	operation := endpoint.EscapedPath()
	var body io.Reader
	if input != nil {
		encoded, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			return fmt.Errorf("encode Polaris request: %w", marshalErr)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("create Polaris request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.realm != "" {
		req.Header.Set(c.realmHeader, c.realm)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request Polaris %s: %w", operation, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &HTTPError{Operation: operation, StatusCode: resp.StatusCode}
	}
	if output != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
			return fmt.Errorf("decode Polaris response: %w", err)
		}
	}
	return nil
}

func (c *Client) probe(ctx context.Context, endpoint *url.URL, token, operation string) (int, error) {
	return c.probeJSON(ctx, endpoint, token, operation, nil)
}

func (c *Client) probeJSON(ctx context.Context, endpoint *url.URL, token, operation string, output any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("create %s request: %w", operation, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if c.realm != "" {
		req.Header.Set(c.realmHeader, c.realm)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request %s: %w", operation, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return resp.StatusCode, &HTTPError{Operation: operation, StatusCode: resp.StatusCode}
	}
	if output != nil {
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil && !errors.Is(err, io.EOF) {
			return resp.StatusCode, fmt.Errorf("decode %s response: %w", operation, err)
		}
	}
	return resp.StatusCode, nil
}

func joinURLPath(base *url.URL, suffix string) *url.URL {
	joined := *base
	joined.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(suffix, "/")
	return &joined
}

func parseBaseURL(raw, name string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("%s must be an absolute URL", name)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("%s must use http or https", name)
	}
	return parsed, nil
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
}

type catalogConfig struct {
	Endpoints []string          `json:"endpoints,omitempty"`
	Defaults  map[string]string `json:"defaults,omitempty"`
	Overrides map[string]string `json:"overrides,omitempty"`
}

func configuredNamespaceSeparator(config catalogConfig) (string, error) {
	for _, properties := range []map[string]string{config.Overrides, config.Defaults} {
		if encoded, ok := properties["namespace-separator"]; ok && encoded != "" {
			separator, err := url.PathUnescape(encoded)
			if err != nil {
				return "", fmt.Errorf("decode namespace separator from Polaris /v1/config: %w", err)
			}
			if separator == "" {
				return "", errors.New("polaris /v1/config returned an empty namespace separator")
			}
			return separator, nil
		}
	}
	return DefaultNamespaceSeparator, nil
}

// Namespace is a Polaris namespace and its user-configured properties.
type Namespace struct {
	Namespace  []string          `json:"namespace"`
	Properties map[string]string `json:"properties,omitempty"`
}

type createNamespaceRequest struct {
	Namespace  []string          `json:"namespace"`
	Properties map[string]string `json:"properties,omitempty"`
}

type updateNamespacePropertiesRequest struct {
	Removals []string          `json:"removals,omitempty"`
	Updates  map[string]string `json:"updates,omitempty"`
}

// NamespacePropertiesResult describes the property keys changed by Polaris.
type NamespacePropertiesResult struct {
	Updated []string `json:"updated"`
	Removed []string `json:"removed"`
	Missing []string `json:"missing,omitempty"`
}

func namespaceResourceURL(base *url.URL, catalogName, namespace, action string) *url.URL {
	segments := []string{"v1", catalogName, "namespaces", namespace}
	if action != "" {
		segments = append(segments, action)
	}
	return resourceSegmentsURL(base, segments...)
}

// GetNamespace loads the namespace and its properties.
func (c *Client) GetNamespace(ctx context.Context, catalogName string, namespace []string) (Namespace, error) {
	if c.catalogURL == nil {
		return Namespace{}, errors.New("catalog URL is not configured")
	}
	var result Namespace
	path := strings.Join(namespace, c.namespaceSeparator)
	err := c.doJSON(ctx, http.MethodGet, namespaceResourceURL(c.catalogURL, catalogName, path, ""), nil, &result)
	return result, err
}

// CreateNamespace creates a namespace with the requested initial properties.
func (c *Client) CreateNamespace(ctx context.Context, catalogName string, namespace Namespace) (Namespace, error) {
	if c.catalogURL == nil {
		return Namespace{}, errors.New("catalog URL is not configured")
	}
	var result Namespace
	err := c.doJSON(ctx, http.MethodPost,
		resourceSegmentsURL(c.catalogURL, "v1", catalogName, "namespaces"),
		createNamespaceRequest(namespace), &result)
	return result, err
}

// UpdateNamespaceProperties updates or removes only the supplied property keys.
func (c *Client) UpdateNamespaceProperties(ctx context.Context, catalogName string, namespace []string, updates map[string]string, removals []string) (NamespacePropertiesResult, error) {
	if c.catalogURL == nil {
		return NamespacePropertiesResult{}, errors.New("catalog URL is not configured")
	}
	var result NamespacePropertiesResult
	path := strings.Join(namespace, c.namespaceSeparator)
	request := updateNamespacePropertiesRequest{Updates: updates, Removals: removals}
	err := c.doJSON(ctx, http.MethodPost,
		namespaceResourceURL(c.catalogURL, catalogName, path, "properties"), request, &result)
	return result, err
}

// DeleteNamespace drops a namespace; Polaris rejects non-empty namespaces.
func (c *Client) DeleteNamespace(ctx context.Context, catalogName string, namespace []string) error {
	if c.catalogURL == nil {
		return errors.New("catalog URL is not configured")
	}
	path := strings.Join(namespace, c.namespaceSeparator)
	return c.doJSON(ctx, http.MethodDelete,
		namespaceResourceURL(c.catalogURL, catalogName, path, ""), nil, nil)
}

// Principal is a Polaris management-service principal.
type Principal struct {
	Name          string            `json:"name"`
	ClientID      string            `json:"clientId,omitempty"`
	Properties    map[string]string `json:"properties,omitempty"`
	EntityVersion int64             `json:"entityVersion"`
}

// PrincipalCredentials contains the one-time credentials returned by Polaris.
type PrincipalCredentials struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// PrincipalWithCredentials is returned only after principal creation or credential rotation.
type PrincipalWithCredentials struct {
	Principal   Principal            `json:"principal"`
	Credentials PrincipalCredentials `json:"credentials"`
}

type createPrincipalRequest struct {
	Principal                  Principal `json:"principal"`
	CredentialRotationRequired bool      `json:"credentialRotationRequired"`
}

type updatePrincipalRequest struct {
	CurrentEntityVersion int64             `json:"currentEntityVersion"`
	Properties           map[string]string `json:"properties"`
}

// GetPrincipal fetches a principal by its Polaris name.
func (c *Client) GetPrincipal(ctx context.Context, name string) (Principal, error) {
	var result Principal
	err := c.doJSON(ctx, http.MethodGet, principalResourceURL(c.managementURL, name), nil, &result)
	return result, err
}

// CreatePrincipal creates a principal and returns its one-time credentials.
func (c *Client) CreatePrincipal(ctx context.Context, principal Principal) (PrincipalWithCredentials, error) {
	var result PrincipalWithCredentials
	request := createPrincipalRequest{Principal: principal, CredentialRotationRequired: false}
	err := c.doJSON(ctx, http.MethodPost, joinURLPath(c.managementURL, "principals"), request, &result)
	return result, err
}

// UpdatePrincipal updates metadata using Polaris optimistic concurrency.
func (c *Client) UpdatePrincipal(ctx context.Context, principal Principal) (Principal, error) {
	var result Principal
	request := updatePrincipalRequest{
		CurrentEntityVersion: principal.EntityVersion,
		Properties:           principal.Properties,
	}
	err := c.doJSON(ctx, http.MethodPut, principalResourceURL(c.managementURL, principal.Name), request, &result)
	return result, err
}

// DeletePrincipal deletes a principal by its Polaris name.
func (c *Client) DeletePrincipal(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodDelete, principalResourceURL(c.managementURL, name), nil, nil)
}

// RotatePrincipal explicitly rotates principal credentials.
func (c *Client) RotatePrincipal(ctx context.Context, name string) (PrincipalWithCredentials, error) {
	var result PrincipalWithCredentials
	err := c.doJSON(ctx, http.MethodPost, principalRotateURL(c.managementURL, name), nil, &result)
	return result, err
}

// PrincipalRole is a Polaris principal role.
type PrincipalRole struct {
	Name          string            `json:"name"`
	Federated     bool              `json:"federated"`
	Properties    map[string]string `json:"properties,omitempty"`
	EntityVersion int64             `json:"entityVersion"`
}

type createPrincipalRoleRequest struct {
	PrincipalRole PrincipalRole `json:"principalRole"`
}

type updatePrincipalRoleRequest struct {
	CurrentEntityVersion int64             `json:"currentEntityVersion"`
	Properties           map[string]string `json:"properties"`
}

// GetPrincipalRole fetches a principal role by its Polaris name.
func (c *Client) GetPrincipalRole(ctx context.Context, name string) (PrincipalRole, error) {
	var result PrincipalRole
	err := c.doJSON(ctx, http.MethodGet, principalRoleResourceURL(c.managementURL, name), nil, &result)
	return result, err
}

// CreatePrincipalRole creates a Polaris principal role.
func (c *Client) CreatePrincipalRole(ctx context.Context, role PrincipalRole) (PrincipalRole, error) {
	var result PrincipalRole
	err := c.doJSON(ctx, http.MethodPost, joinURLPath(c.managementURL, "principal-roles"),
		createPrincipalRoleRequest{PrincipalRole: role}, &result)
	return result, err
}

// UpdatePrincipalRole updates a role's properties using optimistic concurrency.
func (c *Client) UpdatePrincipalRole(ctx context.Context, role PrincipalRole) (PrincipalRole, error) {
	var result PrincipalRole
	properties := role.Properties
	if properties == nil {
		properties = map[string]string{}
	}
	request := updatePrincipalRoleRequest{CurrentEntityVersion: role.EntityVersion, Properties: properties}
	err := c.doJSON(ctx, http.MethodPut, principalRoleResourceURL(c.managementURL, role.Name), request, &result)
	return result, err
}

// DeletePrincipalRole deletes a role by its Polaris name.
func (c *Client) DeletePrincipalRole(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodDelete, principalRoleResourceURL(c.managementURL, name), nil, nil)
}

func principalRoleResourceURL(base *url.URL, name string) *url.URL {
	return objectResourceURL(base, "principal-roles", name, "")
}

// CatalogRole is a role scoped to one Polaris catalog.
type CatalogRole struct {
	Name          string            `json:"name"`
	Properties    map[string]string `json:"properties,omitempty"`
	EntityVersion int64             `json:"entityVersion"`
}

type createCatalogRoleRequest struct {
	CatalogRole CatalogRole `json:"catalogRole"`
}

type updateCatalogRoleRequest struct {
	CurrentEntityVersion int64             `json:"currentEntityVersion"`
	Properties           map[string]string `json:"properties"`
}

// GetCatalogRole fetches a role by catalog and role name.
func (c *Client) GetCatalogRole(ctx context.Context, catalogName, roleName string) (CatalogRole, error) {
	var result CatalogRole
	err := c.doJSON(ctx, http.MethodGet, catalogRoleResourceURL(c.managementURL, catalogName, roleName), nil, &result)
	return result, err
}

// CreateCatalogRole creates a role in the named catalog.
func (c *Client) CreateCatalogRole(ctx context.Context, catalogName string, role CatalogRole) (CatalogRole, error) {
	var result CatalogRole
	endpoint := resourceSegmentsURL(c.managementURL, "catalogs", catalogName, "catalog-roles")
	err := c.doJSON(ctx, http.MethodPost, endpoint, createCatalogRoleRequest{CatalogRole: role}, &result)
	return result, err
}

// UpdateCatalogRole updates role properties using optimistic concurrency.
func (c *Client) UpdateCatalogRole(ctx context.Context, catalogName string, role CatalogRole) (CatalogRole, error) {
	var result CatalogRole
	properties := role.Properties
	if properties == nil {
		properties = map[string]string{}
	}
	request := updateCatalogRoleRequest{CurrentEntityVersion: role.EntityVersion, Properties: properties}
	err := c.doJSON(ctx, http.MethodPut, catalogRoleResourceURL(c.managementURL, catalogName, role.Name), request, &result)
	return result, err
}

// DeleteCatalogRole deletes a role; Polaris deletes grants associated with the role.
func (c *Client) DeleteCatalogRole(ctx context.Context, catalogName, roleName string) error {
	return c.doJSON(ctx, http.MethodDelete, catalogRoleResourceURL(c.managementURL, catalogName, roleName), nil, nil)
}

func catalogRoleResourceURL(base *url.URL, catalogName, roleName string) *url.URL {
	return resourceSegmentsURL(base, "catalogs", catalogName, "catalog-roles", roleName)
}

func resourceSegmentsURL(base *url.URL, segments ...string) *url.URL {
	endpoint := *base
	plain := make([]string, len(segments))
	escaped := make([]string, len(segments))
	for i, segment := range segments {
		plain[i] = segment
		escaped[i] = url.PathEscape(segment)
	}
	endpoint.Path = strings.TrimRight(base.Path, "/") + "/" + strings.Join(plain, "/")
	endpoint.RawPath = strings.TrimRight(base.EscapedPath(), "/") + "/" + strings.Join(escaped, "/")
	return &endpoint
}

type principalRolesResponse struct {
	Roles []PrincipalRole `json:"roles"`
}

// ListPrincipalRolesForPrincipal lists role assignments for one principal.
func (c *Client) ListPrincipalRolesForPrincipal(ctx context.Context, principalName string) ([]PrincipalRole, error) {
	var result principalRolesResponse
	endpoint := resourceSegmentsURL(c.managementURL, "principals", principalName, "principal-roles")
	err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &result)
	return result.Roles, err
}

// AssignPrincipalRole adds the named role to a principal.
func (c *Client) AssignPrincipalRole(ctx context.Context, principalName, roleName string) error {
	endpoint := resourceSegmentsURL(c.managementURL, "principals", principalName, "principal-roles")
	request := struct {
		PrincipalRole PrincipalRole `json:"principalRole"`
	}{PrincipalRole: PrincipalRole{Name: roleName}}
	return c.doJSON(ctx, http.MethodPut, endpoint, request, nil)
}

// RevokePrincipalRole removes the named role from a principal.
func (c *Client) RevokePrincipalRole(ctx context.Context, principalName, roleName string) error {
	endpoint := resourceSegmentsURL(c.managementURL, "principals", principalName, "principal-roles", roleName)
	return c.doJSON(ctx, http.MethodDelete, endpoint, nil, nil)
}

type catalogRolesResponse struct {
	Roles []CatalogRole `json:"roles"`
}

// ListCatalogRolesForPrincipalRole lists catalog roles assigned in a catalog.
func (c *Client) ListCatalogRolesForPrincipalRole(ctx context.Context, principalRoleName, catalogName string) ([]CatalogRole, error) {
	var result catalogRolesResponse
	endpoint := resourceSegmentsURL(c.managementURL, "principal-roles", principalRoleName, "catalog-roles", catalogName)
	err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &result)
	return result.Roles, err
}

// AssignCatalogRoleToPrincipalRole assigns a catalog role to a principal role.
func (c *Client) AssignCatalogRoleToPrincipalRole(ctx context.Context, principalRoleName, catalogName, catalogRoleName string) error {
	endpoint := resourceSegmentsURL(c.managementURL, "principal-roles", principalRoleName, "catalog-roles", catalogName)
	request := struct {
		CatalogRole CatalogRole `json:"catalogRole"`
	}{CatalogRole: CatalogRole{Name: catalogRoleName}}
	return c.doJSON(ctx, http.MethodPut, endpoint, request, nil)
}

// RevokeCatalogRoleFromPrincipalRole removes a catalog role assignment.
func (c *Client) RevokeCatalogRoleFromPrincipalRole(ctx context.Context, principalRoleName, catalogName, catalogRoleName string) error {
	endpoint := resourceSegmentsURL(c.managementURL, "principal-roles", principalRoleName, "catalog-roles", catalogName, catalogRoleName)
	return c.doJSON(ctx, http.MethodDelete, endpoint, nil, nil)
}

// Grant represents one Polaris privilege on a securable resource.
type Grant struct {
	Type              string   `json:"type"`
	Namespace         []string `json:"namespace,omitempty"`
	TableName         string   `json:"tableName,omitempty"`
	ViewName          string   `json:"viewName,omitempty"`
	PolicyName        string   `json:"policyName,omitempty"`
	SemanticModelName string   `json:"semanticModelName,omitempty"`
	Privilege         string   `json:"privilege"`
}

type grantsResponse struct {
	Grants []Grant `json:"grants"`
}

// ListCatalogRoleGrants lists a role's grants within the named catalog.
func (c *Client) ListCatalogRoleGrants(ctx context.Context, catalogName, roleName string) ([]Grant, error) {
	var result grantsResponse
	endpoint := resourceSegmentsURL(c.managementURL, "catalogs", catalogName, "catalog-roles", roleName, "grants")
	err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &result)
	return result.Grants, err
}

// AddCatalogRoleGrant adds one privilege grant.
func (c *Client) AddCatalogRoleGrant(ctx context.Context, catalogName, roleName string, grant Grant) error {
	endpoint := resourceSegmentsURL(c.managementURL, "catalogs", catalogName, "catalog-roles", roleName, "grants")
	request := struct {
		Grant Grant `json:"grant"`
	}{Grant: grant}
	return c.doJSON(ctx, http.MethodPut, endpoint, request, nil)
}

// RevokeCatalogRoleGrant revokes a specific grant, optionally cascading to subresources.
func (c *Client) RevokeCatalogRoleGrant(ctx context.Context, catalogName, roleName string, grant Grant, cascade bool) error {
	endpoint := resourceSegmentsURL(c.managementURL, "catalogs", catalogName, "catalog-roles", roleName, "grants")
	query := url.Values{}
	query.Set("cascade", strconv.FormatBool(cascade))
	endpoint.RawQuery = query.Encode()
	request := struct {
		Grant Grant `json:"grant"`
	}{Grant: grant}
	return c.doJSON(ctx, http.MethodPost, endpoint, request, nil)
}

// Policy is a policy stored in a Polaris catalog namespace.
type Policy struct {
	Type        string `json:"policy-type"`
	Inheritable bool   `json:"inheritable"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Version     int64  `json:"version"`
}

type policyResponse struct {
	Policy Policy `json:"policy"`
}

type createPolicyRequest struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Content     string `json:"content,omitempty"`
}

type updatePolicyRequest struct {
	Description          string `json:"description"`
	Content              string `json:"content"`
	CurrentPolicyVersion int64  `json:"current-policy-version"`
}

// PolicyTarget identifies a catalog, namespace, or table-like attachment target.
type PolicyTarget struct {
	Type string   `json:"type"`
	Path []string `json:"path,omitempty"`
}

type attachPolicyRequest struct {
	Target     PolicyTarget      `json:"target"`
	Parameters map[string]string `json:"parameters"`
}

type detachPolicyRequest struct {
	Target PolicyTarget `json:"target"`
}

func policyCollectionURL(base *url.URL, namespaceSeparator, catalogName string, namespace []string) *url.URL {
	namespacePath := strings.Join(namespace, namespaceSeparator)
	return resourceSegmentsURL(base, "polaris", "v1", catalogName, "namespaces", namespacePath, "policies")
}

func policyResourceURL(base *url.URL, namespaceSeparator, catalogName string, namespace []string, policyName string) *url.URL {
	return resourceSegmentsURL(base, "polaris", "v1", catalogName, "namespaces",
		strings.Join(namespace, namespaceSeparator), "policies", policyName)
}

func policyMappingsURL(base *url.URL, namespaceSeparator, catalogName string, namespace []string, policyName string) *url.URL {
	endpoint := policyResourceURL(base, namespaceSeparator, catalogName, namespace, policyName)
	endpoint.Path += "/mappings"
	endpoint.RawPath += "/mappings"
	return endpoint
}

// GetPolicy fetches a policy by catalog, namespace path, and policy name.
func (c *Client) GetPolicy(ctx context.Context, catalogName string, namespace []string, policyName string) (Policy, error) {
	if c.catalogURL == nil {
		return Policy{}, errors.New("catalog URL is not configured")
	}
	var result policyResponse
	err := c.doJSON(ctx, http.MethodGet, policyResourceURL(c.catalogURL, c.namespaceSeparator, catalogName, namespace, policyName), nil, &result)
	return result.Policy, err
}

// CreatePolicy creates a policy in a catalog namespace.
func (c *Client) CreatePolicy(ctx context.Context, catalogName string, namespace []string, policy Policy) (Policy, error) {
	if c.catalogURL == nil {
		return Policy{}, errors.New("catalog URL is not configured")
	}
	var result policyResponse
	endpoint := policyCollectionURL(c.catalogURL, c.namespaceSeparator, catalogName, namespace)
	request := createPolicyRequest{
		Name: policy.Name, Type: policy.Type, Description: policy.Description, Content: policy.Content,
	}
	err := c.doJSON(ctx, http.MethodPost, endpoint, request, &result)
	return result.Policy, err
}

// UpdatePolicy updates policy content using Polaris optimistic concurrency.
func (c *Client) UpdatePolicy(ctx context.Context, catalogName string, namespace []string, policy Policy) (Policy, error) {
	if c.catalogURL == nil {
		return Policy{}, errors.New("catalog URL is not configured")
	}
	var result policyResponse
	request := updatePolicyRequest{
		Description: policy.Description, Content: policy.Content, CurrentPolicyVersion: policy.Version,
	}
	err := c.doJSON(ctx, http.MethodPut, policyResourceURL(c.catalogURL, c.namespaceSeparator, catalogName, namespace, policy.Name), request, &result)
	return result.Policy, err
}

// DeletePolicy deletes a policy; detachAll explicitly requests removal of every remote mapping.
func (c *Client) DeletePolicy(ctx context.Context, catalogName string, namespace []string, policyName string, detachAll bool) error {
	if c.catalogURL == nil {
		return errors.New("catalog URL is not configured")
	}
	endpoint := policyResourceURL(c.catalogURL, c.namespaceSeparator, catalogName, namespace, policyName)
	if detachAll {
		query := url.Values{}
		query.Set("detach-all", "true")
		endpoint.RawQuery = query.Encode()
	}
	return c.doJSON(ctx, http.MethodDelete, endpoint, nil, nil)
}

// AttachPolicy creates or replaces one policy mapping.
func (c *Client) AttachPolicy(ctx context.Context, catalogName string, namespace []string, policyName string, target PolicyTarget, parameters map[string]string) error {
	if c.catalogURL == nil {
		return errors.New("catalog URL is not configured")
	}
	if parameters == nil {
		parameters = map[string]string{}
	}
	request := attachPolicyRequest{Target: target, Parameters: parameters}
	return c.doJSON(ctx, http.MethodPut, policyMappingsURL(c.catalogURL, c.namespaceSeparator, catalogName, namespace, policyName), request, nil)
}

// DetachPolicy removes one policy mapping.
func (c *Client) DetachPolicy(ctx context.Context, catalogName string, namespace []string, policyName string, target PolicyTarget) error {
	if c.catalogURL == nil {
		return errors.New("catalog URL is not configured")
	}
	request := detachPolicyRequest{Target: target}
	return c.doJSON(ctx, http.MethodPost, policyMappingsURL(c.catalogURL, c.namespaceSeparator, catalogName, namespace, policyName), request, nil)
}
