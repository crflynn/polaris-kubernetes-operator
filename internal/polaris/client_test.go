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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const testRealmHeader = "X-Polaris-Realm"

const (
	testPolarisRealm        = "POLARIS"
	testToken               = "test-token"
	testPolicyType          = "system.retention"
	testBearerAuthorization = "Bearer " + testToken
	testSchemaName          = "schema"
	testOwnerProperty       = "owner"
	testEventName           = "events"
)

func TestClientCheckUsesTokenAndRealm(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			if got := r.Header.Get(testRealmHeader); got != testPolarisRealm {
				t.Errorf("%s = %q on token request", testRealmHeader, got)
			}
			if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
				t.Errorf("token content type = %q", got)
			}
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
				return
			}
			if got := r.Form.Get("grant_type"); got != clientCredentialsGrant {
				t.Errorf("grant_type = %q", got)
			}
			if got := r.Form.Get("scope"); got != "catalog admin" {
				t.Errorf("scope = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token-value"}`))
		case "/api/management/v1/catalogs":
			assertAuthAndRealm(t, r)
			w.WriteHeader(http.StatusOK)
		case "/api/catalog/v1/config":
			assertAuthAndRealm(t, r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"defaults":{},"overrides":{"namespace-separator":"%2E"},"endpoints":["GET /v1/{prefix}/namespaces"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{
		ManagementURL: server.URL + "/api/management/v1",
		CatalogURL:    server.URL + "/api/catalog",
		Realm:         testPolarisRealm,
		RealmHeader:   testRealmHeader,
		Auth: AuthConfig{
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			TokenURL:     server.URL + "/oauth/token",
			Scopes:       []string{"catalog", "admin"},
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	result, err := client.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if result.ManagementStatusCode != http.StatusOK || result.CatalogStatusCode != http.StatusOK {
		t.Fatalf("unexpected status codes: %+v", result)
	}
	if len(result.Capabilities) != 1 || result.Capabilities[0] != "GET /v1/{prefix}/namespaces" {
		t.Fatalf("unexpected capabilities: %#v", result.Capabilities)
	}
	if result.NamespaceSeparator != "." {
		t.Fatalf("namespace separator = %q, want %q", result.NamespaceSeparator, ".")
	}
}

func TestClientCheckUsesBearerTokenWithoutTokenRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/management/catalogs" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer direct-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get(DefaultRealmHeader); got != testPolarisRealm {
			t.Errorf("%s = %q, want POLARIS", DefaultRealmHeader, got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		ManagementURL: server.URL + "/management",
		Realm:         testPolarisRealm,
		Auth:          AuthConfig{BearerToken: "direct-token"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Check(context.Background()); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
}

func TestNewHTTPClientRejectsInvalidCABundle(t *testing.T) {
	t.Parallel()

	if _, err := NewHTTPClient(0, []byte("not-a-certificate")); err == nil {
		t.Fatal("NewHTTPClient() accepted an invalid CA bundle")
	}
}

func TestParseBaseURLRejectsUnsupportedScheme(t *testing.T) {
	t.Parallel()

	if _, err := parseBaseURL("ftp://polaris.example.com", "management URL"); err == nil {
		t.Fatal("parseBaseURL() accepted ftp")
	}
}

func TestNamespaceClientUsesConfiguredSeparatorAndLifecycleRoutes(t *testing.T) {
	t.Parallel()

	const collectionPath = "/api/catalog/v1/warehouse/namespaces"
	const namespacePath = collectionPath + "/db.schema"
	var sawCreate, sawGet, sawUpdate, sawDelete bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != collectionPath && got != namespacePath && got != namespacePath+"/properties" {
			t.Errorf("request path = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAuthorization {
			t.Errorf("Authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.EscapedPath() == collectionPath:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create request: %v", err)
			}
			if !reflect.DeepEqual(body["namespace"], []any{"db", testSchemaName}) ||
				!reflect.DeepEqual(body["properties"], map[string]any{testOwnerProperty: testTeam}) {
				t.Errorf("create body = %#v", body)
			}
			sawCreate = true
			_, _ = w.Write([]byte(`{"namespace":["db","schema"],"properties":{"owner":"team"}}`))
		case r.Method == http.MethodGet && r.URL.EscapedPath() == namespacePath:
			sawGet = true
			_, _ = w.Write([]byte(`{"namespace":["db","schema"],"properties":{"owner":"team"}}`))
		case r.Method == http.MethodPost && r.URL.EscapedPath() == namespacePath+"/properties":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode property request: %v", err)
			}
			if !reflect.DeepEqual(body["updates"], map[string]any{testOwnerProperty: "platform"}) ||
				!reflect.DeepEqual(body["removals"], []any{"retired"}) {
				t.Errorf("property body = %#v", body)
			}
			sawUpdate = true
			_, _ = w.Write([]byte(`{"updated":["owner"],"removed":["retired"],"missing":[]}`))
		case r.Method == http.MethodDelete && r.URL.EscapedPath() == namespacePath:
			sawDelete = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{
		ManagementURL:      server.URL + "/management",
		CatalogURL:         server.URL + "/api/catalog",
		NamespaceSeparator: ".",
		Auth:               AuthConfig{BearerToken: testToken},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	namespace := Namespace{Namespace: []string{"db", testSchemaName}, Properties: map[string]string{testOwnerProperty: "team"}}
	if _, err := client.CreateNamespace(context.Background(), "warehouse", namespace); err != nil {
		t.Fatalf("CreateNamespace() error = %v", err)
	}
	remote, err := client.GetNamespace(context.Background(), "warehouse", namespace.Namespace)
	if err != nil {
		t.Fatalf("GetNamespace() error = %v", err)
	}
	if !reflect.DeepEqual(remote.Namespace, namespace.Namespace) || remote.Properties[testOwnerProperty] != "team" {
		t.Fatalf("GetNamespace() = %#v", remote)
	}
	if _, err := client.UpdateNamespaceProperties(context.Background(), "warehouse", namespace.Namespace,
		map[string]string{testOwnerProperty: "platform"}, []string{"retired"}); err != nil {
		t.Fatalf("UpdateNamespaceProperties() error = %v", err)
	}
	if err := client.DeleteNamespace(context.Background(), "warehouse", namespace.Namespace); err != nil {
		t.Fatalf("DeleteNamespace() error = %v", err)
	}
	if !sawCreate || !sawGet || !sawUpdate || !sawDelete {
		t.Fatalf("namespace operations: create=%t get=%t update=%t delete=%t", sawCreate, sawGet, sawUpdate, sawDelete)
	}
}

func assertAuthAndRealm(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer token-value" {
		t.Errorf("Authorization = %q", got)
	}
	if got := r.Header.Get(testRealmHeader); got != testPolarisRealm {
		t.Errorf("%s = %q", testRealmHeader, got)
	}
	if strings.Contains(r.URL.RawQuery, "secret") {
		t.Errorf("request unexpectedly contains secret query data: %q", r.URL.RawQuery)
	}
}

func TestCatalogResourceURLEscapesNameAsSingleSegment(t *testing.T) {
	base, err := parseBaseURL("https://polaris.example.com/api/management/v1", "management URL")
	if err != nil {
		t.Fatal(err)
	}
	got := catalogResourceURL(base, "warehouse central/primary").String()
	want := "https://polaris.example.com/api/management/v1/catalogs/warehouse%20central%2Fprimary"
	if got != want {
		t.Fatalf("catalog URL = %q, want %q", got, want)
	}
}

type policyTestOperations struct {
	sawCreate  bool
	sawVersion bool
	sawAttach  bool
	sawDetach  bool
	sawDelete  bool
}

func newPolicyTestServer(t *testing.T, operations *policyTestOperations) *httptest.Server {
	t.Helper()
	const collectionPath = "/api/catalog/polaris/v1/warehouse/namespaces/db.schema/policies"
	const policyPath = collectionPath + "/retention"

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != collectionPath && got != policyPath && got != policyPath+"/mappings" {
			t.Errorf("request path = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAuthorization {
			t.Errorf("Authorization = %q", got)
		}
		if handlePolicyLifecycleTestRequest(t, w, r, operations, collectionPath, policyPath) ||
			handlePolicyMappingTestRequest(t, w, r, operations, policyPath) {
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		http.Error(w, "unexpected", http.StatusBadRequest)
	}))
}

func handlePolicyLifecycleTestRequest(t *testing.T, w http.ResponseWriter, r *http.Request, operations *policyTestOperations, collectionPath, policyPath string) bool {
	switch {
	case r.Method == http.MethodPost && r.URL.EscapedPath() == collectionPath:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode create: %v", err)
		}
		if body["name"] != "retention" || body["type"] != testPolicyType {
			t.Errorf("create body = %#v", body)
		}
		operations.sawCreate = true
		_, _ = w.Write([]byte(`{"policy":{"policy-type":"system.retention","inheritable":true,"name":"retention","description":"old","content":"{}","version":0}}`))
	case r.Method == http.MethodGet && r.URL.EscapedPath() == policyPath:
		_, _ = w.Write([]byte(`{"policy":{"policy-type":"system.retention","inheritable":true,"name":"retention","description":"old","content":"{}","version":7}}`))
	case r.Method == http.MethodPut && r.URL.EscapedPath() == policyPath:
		var updateBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&updateBody); err != nil {
			t.Errorf("decode update: %v", err)
		}
		if updateBody["current-policy-version"] != float64(7) || updateBody["content"] != `{"days":30}` || updateBody["description"] != "new" {
			t.Errorf("update body = %#v", updateBody)
		}
		operations.sawVersion = true
		_, _ = w.Write([]byte(`{"policy":{"policy-type":"system.retention","inheritable":true,"name":"retention","description":"new","content":"{\"days\":30}","version":8}}`))
	case r.Method == http.MethodDelete && r.URL.EscapedPath() == policyPath:
		if r.URL.Query().Get("detach-all") != "true" {
			t.Errorf("detach-all query = %q", r.URL.RawQuery)
		}
		operations.sawDelete = true
		w.WriteHeader(http.StatusNoContent)
	default:
		return false
	}
	return true
}

func handlePolicyMappingTestRequest(t *testing.T, w http.ResponseWriter, r *http.Request, operations *policyTestOperations, policyPath string) bool {
	switch {
	case r.Method == http.MethodPut && r.URL.EscapedPath() == policyPath+"/mappings":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode attachment: %v", err)
		}
		target, ok := body["target"].(map[string]any)
		if !ok || target["type"] != "table-like" {
			t.Errorf("attachment target = %#v", body["target"])
		}
		path, ok := target["path"].([]any)
		if !ok || !reflect.DeepEqual(path, []any{"db", testSchemaName, testEventName}) {
			t.Errorf("attachment path = %#v", target["path"])
		}
		if params, ok := body["parameters"].(map[string]any); !ok || len(params) != 0 {
			t.Errorf("attachment parameters = %#v", body["parameters"])
		}
		operations.sawAttach = true
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && r.URL.EscapedPath() == policyPath+"/mappings":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode detachment: %v", err)
		}
		operations.sawDetach = true
		w.WriteHeader(http.StatusNoContent)
	default:
		return false
	}
	return true
}

func TestPolicyClientUsesVersionedPolicyAndMappingAPIs(t *testing.T) {
	t.Parallel()
	var operations policyTestOperations
	server := newPolicyTestServer(t, &operations)
	defer server.Close()

	apiClient, err := NewClient(Config{
		ManagementURL:      server.URL + "/api/management/v1",
		CatalogURL:         server.URL + "/api/catalog",
		NamespaceSeparator: ".",
		Auth:               AuthConfig{BearerToken: testToken},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := apiClient.CreatePolicy(context.Background(), "warehouse", []string{"db", testSchemaName}, Policy{
		Name: "retention", Type: testPolicyType, Description: "old", Content: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 0 {
		t.Fatalf("created policy version = %d, want 0", created.Version)
	}
	policy, err := apiClient.GetPolicy(context.Background(), "warehouse", []string{"db", testSchemaName}, "retention")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Version != 7 || policy.Type != testPolicyType || !policy.Inheritable {
		t.Fatalf("policy response = %#v", policy)
	}
	policy.Description = "new"
	policy.Content = `{"days":30}`
	policy, err = apiClient.UpdatePolicy(context.Background(), "warehouse", []string{"db", testSchemaName}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Version != 8 {
		t.Fatalf("updated policy version = %d, want 8", policy.Version)
	}
	target := PolicyTarget{Type: "table-like", Path: []string{"db", testSchemaName, testEventName}}
	if err := apiClient.AttachPolicy(context.Background(), "warehouse", []string{"db", testSchemaName}, "retention", target, nil); err != nil {
		t.Fatal(err)
	}
	if err := apiClient.DetachPolicy(context.Background(), "warehouse", []string{"db", testSchemaName}, "retention", target); err != nil {
		t.Fatal(err)
	}
	if err := apiClient.DeletePolicy(context.Background(), "warehouse", []string{"db", testSchemaName}, "retention", true); err != nil {
		t.Fatal(err)
	}
	if !operations.sawCreate || !operations.sawVersion || !operations.sawAttach || !operations.sawDetach || !operations.sawDelete {
		t.Fatalf("observed create=%t version=%t attach=%t detach=%t delete=%t", operations.sawCreate, operations.sawVersion, operations.sawAttach, operations.sawDetach, operations.sawDelete)
	}
}

func TestPolicyClientRequiresCatalogURL(t *testing.T) {
	t.Parallel()

	apiClient, err := NewClient(Config{
		ManagementURL: "https://polaris.example.com/api/management/v1",
		Auth:          AuthConfig{BearerToken: testToken},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apiClient.GetPolicy(context.Background(), "warehouse", []string{"db"}, "retention"); err == nil {
		t.Fatal("GetPolicy succeeded without a catalog URL")
	}
}
