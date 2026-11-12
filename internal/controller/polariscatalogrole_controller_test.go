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
	"testing"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
	polarisclient "github.com/apache/polaris-kubernetes-operator/internal/polaris"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestPolarisCatalogRoleLifecycle(t *testing.T) {
	ctx := context.Background()
	var remote *polarisclient.CatalogRole
	var creates, updates, deletes int
	const rolePath = "/api/management/v1/catalogs/warehouse/catalog-roles/analysts"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.URL.Path == rolePath && r.Method == http.MethodGet:
			if remote == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(remote)
		case r.URL.Path == "/api/management/v1/catalogs/warehouse/catalog-roles" && r.Method == http.MethodPost:
			var body struct {
				CatalogRole polarisclient.CatalogRole `json:"catalogRole"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create: %v", err)
			}
			body.CatalogRole.EntityVersion = 1
			remote = &body.CatalogRole
			creates++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(remote)
		case r.URL.Path == rolePath && r.Method == http.MethodPut:
			var body struct {
				CurrentEntityVersion int64             `json:"currentEntityVersion"`
				Properties           map[string]string `json:"properties"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode update: %v", err)
			}
			if remote == nil || body.CurrentEntityVersion != remote.EntityVersion {
				http.Error(w, "version conflict", http.StatusConflict)
				return
			}
			remote.Properties = body.Properties
			remote.EntityVersion++
			updates++
			_ = json.NewEncoder(w).Encode(remote)
		case r.URL.Path == rolePath && r.Method == http.MethodDelete:
			remote = nil
			deletes++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := polarisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	connection := &polarisv1alpha1.PolarisConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: "default"},
		Spec: polarisv1alpha1.PolarisConnectionSpec{
			ManagementURL: server.URL + "/api/management/v1",
			Auth: polarisv1alpha1.PolarisConnectionAuth{BearerTokenSecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "auth"}, Key: "token",
			}},
		},
	}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"}, Data: map[string][]byte{"token": []byte("test-token")}}
	catalog := &polarisv1alpha1.PolarisCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: "warehouse-resource", Namespace: "default"},
		Spec:       polarisv1alpha1.PolarisCatalogSpec{ConnectionRef: "remote", CatalogName: "warehouse"},
	}
	role := &polarisv1alpha1.PolarisCatalogRole{
		ObjectMeta: metav1.ObjectMeta{Name: "analysts", Namespace: "default"},
		Spec:       polarisv1alpha1.PolarisCatalogRoleSpec{CatalogRef: catalog.Name, CatalogRoleName: "analysts", Properties: map[string]string{"description": "read access"}},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisCatalogRole{}).
		WithObjects(connection, authSecret, catalog, role).Build()
	reconciler := &PolarisCatalogRoleReconciler{Client: k8sClient, Scheme: scheme}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: role.Namespace, Name: role.Name}}

	for range 3 {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			t.Fatalf("create reconcile: %v", err)
		}
	}
	if creates != 1 || remote == nil || remote.Properties["description"] != "read access" {
		t.Fatalf("remote role after create: creates=%d remote=%#v", creates, remote)
	}
	stored := &polarisv1alpha1.PolarisCatalogRole{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.ObservedCatalogName != "warehouse" || stored.Status.ObservedConnectionRef != "remote" {
		t.Fatalf("remote identity provenance was not recorded: %#v", stored.Status)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("steady-state reconcile: %v", err)
	}
	if updates != 0 {
		t.Fatalf("steady-state reconciliation issued %d updates", updates)
	}

	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Properties = map[string]string{"description": "write access"}
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("update reconcile: %v", err)
	}
	if updates != 1 || remote.Properties["description"] != "write access" || remote.EntityVersion != 2 {
		t.Fatalf("remote role after update: updates=%d remote=%#v", updates, remote)
	}

	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Delete(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}
	if deletes != 1 || remote != nil {
		t.Fatalf("remote delete not performed: deletes=%d remote=%#v", deletes, remote)
	}
}
