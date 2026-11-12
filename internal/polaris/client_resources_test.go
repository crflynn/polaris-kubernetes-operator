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
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func newResourceTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := NewClient(Config{
		ManagementURL: server.URL + "/api/management/v1",
		CatalogURL:    server.URL + "/api/catalog",
		Auth:          AuthConfig{BearerToken: "test-token"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func assertResourceRequest(t *testing.T, r *http.Request, method, path string) {
	t.Helper()
	if r.Method != method || r.URL.EscapedPath() != path {
		t.Errorf("request = %s %q, want %s %q", r.Method, r.URL.EscapedPath(), method, path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Errorf("Authorization = %q, want bearer token", got)
	}
}

func decodeResourceRequest(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decode request body: %v", err)
	}
	return body
}

func TestCatalogClientCRUDAndSafeHTTPError(t *testing.T) {
	const collectionPath = "/api/management/v1/catalogs"
	const catalogPath = collectionPath + "/warehouse"
	const response = `{"name":"warehouse","type":"INTERNAL","properties":{"owner":"analytics"},"storageConfigInfo":{"storageType":"S3","allowedLocations":["s3://warehouse"]},"entityVersion":7}`

	client := newResourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.EscapedPath() == collectionPath:
			assertResourceRequest(t, r, http.MethodPost, collectionPath)
			body := decodeResourceRequest(t, r)
			catalog, ok := body["catalog"].(map[string]any)
			if !ok || catalog["name"] != "warehouse" || catalog["type"] != "INTERNAL" {
				t.Errorf("create request catalog = %#v", body["catalog"])
			}
			storage, ok := catalog["storageConfigInfo"].(map[string]any)
			if !ok || storage["storageType"] != "S3" {
				t.Errorf("create request storage config = %#v", catalog["storageConfigInfo"])
			}
			_, _ = w.Write([]byte(response))
		case r.URL.EscapedPath() == catalogPath && r.Method == http.MethodGet:
			assertResourceRequest(t, r, http.MethodGet, catalogPath)
			_, _ = w.Write([]byte(response))
		case r.URL.EscapedPath() == catalogPath && r.Method == http.MethodPut:
			assertResourceRequest(t, r, http.MethodPut, catalogPath)
			body := decodeResourceRequest(t, r)
			if body["currentEntityVersion"] != float64(7) || body["properties"].(map[string]any)["owner"] != "analytics" {
				t.Errorf("update request = %#v", body)
			}
			_, _ = w.Write([]byte(response))
		case r.URL.EscapedPath() == catalogPath && r.Method == http.MethodDelete:
			assertResourceRequest(t, r, http.MethodDelete, catalogPath)
			w.WriteHeader(http.StatusNoContent)
		case r.URL.EscapedPath() == collectionPath+"/denied":
			assertResourceRequest(t, r, http.MethodGet, collectionPath+"/denied")
			http.Error(w, "sensitive Polaris error detail", http.StatusForbidden)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})

	ctx := context.Background()
	catalog, err := client.CreateCatalog(ctx, Catalog{
		Name: "warehouse", Type: "INTERNAL", Properties: map[string]string{"owner": "analytics"},
		StorageConfigInfo: StorageConfig{StorageType: "S3", AllowedLocations: []string{"s3://warehouse"}},
	})
	if err != nil || catalog.EntityVersion != 7 {
		t.Fatalf("CreateCatalog() = %#v, %v", catalog, err)
	}
	catalog, err = client.GetCatalog(ctx, "warehouse")
	if err != nil || catalog.Name != "warehouse" || catalog.EntityVersion != 7 {
		t.Fatalf("GetCatalog() = %#v, %v", catalog, err)
	}
	if _, err := client.UpdateCatalog(ctx, catalog); err != nil {
		t.Fatalf("UpdateCatalog() error = %v", err)
	}
	if err := client.DeleteCatalog(ctx, "warehouse"); err != nil {
		t.Fatalf("DeleteCatalog() error = %v", err)
	}

	_, err = client.GetCatalog(ctx, "denied")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusForbidden {
		t.Fatalf("GetCatalog(denied) error = %v, want HTTP 403", err)
	}
	if got := httpErr.Error(); got != "Polaris /api/management/v1/catalogs/denied returned HTTP 403" {
		t.Fatalf("HTTPError.Error() = %q", got)
	}
}

func TestPrincipalAndPrincipalRoleClientLifecycle(t *testing.T) {
	const principalPath = "/api/management/v1/principals/analyst"
	const principalRolesPath = "/api/management/v1/principal-roles"
	const rolePath = principalRolesPath + "/readers"

	client := newResourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case path == "/api/management/v1/principals" && r.Method == http.MethodPost:
			assertResourceRequest(t, r, http.MethodPost, path)
			body := decodeResourceRequest(t, r)
			principal, ok := body["principal"].(map[string]any)
			if !ok || principal["name"] != "analyst" || body["credentialRotationRequired"] != false {
				t.Errorf("create principal body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"principal":{"name":"analyst","entityVersion":1},"credentials":{"clientId":"id","clientSecret":"secret"}}`))
		case path == principalPath && r.Method == http.MethodGet:
			assertResourceRequest(t, r, http.MethodGet, principalPath)
			_, _ = w.Write([]byte(`{"name":"analyst","entityVersion":1}`))
		case path == principalPath && r.Method == http.MethodPut:
			assertResourceRequest(t, r, http.MethodPut, principalPath)
			body := decodeResourceRequest(t, r)
			if body["currentEntityVersion"] != float64(1) || body["properties"].(map[string]any)["team"] != "data" {
				t.Errorf("update principal body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"name":"analyst","entityVersion":2}`))
		case path == principalPath && r.Method == http.MethodDelete:
			assertResourceRequest(t, r, http.MethodDelete, principalPath)
			w.WriteHeader(http.StatusNoContent)
		case path == principalPath+"/rotate" && r.Method == http.MethodPost:
			assertResourceRequest(t, r, http.MethodPost, principalPath+"/rotate")
			_, _ = w.Write([]byte(`{"principal":{"name":"analyst","entityVersion":3},"credentials":{"clientId":"new-id","clientSecret":"new-secret"}}`))
		case path == principalRolesPath && r.Method == http.MethodPost:
			assertResourceRequest(t, r, http.MethodPost, principalRolesPath)
			body := decodeResourceRequest(t, r)
			role, ok := body["principalRole"].(map[string]any)
			if !ok || role["name"] != "readers" {
				t.Errorf("create principal role body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"name":"readers","federated":false,"entityVersion":1}`))
		case path == rolePath && r.Method == http.MethodGet:
			assertResourceRequest(t, r, http.MethodGet, rolePath)
			_, _ = w.Write([]byte(`{"name":"readers","federated":false,"entityVersion":1}`))
		case path == rolePath && r.Method == http.MethodPut:
			assertResourceRequest(t, r, http.MethodPut, rolePath)
			body := decodeResourceRequest(t, r)
			if body["currentEntityVersion"] != float64(1) || !reflect.DeepEqual(body["properties"], map[string]any{}) {
				t.Errorf("update principal role body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"name":"readers","federated":false,"entityVersion":2}`))
		case path == rolePath && r.Method == http.MethodDelete:
			assertResourceRequest(t, r, http.MethodDelete, rolePath)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})

	ctx := context.Background()
	created, err := client.CreatePrincipal(ctx, Principal{Name: "analyst"})
	if err != nil || created.Credentials.ClientSecret != "secret" {
		t.Fatalf("CreatePrincipal() = %#v, %v", created, err)
	}
	principal, err := client.GetPrincipal(ctx, "analyst")
	if err != nil || principal.EntityVersion != 1 {
		t.Fatalf("GetPrincipal() = %#v, %v", principal, err)
	}
	principal.Properties = map[string]string{"team": "data"}
	if _, err := client.UpdatePrincipal(ctx, principal); err != nil {
		t.Fatalf("UpdatePrincipal() error = %v", err)
	}
	rotated, err := client.RotatePrincipal(ctx, "analyst")
	if err != nil || rotated.Credentials.ClientID != "new-id" {
		t.Fatalf("RotatePrincipal() = %#v, %v", rotated, err)
	}
	if err := client.DeletePrincipal(ctx, "analyst"); err != nil {
		t.Fatalf("DeletePrincipal() error = %v", err)
	}

	role, err := client.CreatePrincipalRole(ctx, PrincipalRole{Name: "readers"})
	if err != nil || role.Name != "readers" {
		t.Fatalf("CreatePrincipalRole() = %#v, %v", role, err)
	}
	role, err = client.GetPrincipalRole(ctx, "readers")
	if err != nil || role.EntityVersion != 1 {
		t.Fatalf("GetPrincipalRole() = %#v, %v", role, err)
	}
	role.Properties = nil
	if _, err := client.UpdatePrincipalRole(ctx, role); err != nil {
		t.Fatalf("UpdatePrincipalRole() error = %v", err)
	}
	if err := client.DeletePrincipalRole(ctx, "readers"); err != nil {
		t.Fatalf("DeletePrincipalRole() error = %v", err)
	}
}

func TestCatalogRoleAssignmentAndGrantRequests(t *testing.T) {
	const roleCollection = "/api/management/v1/catalogs/warehouse/catalog-roles"
	const rolePath = roleCollection + "/writers"
	const principalRolePath = "/api/management/v1/principal-roles/analysts"
	const principalAssignmentPath = principalRolePath + "/catalog-roles/warehouse"
	const catalogRoleAssignmentPath = "/api/management/v1/principals/analyst/principal-roles"
	const grantPath = rolePath + "/grants"

	client := newResourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case path == roleCollection && r.Method == http.MethodPost:
			assertResourceRequest(t, r, http.MethodPost, roleCollection)
			body := decodeResourceRequest(t, r)
			role, ok := body["catalogRole"].(map[string]any)
			if !ok || role["name"] != "writers" {
				t.Errorf("create catalog role body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"name":"writers","entityVersion":1}`))
		case path == rolePath && r.Method == http.MethodGet:
			assertResourceRequest(t, r, http.MethodGet, rolePath)
			_, _ = w.Write([]byte(`{"name":"writers","entityVersion":1}`))
		case path == rolePath && r.Method == http.MethodPut:
			assertResourceRequest(t, r, http.MethodPut, rolePath)
			body := decodeResourceRequest(t, r)
			if body["currentEntityVersion"] != float64(1) || !reflect.DeepEqual(body["properties"], map[string]any{}) {
				t.Errorf("update catalog role body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"name":"writers","entityVersion":2}`))
		case path == rolePath && r.Method == http.MethodDelete:
			assertResourceRequest(t, r, http.MethodDelete, rolePath)
			w.WriteHeader(http.StatusNoContent)
		case path == catalogRoleAssignmentPath && r.Method == http.MethodGet:
			assertResourceRequest(t, r, http.MethodGet, catalogRoleAssignmentPath)
			_, _ = w.Write([]byte(`{"roles":[{"name":"analysts"}]}`))
		case path == catalogRoleAssignmentPath && r.Method == http.MethodPut:
			assertResourceRequest(t, r, http.MethodPut, catalogRoleAssignmentPath)
			body := decodeResourceRequest(t, r)
			if body["principalRole"].(map[string]any)["name"] != "analysts" {
				t.Errorf("principal assignment body = %#v", body)
			}
			w.WriteHeader(http.StatusNoContent)
		case path == catalogRoleAssignmentPath+"/analysts" && r.Method == http.MethodDelete:
			assertResourceRequest(t, r, http.MethodDelete, catalogRoleAssignmentPath+"/analysts")
			w.WriteHeader(http.StatusNoContent)
		case path == principalAssignmentPath && r.Method == http.MethodGet:
			assertResourceRequest(t, r, http.MethodGet, principalAssignmentPath)
			_, _ = w.Write([]byte(`{"roles":[{"name":"writers"}]}`))
		case path == principalAssignmentPath && r.Method == http.MethodPut:
			assertResourceRequest(t, r, http.MethodPut, principalAssignmentPath)
			body := decodeResourceRequest(t, r)
			if body["catalogRole"].(map[string]any)["name"] != "writers" {
				t.Errorf("catalog role assignment body = %#v", body)
			}
			w.WriteHeader(http.StatusNoContent)
		case path == principalAssignmentPath+"/writers" && r.Method == http.MethodDelete:
			assertResourceRequest(t, r, http.MethodDelete, principalAssignmentPath+"/writers")
			w.WriteHeader(http.StatusNoContent)
		case path == grantPath && r.Method == http.MethodGet:
			assertResourceRequest(t, r, http.MethodGet, grantPath)
			_, _ = w.Write([]byte(`{"grants":[{"type":"table","namespace":["db","schema"],"tableName":"events","privilege":"SELECT"}]}`))
		case path == grantPath && r.Method == http.MethodPut:
			assertResourceRequest(t, r, http.MethodPut, grantPath)
			body := decodeResourceRequest(t, r)
			grant, ok := body["grant"].(map[string]any)
			if !ok || grant["tableName"] != "events" || grant["privilege"] != "SELECT" {
				t.Errorf("add grant body = %#v", body)
			}
			w.WriteHeader(http.StatusNoContent)
		case path == grantPath && r.Method == http.MethodPost:
			assertResourceRequest(t, r, http.MethodPost, grantPath)
			if r.URL.Query().Get("cascade") != "true" {
				t.Errorf("cascade query = %q, want true", r.URL.RawQuery)
			}
			body := decodeResourceRequest(t, r)
			grant, ok := body["grant"].(map[string]any)
			if !ok || grant["privilege"] != "SELECT" {
				t.Errorf("revoke grant body = %#v", body)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})

	ctx := context.Background()
	role, err := client.CreateCatalogRole(ctx, "warehouse", CatalogRole{Name: "writers"})
	if err != nil || role.Name != "writers" {
		t.Fatalf("CreateCatalogRole() = %#v, %v", role, err)
	}
	role, err = client.GetCatalogRole(ctx, "warehouse", "writers")
	if err != nil || role.EntityVersion != 1 {
		t.Fatalf("GetCatalogRole() = %#v, %v", role, err)
	}
	role.Properties = nil
	if _, err := client.UpdateCatalogRole(ctx, "warehouse", role); err != nil {
		t.Fatalf("UpdateCatalogRole() error = %v", err)
	}
	if err := client.DeleteCatalogRole(ctx, "warehouse", "writers"); err != nil {
		t.Fatalf("DeleteCatalogRole() error = %v", err)
	}

	principalRoles, err := client.ListPrincipalRolesForPrincipal(ctx, "analyst")
	if err != nil || len(principalRoles) != 1 || principalRoles[0].Name != "analysts" {
		t.Fatalf("ListPrincipalRolesForPrincipal() = %#v, %v", principalRoles, err)
	}
	if err := client.AssignPrincipalRole(ctx, "analyst", "analysts"); err != nil {
		t.Fatalf("AssignPrincipalRole() error = %v", err)
	}
	if err := client.RevokePrincipalRole(ctx, "analyst", "analysts"); err != nil {
		t.Fatalf("RevokePrincipalRole() error = %v", err)
	}
	catalogRoles, err := client.ListCatalogRolesForPrincipalRole(ctx, "analysts", "warehouse")
	if err != nil || len(catalogRoles) != 1 || catalogRoles[0].Name != "writers" {
		t.Fatalf("ListCatalogRolesForPrincipalRole() = %#v, %v", catalogRoles, err)
	}
	if err := client.AssignCatalogRoleToPrincipalRole(ctx, "analysts", "warehouse", "writers"); err != nil {
		t.Fatalf("AssignCatalogRoleToPrincipalRole() error = %v", err)
	}
	if err := client.RevokeCatalogRoleFromPrincipalRole(ctx, "analysts", "warehouse", "writers"); err != nil {
		t.Fatalf("RevokeCatalogRoleFromPrincipalRole() error = %v", err)
	}
	grant := Grant{Type: "table", Namespace: []string{"db", "schema"}, TableName: "events", Privilege: "SELECT"}
	grants, err := client.ListCatalogRoleGrants(ctx, "warehouse", "writers")
	if err != nil || len(grants) != 1 || grants[0].TableName != "events" {
		t.Fatalf("ListCatalogRoleGrants() = %#v, %v", grants, err)
	}
	if err := client.AddCatalogRoleGrant(ctx, "warehouse", "writers", grant); err != nil {
		t.Fatalf("AddCatalogRoleGrant() error = %v", err)
	}
	if err := client.RevokeCatalogRoleGrant(ctx, "warehouse", "writers", grant, true); err != nil {
		t.Fatalf("RevokeCatalogRoleGrant() error = %v", err)
	}
}

func TestGenericTableClientLifecycleAndUnsupportedAPI(t *testing.T) {
	const collectionPath = "/api/catalog/polaris/v1/warehouse/namespaces/db%1Fschema/generic-tables"
	const tablePath = collectionPath + "/events"
	client := newResourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case path == collectionPath && r.Method == http.MethodPost:
			assertResourceRequest(t, r, http.MethodPost, collectionPath)
			body := decodeResourceRequest(t, r)
			if body["name"] == "unsupported" {
				w.WriteHeader(http.StatusNotImplemented)
				return
			}
			if body["name"] != "events" || body["format"] != "json" {
				t.Errorf("create generic table body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"table":{"name":"events","format":"json","base-location":"s3://warehouse/events"}}`))
		case path == tablePath && r.Method == http.MethodGet:
			assertResourceRequest(t, r, http.MethodGet, tablePath)
			_, _ = w.Write([]byte(`{"table":{"name":"events","format":"json","base-location":"s3://warehouse/events"}}`))
		case path == tablePath && r.Method == http.MethodDelete:
			assertResourceRequest(t, r, http.MethodDelete, tablePath)
			w.WriteHeader(http.StatusNoContent)
		case path == collectionPath+"/unsupported":
			w.WriteHeader(http.StatusNotImplemented)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})

	ctx := context.Background()
	generic := NewGenericTableClient(client)
	created, err := generic.CreateTable(ctx, "warehouse", []string{"db", "schema"}, GenericTable{Name: "events", Format: "json"})
	if err != nil || created.BaseLocation != "s3://warehouse/events" {
		t.Fatalf("CreateTable() = %#v, %v", created, err)
	}
	loaded, err := generic.GetTable(ctx, "warehouse", []string{"db", "schema"}, "events")
	if err != nil || loaded.Format != "json" {
		t.Fatalf("GetTable() = %#v, %v", loaded, err)
	}
	if err := generic.DeleteTable(ctx, "warehouse", []string{"db", "schema"}, "events"); err != nil {
		t.Fatalf("DeleteTable() error = %v", err)
	}

	_, err = generic.GetTable(ctx, "warehouse", []string{"db", "schema"}, "unsupported")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("GetTable(unsupported) error = %v, want ErrUnsupported", err)
	}
	if _, err := generic.CreateTable(ctx, "warehouse", []string{"db", "schema"}, GenericTable{Name: "unsupported", Format: "json"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("CreateTable(unsupported) error = %v, want ErrUnsupported", err)
	}
	if err := generic.DeleteTable(ctx, "warehouse", []string{"db", "schema"}, "unsupported"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("DeleteTable(unsupported) error = %v, want ErrUnsupported", err)
	}
	if err := NewGenericTableClient(nil).DeleteTable(ctx, "warehouse", nil, "events"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("DeleteTable() without client error = %v, want ErrUnsupported", err)
	}
	withoutCatalog := newResourceTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("request sent without configured catalog URL")
	})
	withoutCatalog.catalogURL = nil
	if _, err := NewGenericTableClient(withoutCatalog).GetTable(ctx, "warehouse", nil, "events"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("GetTable() without catalog URL error = %v, want ErrUnsupported", err)
	}
}

func TestIcebergTableAndViewRequestContracts(t *testing.T) {
	const tablePath = "/api/catalog/v1/warehouse/namespaces/db%1Fschema/tables/events"
	const registerTablePath = "/api/catalog/v1/warehouse/namespaces/db%1Fschema/register"
	const viewPath = "/api/catalog/v1/warehouse/namespaces/db%1Fschema/views/summary"
	const registerViewPath = "/api/catalog/v1/warehouse/namespaces/db%1Fschema/register-view"

	client := newResourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case path == tablePath && r.Method == http.MethodGet:
			assertResourceRequest(t, r, http.MethodGet, tablePath)
			_, _ = w.Write([]byte(`{"metadata-location":"s3://warehouse/events/metadata.json"}`))
		case path == registerTablePath && r.Method == http.MethodPost:
			assertResourceRequest(t, r, http.MethodPost, registerTablePath)
			body := decodeResourceRequest(t, r)
			if body["name"] != "events" || body["metadata-location"] != "s3://warehouse/events/metadata.json" {
				t.Errorf("register table body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"metadata-location":"s3://warehouse/events/metadata.json"}`))
		case path == tablePath+"/unregister" && r.Method == http.MethodPost:
			assertResourceRequest(t, r, http.MethodPost, tablePath+"/unregister")
			w.WriteHeader(http.StatusNoContent)
		case path == tablePath && r.Method == http.MethodDelete:
			assertResourceRequest(t, r, http.MethodDelete, tablePath)
			w.WriteHeader(http.StatusNoContent)
		case path == viewPath && r.Method == http.MethodGet:
			assertResourceRequest(t, r, http.MethodGet, viewPath)
			_, _ = w.Write([]byte(`{"metadata-location":"s3://warehouse/views/summary/metadata.json"}`))
		case path == registerViewPath && r.Method == http.MethodPost:
			assertResourceRequest(t, r, http.MethodPost, registerViewPath)
			body := decodeResourceRequest(t, r)
			if body["name"] != "summary" || body["metadata-location"] != "s3://warehouse/views/summary/metadata.json" {
				t.Errorf("register view body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"metadata-location":"s3://warehouse/views/summary/metadata.json"}`))
		case path == viewPath && r.Method == http.MethodDelete:
			assertResourceRequest(t, r, http.MethodDelete, viewPath)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})

	ctx := context.Background()
	if object, err := client.GetTable(ctx, "warehouse", []string{"db", "schema"}, "events"); err != nil || object.MetadataLocation == "" {
		t.Fatalf("GetTable() = %#v, %v", object, err)
	}
	if object, err := client.RegisterTable(ctx, "warehouse", []string{"db", "schema"}, "events", "s3://warehouse/events/metadata.json"); err != nil || object.MetadataLocation == "" {
		t.Fatalf("RegisterTable() = %#v, %v", object, err)
	}
	if err := client.UnregisterTable(ctx, "warehouse", []string{"db", "schema"}, "events"); err != nil {
		t.Fatalf("UnregisterTable() error = %v", err)
	}
	if err := client.DeleteTable(ctx, "warehouse", []string{"db", "schema"}, "events"); err != nil {
		t.Fatalf("DeleteTable() error = %v", err)
	}
	if object, err := client.GetView(ctx, "warehouse", []string{"db", "schema"}, "summary"); err != nil || object.MetadataLocation == "" {
		t.Fatalf("GetView() = %#v, %v", object, err)
	}
	if object, err := client.RegisterView(ctx, "warehouse", []string{"db", "schema"}, "summary", "s3://warehouse/views/summary/metadata.json"); err != nil || object.MetadataLocation == "" {
		t.Fatalf("RegisterView() = %#v, %v", object, err)
	}
	if err := client.DeleteView(ctx, "warehouse", []string{"db", "schema"}, "summary"); err != nil {
		t.Fatalf("DeleteView() error = %v", err)
	}
}

func TestIcebergClientRequiresCatalogURL(t *testing.T) {
	client, err := NewClient(Config{
		ManagementURL: "https://polaris.example.com/api/management/v1",
		Auth:          AuthConfig{BearerToken: "test-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := client.GetTable(ctx, "warehouse", nil, "events"); err == nil {
		t.Fatal("GetTable() succeeded without a catalog URL")
	}
	if _, err := client.RegisterView(ctx, "warehouse", nil, "summary", "s3://metadata"); err == nil {
		t.Fatal("RegisterView() succeeded without a catalog URL")
	}
}
