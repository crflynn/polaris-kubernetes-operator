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

func TestPolarisNamespaceEnforceCreatesAndProtectsDelete(t *testing.T) {
	ctx := context.Background()
	const collectionPath = "/api/catalog/v1/warehouse/namespaces"
	const namespacePath = collectionPath + "/db.schema"
	var created, empty, deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got := req.URL.EscapedPath(); got != collectionPath && got != namespacePath {
			t.Errorf("request path = %q", got)
		}
		switch req.Method {
		case http.MethodGet:
			if !created {
				http.NotFound(w, req)
				return
			}
			_, _ = w.Write([]byte(`{"namespace":["db","schema"],"properties":{"owner":"team"}}`))
		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode namespace create: %v", err)
			}
			if !reflect.DeepEqual(body["namespace"], []any{"db", "schema"}) ||
				!reflect.DeepEqual(body["properties"], map[string]any{"owner": "team"}) {
				t.Errorf("create body = %#v", body)
			}
			created = true
			_, _ = w.Write([]byte(`{"namespace":["db","schema"],"properties":{"owner":"team"}}`))
		case http.MethodDelete:
			if empty {
				deleted = true
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "namespace is not empty", http.StatusConflict)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.RequestURI())
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	namespace := testPolarisNamespace("Enforce", "Delete")
	namespace.Spec.Properties = map[string]string{"owner": "team"}
	reconciler, kubeClient, request := newNamespaceTestReconciler(t, server, namespace)

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("finalizer reconcile: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("create reconcile: %v", err)
	}
	stored := &polarisv1alpha1.PolarisNamespace{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if !created || !meta.IsStatusConditionTrue(stored.Status.Conditions, "Ready") ||
		!slices.Equal(stored.Status.ManagedPropertyKeys, []string{"owner"}) {
		t.Fatalf("namespace create status = %#v, created=%t", stored.Status, created)
	}

	if err := kubeClient.Delete(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("non-empty delete reconcile: %v", err)
	}
	stored = &polarisv1alpha1.PolarisNamespace{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(stored.Finalizers, namespaceFinalizer) ||
		meta.IsStatusConditionTrue(stored.Status.Conditions, "Ready") {
		t.Fatalf("non-empty namespace deletion was not blocked: %#v", stored)
	}

	empty = true
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("empty delete reconcile: %v", err)
	}
	if !deleted {
		t.Fatal("empty namespace was not deleted from Polaris")
	}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); !apierrors.IsNotFound(err) {
		t.Fatalf("namespace still exists after remote deletion and finalizer removal: err=%v", err)
	}
}

func TestPolarisNamespaceObserveReportsDriftWithoutMutation(t *testing.T) {
	ctx := context.Background()
	const namespacePath = "/api/catalog/v1/warehouse/namespaces/db.schema"
	var gets, mutations int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && req.URL.EscapedPath() == namespacePath {
			gets++
			_, _ = w.Write([]byte(`{"namespace":["db","schema"],"properties":{"owner":"external","system":"keep"}}`))
			return
		}
		mutations++
		http.Error(w, "unexpected mutation", http.StatusBadRequest)
	}))
	defer server.Close()

	namespace := testPolarisNamespace("Observe", "Orphan")
	namespace.Spec.Properties = map[string]string{"owner": "desired"}
	reconciler, kubeClient, request := newNamespaceTestReconciler(t, server, namespace)
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("observe reconcile: %v", err)
	}
	stored := &polarisv1alpha1.PolarisNamespace{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if gets != 1 || mutations != 0 || !meta.IsStatusConditionTrue(stored.Status.Conditions, "Ready") ||
		!meta.IsStatusConditionTrue(stored.Status.Conditions, "Drifted") ||
		!slices.Equal(stored.Status.DriftedFields, []string{"properties.owner"}) {
		t.Fatalf("observe result: gets=%d mutations=%d status=%#v", gets, mutations, stored.Status)
	}
}

