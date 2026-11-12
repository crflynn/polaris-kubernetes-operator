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

func TestPolarisCatalogRoleBindingLifecycle(t *testing.T) {
	ctx := context.Background()
	var assigned []polarisclient.CatalogRole
	var assignCalls, revokeCalls int
	const collectionPath = "/api/management/v1/principal-roles/analytics-readers/catalog-roles/warehouse"
	const itemPath = collectionPath + "/warehouse-writers"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.URL.Path == collectionPath && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"roles": assigned})
		case r.URL.Path == collectionPath && r.Method == http.MethodPut:
			var body struct {
				CatalogRole polarisclient.CatalogRole `json:"catalogRole"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode assignment: %v", err)
			}
			if body.CatalogRole.Name != "warehouse-writers" {
				t.Errorf("assigned role = %q", body.CatalogRole.Name)
			}
			assigned = append(assigned, body.CatalogRole)
			assignCalls++
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == itemPath && r.Method == http.MethodDelete:
			assigned = nil
			revokeCalls++
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
	readyCondition := metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready", Message: "ready", LastTransitionTime: metav1.Now()}
	principalRole := &polarisv1alpha1.PolarisPrincipalRole{
		ObjectMeta: metav1.ObjectMeta{Name: "analytics-readers", Namespace: "default"},
		Spec:       polarisv1alpha1.PolarisPrincipalRoleSpec{ConnectionRef: "remote", RoleName: "analytics-readers"},
		Status:     polarisv1alpha1.PolarisPrincipalRoleStatus{Conditions: []metav1.Condition{readyCondition}},
	}
	catalogRole := &polarisv1alpha1.PolarisCatalogRole{
		ObjectMeta: metav1.ObjectMeta{Name: "warehouse-writers", Namespace: "default"},
		Spec:       polarisv1alpha1.PolarisCatalogRoleSpec{CatalogRef: "warehouse", CatalogRoleName: "warehouse-writers"},
		Status: polarisv1alpha1.PolarisCatalogRoleStatus{
			Conditions: []metav1.Condition{readyCondition}, ObservedCatalogName: "warehouse",
			ObservedRoleName: "warehouse-writers", ObservedConnectionRef: "remote",
		},
	}
	binding := &polarisv1alpha1.PolarisCatalogRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "analytics-readers-warehouse-writers", Namespace: "default"},
		Spec:       polarisv1alpha1.PolarisCatalogRoleBindingSpec{PrincipalRoleRef: principalRole.Name, CatalogRoleRef: catalogRole.Name},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisCatalogRoleBinding{}).
		WithObjects(connection, authSecret, principalRole, catalogRole, binding).Build()
	reconciler := &PolarisCatalogRoleBindingReconciler{Client: k8sClient, Scheme: scheme}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: binding.Namespace, Name: binding.Name}}

	for range 3 {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			t.Fatalf("assignment reconcile: %v", err)
		}
	}
	if assignCalls != 1 || len(assigned) != 1 || assigned[0].Name != "warehouse-writers" {
		t.Fatalf("assignment calls=%d roles=%#v", assignCalls, assigned)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("steady-state reconcile: %v", err)
	}
	if assignCalls != 1 {
		t.Fatalf("steady-state reconciliation repeated assignment %d times", assignCalls)
	}
	stored := &polarisv1alpha1.PolarisCatalogRoleBinding{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.ObservedPrincipalRoleName != "analytics-readers" || stored.Status.ObservedCatalogName != "warehouse" || stored.Status.ObservedCatalogRoleName != "warehouse-writers" {
		t.Fatalf("binding identity provenance missing: %#v", stored.Status)
	}
	if err := k8sClient.Delete(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("revoke reconcile: %v", err)
	}
	if revokeCalls != 1 || len(assigned) != 0 {
		t.Fatalf("revocation calls=%d roles=%#v", revokeCalls, assigned)
	}
}
