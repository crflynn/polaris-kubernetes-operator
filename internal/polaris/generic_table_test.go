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

const (
	genericTestBearerToken  = "test-token"
	genericTestSchema       = "schema"
	genericTestCurrentTable = "events/current"
)

func TestGenericTableClientUsesPolarisBetaAPIAndDropsSensitiveConfig(t *testing.T) {
	t.Parallel()
	const collectionPath = "/api/catalog/polaris/v1/warehouse/namespaces/db.schema/generic-tables"
	const tablePath = collectionPath + "/events%2Fcurrent"
	var created, deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got := req.Header.Get("Authorization"); got != "Bearer "+genericTestBearerToken {
			t.Errorf("Authorization = %q", got)
		}
		switch {
		case req.Method == http.MethodPost && req.URL.EscapedPath() == collectionPath:
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode generic table create: %v", err)
			}
			if body["name"] != genericTestCurrentTable || body["format"] != "delta" || body["base-location"] != "s3://warehouse/events" {
				t.Errorf("create body = %#v", body)
			}
			created = true
			_, _ = w.Write([]byte(`{"table":{"name":"events/current","format":"delta","base-location":"s3://warehouse/events"}}`))
		case req.Method == http.MethodGet && req.URL.EscapedPath() == tablePath:
			_, _ = w.Write([]byte(`{"table":{"name":"events/current","format":"delta","base-location":"s3://warehouse/events"},"storage-access-configs":[{"config":{"secret":"never-return-this"}}]}`))
		case req.Method == http.MethodDelete && req.URL.EscapedPath() == tablePath:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.RequestURI())
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	base, err := NewClient(Config{
		ManagementURL: server.URL + "/management", CatalogURL: server.URL + "/api/catalog",
		NamespaceSeparator: ".", Auth: AuthConfig{BearerToken: genericTestBearerToken},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	generic := NewGenericTableClient(base)
	namespace := []string{"db", genericTestSchema}
	createdTable, err := generic.CreateTable(context.Background(), "warehouse", namespace, GenericTable{
		Name: genericTestCurrentTable, Format: "delta", BaseLocation: "s3://warehouse/events",
	})
	if err != nil || createdTable.Name != genericTestCurrentTable {
		t.Fatalf("CreateTable() = %#v, %v", createdTable, err)
	}
	loadedTable, err := generic.GetTable(context.Background(), "warehouse", namespace, genericTestCurrentTable)
	if err != nil || !reflect.DeepEqual(loadedTable, createdTable) {
		t.Fatalf("GetTable() = %#v, %v; want %#v", loadedTable, err, createdTable)
	}
	if err := generic.DeleteTable(context.Background(), "warehouse", namespace, genericTestCurrentTable); err != nil {
		t.Fatalf("DeleteTable() error = %v", err)
	}
	if !created || !deleted {
		t.Fatalf("operations: created=%t deleted=%t", created, deleted)
	}
}

func TestGenericTableClientClassifiesNotImplemented(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	}))
	defer server.Close()
	base, err := NewClient(Config{
		ManagementURL: server.URL + "/management", CatalogURL: server.URL + "/api/catalog",
		Auth: AuthConfig{BearerToken: genericTestBearerToken},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = NewGenericTableClient(base).GetTable(context.Background(), "warehouse", []string{"db"}, "events")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("GetTable() error = %v, want ErrUnsupported", err)
	}
}
