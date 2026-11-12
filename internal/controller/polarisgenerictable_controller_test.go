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
	"reflect"
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

const (
	genericTableTestReady        = readyConditionType
	genericTableTestReadyMessage = "ready"
	genericTableTestBaseLocation = "s3://warehouse/events"
	genericTableTestSchema       = "schema"
)

func TestPolarisGenericTableEnforceCreatesRemoteTable(t *testing.T) {
	ctx := context.Background()
	const collectionPath = "/api/catalog/polaris/v1/warehouse/namespaces/db.schema/generic-tables"
	const tablePath = collectionPath + "/" + testEventsTableName
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.EscapedPath() != collectionPath && req.URL.EscapedPath() != tablePath {
			t.Errorf("request path = %q", req.URL.EscapedPath())
		}
		switch req.Method {
		case http.MethodGet:
			if !created {
				http.NotFound(w, req)
				return
			}
			_, _ = w.Write([]byte(`{"table":{"name":"` + testEventsTableName + `","format":"delta","base-location":"` + genericTableTestBaseLocation + `","properties":{"` + testOwnerProperty + `":"team"}},"storage-access-configs":[{"prefix":"s3://warehouse","config":{"secret":"ignored"}}]}`))
		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode generic table create: %v", err)
			}
			if body["name"] != testEventsTableName || body["format"] != testDeltaTableFormat ||
				body["base-location"] != genericTableTestBaseLocation ||
				!reflect.DeepEqual(body["properties"], map[string]any{testOwnerProperty: "team"}) {
				t.Errorf("create body = %#v", body)
			}
			created = true
			_, _ = w.Write([]byte(`{"table":{"name":"` + testEventsTableName + `","format":"delta","base-location":"` + genericTableTestBaseLocation + `","properties":{"` + testOwnerProperty + `":"team"}}}`))
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.RequestURI())
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	table := testPolarisGenericTable("Enforce", "Orphan")
	table.Spec.Format = testDeltaTableFormat
	table.Spec.BaseLocation = genericTableTestBaseLocation
	table.Spec.Properties = map[string]string{testOwnerProperty: "team"}
	reconciler, kubeClient, request := newGenericTableTestReconciler(t, server, table)
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	stored := &polarisv1alpha1.PolarisGenericTable{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if !created || !meta.IsStatusConditionTrue(stored.Status.Conditions, genericTableTestReady) ||
		stored.Status.RemoteTableName != testEventsTableName || stored.Status.RemoteFormat != testDeltaTableFormat ||
		stored.Status.RemoteBaseLocation != genericTableTestBaseLocation {
		t.Fatalf("create result: created=%t status=%#v", created, stored.Status)
	}
}

func TestPolarisGenericTableObserveReportsDriftWithoutMutation(t *testing.T) {
	ctx := context.Background()
	const tablePath = "/api/catalog/polaris/v1/warehouse/namespaces/db.schema/generic-tables/" + testEventsTableName
	var gets, mutations int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && req.URL.EscapedPath() == tablePath {
			gets++
			_, _ = w.Write([]byte(`{"table":{"name":"` + testEventsTableName + `","format":"parquet","base-location":"s3://external/events","properties":{"` + testOwnerProperty + `":"external"}}}`))
			return
		}
		mutations++
		http.Error(w, "unexpected mutation", http.StatusBadRequest)
	}))
	defer server.Close()

	table := testPolarisGenericTable("Observe", "Orphan")
	table.Spec.Format = testDeltaTableFormat
	table.Spec.BaseLocation = "s3://" + testDesiredValue + "/events"
	table.Spec.Properties = map[string]string{testOwnerProperty: testDesiredValue}
	reconciler, kubeClient, request := newGenericTableTestReconciler(t, server, table)
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("observe reconcile: %v", err)
	}
	stored := &polarisv1alpha1.PolarisGenericTable{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	wantDrift := []string{"format", "baseLocation", "properties"}
	if gets != 1 || mutations != 0 || !meta.IsStatusConditionTrue(stored.Status.Conditions, genericTableTestReady) ||
		!meta.IsStatusConditionTrue(stored.Status.Conditions, "Drifted") ||
		!slices.Equal(stored.Status.DriftedFields, wantDrift) {
		t.Fatalf("observe result: gets=%d mutations=%d status=%#v", gets, mutations, stored.Status)
	}
}