func TestPolarisNamespaceAdoptWaitsBeforeApplyingDesiredProperties(t *testing.T) {
	ctx := context.Background()
	const namespacePath = "/api/catalog/v1/warehouse/namespaces/db.schema"
	var updates, creates, deletes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.EscapedPath() == namespacePath:
			_, _ = w.Write([]byte(`{"namespace":["db","schema"],"properties":{"owner":"external","system":"keep"}}`))
		case req.Method == http.MethodPost && req.URL.EscapedPath() == namespacePath+"/properties":
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode property update: %v", err)
			}
			if !reflect.DeepEqual(body["updates"], map[string]any{"owner": "desired"}) || body["removals"] != nil {
				t.Errorf("adopt update body = %#v", body)
			}
			updates++
			_, _ = w.Write([]byte(`{"updated":["owner"],"removed":[],"missing":[]}`))
		case req.Method == http.MethodPost:
			creates++
			http.Error(w, "unexpected create", http.StatusBadRequest)
		case req.Method == http.MethodDelete:
			deletes++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.RequestURI())
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	namespace := testPolarisNamespace("Adopt", "Orphan")
	namespace.Spec.Properties = map[string]string{"owner": "desired"}
	reconciler, kubeClient, request := newNamespaceTestReconciler(t, server, namespace)
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("initial adopt reconcile: %v", err)
	}
	stored := &polarisv1alpha1.PolarisNamespace{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if updates != 0 || !meta.IsStatusConditionTrue(stored.Status.Conditions, "Drifted") {
		t.Fatalf("initial adoption mutated remote state or missed drift: updates=%d status=%#v", updates, stored.Status)
	}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("adopt update reconcile: %v", err)
	}
	if updates != 1 || creates != 0 || deletes != 0 {
		t.Fatalf("adopt operations: updates=%d creates=%d deletes=%d", updates, creates, deletes)
	}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(stored.Status.Conditions, "Ready") ||
		meta.IsStatusConditionTrue(stored.Status.Conditions, "Drifted") ||
		!slices.Equal(stored.Status.ManagedPropertyKeys, []string{"owner"}) {
		t.Fatalf("adopted namespace status = %#v", stored.Status)
	}
}

func testPolarisNamespace(ownership, deletion string) *polarisv1alpha1.PolarisNamespace {
	return &polarisv1alpha1.PolarisNamespace{
		ObjectMeta: metav1.ObjectMeta{Name: "db-schema", Namespace: "default"},
		Spec: polarisv1alpha1.PolarisNamespaceSpec{
			CatalogRef:     "warehouse",
			Namespace:      []string{"db", "schema"},
			OwnershipMode:  polarisv1alpha1.PolarisOwnershipMode(ownership),
			DeletionPolicy: polarisv1alpha1.PolarisDeletionPolicy(deletion),
		},
	}
}

func newNamespaceTestReconciler(
	t *testing.T,
	server *httptest.Server,
	namespace *polarisv1alpha1.PolarisNamespace,
) (*PolarisNamespaceReconciler, client.Client, reconcile.Request) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := polarisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ready := metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready", Message: "ready",
		LastTransitionTime: metav1.Now(),
	}
	connection := &polarisv1alpha1.PolarisConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: namespace.Namespace},
		Spec: polarisv1alpha1.PolarisConnectionSpec{
			ManagementURL: server.URL + "/api/management/v1",
			CatalogURL:    server.URL + "/api/catalog",
			Auth: polarisv1alpha1.PolarisConnectionAuth{BearerTokenSecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "auth"}, Key: "token",
			}},
		},
		Status: polarisv1alpha1.PolarisConnectionStatus{
			Conditions: []metav1.Condition{ready}, NamespaceSeparator: ".",
		},
	}
	catalog := &polarisv1alpha1.PolarisCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: namespace.Spec.CatalogRef, Namespace: namespace.Namespace},
		Spec:       polarisv1alpha1.PolarisCatalogSpec{ConnectionRef: connection.Name, CatalogName: "warehouse"},
		Status:     polarisv1alpha1.PolarisCatalogStatus{Conditions: []metav1.Condition{ready}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: namespace.Namespace},
		Data:       map[string][]byte{"token": []byte("test-token")},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisNamespace{}, &polarisv1alpha1.PolarisCatalog{}, &polarisv1alpha1.PolarisConnection{}).
		WithObjects(connection, catalog, secret, namespace).Build()
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace.Namespace, Name: namespace.Name}}
	return &PolarisNamespaceReconciler{Client: kubeClient, Scheme: scheme}, kubeClient, request
}
