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
	"testing"
)

const (
	icebergTestMetadataLocation = "s3://warehouse/metadata.json"
	icebergTestMetadataResponse = `{"metadata-location":"` + icebergTestMetadataLocation + `"}`
)

func TestIcebergRegistrationClientUsesNestedPathsAndSafeOperations(t *testing.T) {
	t.Parallel()

	const (
		tablePath = "/api/catalog/v1/warehouse/namespaces/db.schema/tables/sales%2F2026"
		viewPath  = "/api/catalog/v1/warehouse/namespaces/db.schema/views/summary"
	)
	seen := make(map[string]bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		key := r.Method + " " + r.URL.EscapedPath()
		switch key {
		case "GET " + tablePath, "GET " + viewPath:
			seen[key] = true
			_, _ = w.Write([]byte(icebergTestMetadataResponse))
		case "POST /api/catalog/v1/warehouse/namespaces/db.schema/register":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode table registration: %v", err)
			}
			if !reflect.DeepEqual(body, map[string]string{"name": "sales/2026", "metadata-location": icebergTestMetadataLocation}) {
				t.Errorf("table registration body = %#v", body)
			}
			seen[key] = true
			_, _ = w.Write([]byte(icebergTestMetadataResponse))
		case "POST /api/catalog/v1/warehouse/namespaces/db.schema/register-view":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode view registration: %v", err)
			}
			if !reflect.DeepEqual(body, map[string]string{"name": "summary", "metadata-location": "s3://warehouse/view-metadata.json"}) {
				t.Errorf("view registration body = %#v", body)
			}
			seen[key] = true
			_, _ = w.Write([]byte(`{"metadata-location":"s3://warehouse/view-metadata.json"}`))
		case "POST " + tablePath + "/unregister", "DELETE " + tablePath, "DELETE " + viewPath:
			seen[key] = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{
		ManagementURL:      server.URL + "/management",
		CatalogURL:         server.URL + "/api/catalog",
		NamespaceSeparator: ".",
		Auth:               AuthConfig{BearerToken: "test-token"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	namespace := []string{"db", "schema"}
	if _, err := client.RegisterTable(context.Background(), "warehouse", namespace, "sales/2026", icebergTestMetadataLocation); err != nil {
		t.Fatalf("RegisterTable() error = %v", err)
	}
	if table, err := client.GetTable(context.Background(), "warehouse", namespace, "sales/2026"); err != nil || table.MetadataLocation != icebergTestMetadataLocation {
		t.Fatalf("GetTable() = %#v, %v", table, err)
	}
	if err := client.UnregisterTable(context.Background(), "warehouse", namespace, "sales/2026"); err != nil {
		t.Fatalf("UnregisterTable() error = %v", err)
	}
	if err := client.DeleteTable(context.Background(), "warehouse", namespace, "sales/2026"); err != nil {
		t.Fatalf("DeleteTable() error = %v", err)
	}
	if _, err := client.RegisterView(context.Background(), "warehouse", namespace, "summary", "s3://warehouse/view-metadata.json"); err != nil {
		t.Fatalf("RegisterView() error = %v", err)
	}
	if view, err := client.GetView(context.Background(), "warehouse", namespace, "summary"); err != nil || view.MetadataLocation != icebergTestMetadataLocation {
		t.Fatalf("GetView() = %#v, %v", view, err)
	}
	if err := client.DeleteView(context.Background(), "warehouse", namespace, "summary"); err != nil {
		t.Fatalf("DeleteView() error = %v", err)
	}
	if len(seen) != 7 {
		t.Fatalf("observed %d API operations, want 7: %#v", len(seen), seen)
	}
}
