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

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestPolarisTableObserveReportsMetadataDriftWithoutMutation(t *testing.T) {
	ctx := context.Background()
	const tablePath = "/api/catalog/v1/warehouse/namespaces/db.schema/tables/events"
	var mutations int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && req.URL.EscapedPath() == tablePath {
			_, _ = w.Write([]byte(`{"metadata-location":"s3://warehouse/remote.metadata.json"}`))
			return
		}
		mutations++
		http.Error(w, "unexpected mutation", http.StatusBadRequest)
	}))
	defer server.Close()

	table := testPolarisTable("Observe", "Orphan")
	table.Spec.MetadataLocation = "s3://warehouse/desired.metadata.json"
	reconciler, kubeClient, request := newTableTestReconciler(t, server, table)
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("observe reconcile: %v", err)
	}
	stored := &polarisv1alpha1.PolarisTable{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if mutations != 0 || !meta.IsStatusConditionTrue(stored.Status.Conditions, "Ready") ||
		!meta.IsStatusConditionTrue(stored.Status.Conditions, "Drifted") ||
		!slices.Equal(stored.Status.DriftedFields, []string{"metadataLocation"}) {
		t.Fatalf("observe result: mutations=%d status=%#v", mutations, stored.Status)
	}
}

func TestPolarisTableEnforceRegistersOnlyWhenAbsentAndNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	const tablePath = "/api/catalog/v1/warehouse/namespaces/db.schema/tables/events"
	const registerPath = "/api/catalog/v1/warehouse/namespaces/db.schema/register"
	registered := false
	var registerRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.EscapedPath() == tablePath:
			if !registered {
				http.NotFound(w, req)
				return
			}
			_, _ = w.Write([]byte(`{"metadata-location":"s3://warehouse/engine.metadata.json"}`))
		case req.Method == http.MethodPost && req.URL.EscapedPath() == registerPath:
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode registration request: %v", err)
			}
			if body["name"] != "events" || body["metadata-location"] != "s3://warehouse/desired.metadata.json" {
				t.Errorf("registration request = %#v", body)
			}
			if _, exists := body["overwrite"]; exists {
				t.Error("registration unexpectedly enabled overwrite")
			}
			registerRequests++
			registered = true
			_, _ = w.Write([]byte(`{"metadata-location":"s3://warehouse/desired.metadata.json"}`))
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.RequestURI())
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	table := testPolarisTable("Enforce", "Orphan")
	table.Spec.MetadataLocation = "s3://warehouse/desired.metadata.json"
	reconciler, kubeClient, request := newTableTestReconciler(t, server, table)
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("register reconcile: %v", err)
	}
	stored := &polarisv1alpha1.PolarisTable{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if !registered || registerRequests != 1 || !meta.IsStatusConditionTrue(stored.Status.Conditions, "Ready") {
		t.Fatalf("registration result: registered=%t requests=%d status=%#v", registered, registerRequests, stored.Status)
	}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("external update observation: %v", err)
	}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if registerRequests != 1 || !meta.IsStatusConditionTrue(stored.Status.Conditions, "Drifted") ||
		!slices.Equal(stored.Status.DriftedFields, []string{"metadataLocation"}) {
		t.Fatalf("engine change was overwritten or drift missed: requests=%d status=%#v", registerRequests, stored.Status)
	}
}

func TestPolarisTableExplicitUnregisterUsesFinalizer(t *testing.T) {
	ctx := context.Background()
	const tablePath = "/api/catalog/v1/warehouse/namespaces/db.schema/tables/events"
	var unregistered bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.EscapedPath() == tablePath:
			_, _ = w.Write([]byte(`{"metadata-location":"s3://warehouse/events.metadata.json"}`))
		case req.Method == http.MethodPost && req.URL.EscapedPath() == tablePath+"/unregister":
			unregistered = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.RequestURI())
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	table := testPolarisTable("Enforce", "Unregister")
	reconciler, kubeClient, request := newTableTestReconciler(t, server, table)
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("finalizer reconcile: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("observe reconcile: %v", err)
	}
	stored := &polarisv1alpha1.PolarisTable{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(stored.Finalizers, polarisTableFinalizer) {
		t.Fatal("explicit unregister is missing its finalizer")
	}
	if err := kubeClient.Delete(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("unregister reconcile: %v", err)
	}
	if !unregistered {
		t.Fatal("Polaris table was not unregistered")
	}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); !apierrors.IsNotFound(err) {
		t.Fatalf("table CR remains after successful unregister: %v", err)
	}
}

func testPolarisTable(ownership, deletion string) *polarisv1alpha1.PolarisTable {
	return &polarisv1alpha1.PolarisTable{
		ObjectMeta: metav1.ObjectMeta{Name: "events", Namespace: "default"},
		Spec: polarisv1alpha1.PolarisTableSpec{
			CatalogRef: "warehouse", Namespace: []string{"db", "schema"}, TableName: "events",
			OwnershipMode:  polarisv1alpha1.PolarisOwnershipMode(ownership),
			DeletionPolicy: polarisv1alpha1.PolarisDeletionPolicy(deletion),
		},
	}
}

func newTableTestReconciler(t *testing.T, server *httptest.Server, table *polarisv1alpha1.PolarisTable) (*PolarisTableReconciler, client.Client, reconcile.Request) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := polarisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ready := metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready", Message: "ready", LastTransitionTime: metav1.Now()}
	connection := &polarisv1alpha1.PolarisConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: table.Namespace},
		Spec: polarisv1alpha1.PolarisConnectionSpec{
			ManagementURL: server.URL + "/api/management/v1", CatalogURL: server.URL + "/api/catalog",
			Auth: polarisv1alpha1.PolarisConnectionAuth{BearerTokenSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "auth"}, Key: "token"}},
		},
		Status: polarisv1alpha1.PolarisConnectionStatus{Conditions: []metav1.Condition{ready}, NamespaceSeparator: "."},
	}
	catalog := &polarisv1alpha1.PolarisCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: table.Spec.CatalogRef, Namespace: table.Namespace},
		Spec:       polarisv1alpha1.PolarisCatalogSpec{ConnectionRef: connection.Name, CatalogName: "warehouse"},
		Status:     polarisv1alpha1.PolarisCatalogStatus{Conditions: []metav1.Condition{ready}},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: table.Namespace}, Data: map[string][]byte{"token": []byte("test-token")}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(table, &polarisv1alpha1.PolarisCatalog{}, &polarisv1alpha1.PolarisConnection{}).
		WithObjects(connection, catalog, secret, table).Build()
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: table.Namespace, Name: table.Name}}
	return &PolarisTableReconciler{Client: kubeClient, Scheme: scheme}, kubeClient, request
}