func TestPolarisGenericTableEnforceDeleteFinalizerDropsRemoteCatalogEntry(t *testing.T) {
	ctx := context.Background()
	const collectionPath = "/api/catalog/polaris/v1/warehouse/namespaces/db.schema/generic-tables"
	const tablePath = collectionPath + "/" + testEventsTableName
	created, deleted := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.EscapedPath() != collectionPath && req.URL.EscapedPath() != tablePath {
			t.Errorf("request path = %q", req.URL.EscapedPath())
		}
		switch req.Method {
		case http.MethodGet:
			if !created || deleted {
				http.NotFound(w, req)
				return
			}
			_, _ = w.Write([]byte(`{"table":{"name":"` + testEventsTableName + `","format":"delta"}}`))
		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.RequestURI())
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	table := testPolarisGenericTable("Enforce", "Delete")
	table.Spec.Format = testDeltaTableFormat
	reconciler, kubeClient, request := newGenericTableTestReconciler(t, server, table)
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("install finalizer: %v", err)
	}
	stored := &polarisv1alpha1.PolarisGenericTable{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(stored.Finalizers, genericTableFinalizer) {
		t.Fatalf("finalizer was not installed: %#v", stored.Finalizers)
	}
	created = true
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("reconcile existing table: %v", err)
	}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if err := kubeClient.Delete(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("finalize table: %v", err)
	}
	if !deleted {
		t.Fatal("remote generic table was not deleted")
	}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); !apierrors.IsNotFound(err) {
		t.Fatalf("resource remains after finalization: err=%v", err)
	}
}

func TestPolarisGenericTableDeleteRetainsFinalizerUntilRemoteDeleteSucceeds(t *testing.T) {
	ctx := context.Background()
	const collectionPath = "/api/catalog/polaris/v1/warehouse/namespaces/db.schema/generic-tables"
	const tablePath = collectionPath + "/" + testEventsTableName
	created := false
	deleteAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.EscapedPath() != collectionPath && req.URL.EscapedPath() != tablePath {
			t.Errorf("request path = %q", req.URL.EscapedPath())
		}
		switch req.Method {
		case http.MethodGet:
			if !created {
				http.NotFound(w, req)
				return
			}
			_, _ = w.Write([]byte(`{"table":{"name":"` + testEventsTableName + `","format":"delta"}}`))
		case http.MethodDelete:
			deleteAttempts++
			if deleteAttempts == 1 {
				http.Error(w, "remote unavailable", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.RequestURI())
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	table := testPolarisGenericTable("Enforce", "Delete")
	table.Spec.Format = testDeltaTableFormat
	reconciler, kubeClient, request := newGenericTableTestReconciler(t, server, table)
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("install finalizer: %v", err)
	}
	created = true
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("reconcile existing table: %v", err)
	}
	stored := &polarisv1alpha1.PolarisGenericTable{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if err := kubeClient.Delete(ctx, stored); err != nil {
		t.Fatal(err)
	}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("failed delete reconcile: %v", err)
	}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(stored.Finalizers, genericTableFinalizer) {
		t.Fatal("generic table finalizer was removed after remote deletion failed")
	}
	ready := meta.FindStatusCondition(stored.Status.Conditions, genericTableTestReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "TableDeleteFailed" {
		t.Fatalf("delete failure condition = %#v", ready)
	}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("retry delete reconcile: %v", err)
	}
	if deleteAttempts != 2 {
		t.Fatalf("remote delete attempts = %d, want 2", deleteAttempts)
	}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); !apierrors.IsNotFound(err) {
		t.Fatalf("resource remains after successful finalization: err=%v", err)
	}
}

