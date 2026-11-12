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

func TestPolarisViewAdoptObservesWithoutMutation(t *testing.T) {
	ctx := context.Background()
	const viewPath = "/api/catalog/v1/warehouse/namespaces/db.schema/views/summary"
	var gets, mutations int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && req.URL.EscapedPath() == viewPath {
			gets++
			_, _ = w.Write([]byte(`{"metadata-location":"s3://warehouse/current-view.metadata.json"}`))
			return
		}
		mutations++
		http.Error(w, "unexpected mutation", http.StatusBadRequest)
	}))
	defer server.Close()

	view := &polarisv1alpha1.PolarisView{
		ObjectMeta: metav1.ObjectMeta{Name: catalogRoleGrantTestSummary, Namespace: testDefaultNamespace},
		Spec: polarisv1alpha1.PolarisViewSpec{
			CatalogRef: testWarehouseCatalogName, Namespace: []string{"db", catalogRoleGrantTestSchema}, ViewName: catalogRoleGrantTestSummary,
			MetadataLocation: "s3://warehouse/desired-view.metadata.json", OwnershipMode: polarisv1alpha1.PolarisOwnershipAdopt,
		},
	}
	reconciler, kubeClient, request := newViewTestReconciler(t, server, view)
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("adopt reconcile: %v", err)
	}
	stored := &polarisv1alpha1.PolarisView{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if gets != 1 || mutations != 0 || !meta.IsStatusConditionTrue(stored.Status.Conditions, readyConditionType) ||
		!meta.IsStatusConditionTrue(stored.Status.Conditions, "Drifted") ||
		!slices.Equal(stored.Status.DriftedFields, []string{testMetadataLocationProperty}) {
		t.Fatalf("adoption result: gets=%d mutations=%d status=%#v", gets, mutations, stored.Status)
	}
}

func TestPolarisViewDeleteRetainsFinalizerUntilRemoteDeleteSucceeds(t *testing.T) {
	ctx := context.Background()
	const viewPath = "/api/catalog/v1/warehouse/namespaces/db.schema/views/summary"
	deleteAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.EscapedPath() == viewPath:
			_, _ = w.Write([]byte(`{"metadata-location":"s3://warehouse/summary.metadata.json"}`))
		case req.Method == http.MethodDelete && req.URL.EscapedPath() == viewPath:
			deleteAttempts++
			if deleteAttempts == 1 {
				http.Error(w, "remote unavailable", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.RequestURI())
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	view := &polarisv1alpha1.PolarisView{
		ObjectMeta: metav1.ObjectMeta{Name: catalogRoleGrantTestSummary, Namespace: testDefaultNamespace},
		Spec: polarisv1alpha1.PolarisViewSpec{
			CatalogRef: testWarehouseCatalogName, Namespace: []string{"db", catalogRoleGrantTestSchema}, ViewName: catalogRoleGrantTestSummary,
			OwnershipMode: polarisv1alpha1.PolarisOwnershipEnforce, DeletionPolicy: polarisv1alpha1.PolarisDeletionDelete,
		},
	}
	reconciler, kubeClient, request := newViewTestReconciler(t, server, view)
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("finalizer reconcile: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("view reconcile: %v", err)
	}

	stored := &polarisv1alpha1.PolarisView{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(stored.Finalizers, polarisViewFinalizer) {
		t.Fatal("view deletion is missing its finalizer")
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
	if !slices.Contains(stored.Finalizers, polarisViewFinalizer) {
		t.Fatal("view finalizer was removed after the remote delete failed")
	}
	ready := meta.FindStatusCondition(stored.Status.Conditions, readyConditionType)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "ViewDeleteFailed" {
		t.Fatalf("delete failure condition = %#v", ready)
	}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("retry delete reconcile: %v", err)
	}
	if deleteAttempts != 2 {
		t.Fatalf("remote delete attempts = %d, want 2", deleteAttempts)
	}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); !apierrors.IsNotFound(err) {
		t.Fatalf("view remains after successful remote delete: %v", err)
	}
}

func newViewTestReconciler(t *testing.T, server *httptest.Server, view *polarisv1alpha1.PolarisView) (*PolarisViewReconciler, client.Client, reconcile.Request) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := polarisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ready := metav1.Condition{Type: readyConditionType, Status: metav1.ConditionTrue, Reason: readyConditionType, Message: "ready", LastTransitionTime: metav1.Now()}
	connection := &polarisv1alpha1.PolarisConnection{
		ObjectMeta: metav1.ObjectMeta{Name: testRemoteConnectionName, Namespace: view.Namespace},
		Spec: polarisv1alpha1.PolarisConnectionSpec{
			ManagementURL: server.URL + "/api/management/v1", CatalogURL: server.URL + "/api/catalog",
			Auth: polarisv1alpha1.PolarisConnectionAuth{BearerTokenSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: testAuthSecretName}, Key: testTokenKey}},
		},
		Status: polarisv1alpha1.PolarisConnectionStatus{Conditions: []metav1.Condition{ready}, NamespaceSeparator: "."},
	}
	catalog := &polarisv1alpha1.PolarisCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: view.Spec.CatalogRef, Namespace: view.Namespace},
		Spec:       polarisv1alpha1.PolarisCatalogSpec{ConnectionRef: connection.Name, CatalogName: "warehouse"},
		Status:     polarisv1alpha1.PolarisCatalogStatus{Conditions: []metav1.Condition{ready}},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: testAuthSecretName, Namespace: view.Namespace}, Data: map[string][]byte{"token": []byte("test-token")}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(view, &polarisv1alpha1.PolarisCatalog{}, &polarisv1alpha1.PolarisConnection{}).
		WithObjects(connection, catalog, secret, view).Build()
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: view.Namespace, Name: view.Name}}
	return &PolarisViewReconciler{Client: kubeClient, Scheme: scheme}, kubeClient, request
}