func TestPolarisGenericTableRequiresExplicitBetaOptIn(t *testing.T) {
	ctx := context.Background()
	var remoteRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		remoteRequests++
		http.Error(w, "request must be gated", http.StatusBadRequest)
	}))
	defer server.Close()

	table := testPolarisGenericTable("Observe", "Orphan")
	reconciler, kubeClient, request := newGenericTableTestReconciler(t, server, table)
	connection := &polarisv1alpha1.PolarisConnection{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: table.Namespace, Name: testRemoteConnectionName}, connection); err != nil {
		t.Fatal(err)
	}
	connection.Spec.EnableBetaGenericTables = false
	if err := kubeClient.Update(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("disabled-feature reconcile: %v", err)
	}
	stored := &polarisv1alpha1.PolarisGenericTable{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(stored.Status.Conditions, genericTableTestReady)
	if remoteRequests != 0 || condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "BetaFeatureDisabled" {
		t.Fatalf("feature gate result: requests=%d condition=%#v", remoteRequests, condition)
	}
}

func testPolarisGenericTable(ownership, deletion string) *polarisv1alpha1.PolarisGenericTable {
	return &polarisv1alpha1.PolarisGenericTable{
		ObjectMeta: metav1.ObjectMeta{Name: "db-schema-events", Namespace: testDefaultNamespace},
		Spec: polarisv1alpha1.PolarisGenericTableSpec{
			CatalogRef: testWarehouseCatalogName, Namespace: []string{"db", genericTableTestSchema}, TableName: testEventsTableName,
			OwnershipMode:  polarisv1alpha1.PolarisOwnershipMode(ownership),
			DeletionPolicy: polarisv1alpha1.PolarisDeletionPolicy(deletion),
		},
	}
}

func newGenericTableTestReconciler(
	t *testing.T,
	server *httptest.Server,
	table *polarisv1alpha1.PolarisGenericTable,
) (*PolarisGenericTableReconciler, client.Client, reconcile.Request) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := polarisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ready := metav1.Condition{Type: genericTableTestReady, Status: metav1.ConditionTrue, Reason: genericTableTestReady, Message: genericTableTestReadyMessage, LastTransitionTime: metav1.Now()}
	connection := &polarisv1alpha1.PolarisConnection{
		ObjectMeta: metav1.ObjectMeta{Name: testRemoteConnectionName, Namespace: table.Namespace},
		Spec: polarisv1alpha1.PolarisConnectionSpec{
			ManagementURL: server.URL + "/api/management/v1", CatalogURL: server.URL + "/api/catalog",
			EnableBetaGenericTables: true,
			Auth: polarisv1alpha1.PolarisConnectionAuth{BearerTokenSecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: testAuthSecretName}, Key: testTokenKey,
			}},
		},
		Status: polarisv1alpha1.PolarisConnectionStatus{
			Conditions: []metav1.Condition{ready}, NamespaceSeparator: ".",
			CatalogURL: server.URL + "/api/catalog",
		},
	}
	catalog := &polarisv1alpha1.PolarisCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: table.Spec.CatalogRef, Namespace: table.Namespace},
		Spec:       polarisv1alpha1.PolarisCatalogSpec{ConnectionRef: connection.Name, CatalogName: testWarehouseCatalogName},
		Status:     polarisv1alpha1.PolarisCatalogStatus{Conditions: []metav1.Condition{ready}},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: testAuthSecretName, Namespace: table.Namespace}, Data: map[string][]byte{testTokenKey: []byte("test-token")}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisGenericTable{}, &polarisv1alpha1.PolarisCatalog{}, &polarisv1alpha1.PolarisConnection{}).
		WithObjects(connection, catalog, secret, table).Build()
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: table.Namespace, Name: table.Name}}
	return &PolarisGenericTableReconciler{Client: kubeClient, Scheme: scheme}, kubeClient, request
}
