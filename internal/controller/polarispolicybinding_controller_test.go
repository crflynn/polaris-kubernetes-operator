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
	"testing"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestPolarisPolicyBindingLifecycle(t *testing.T) {
	ctx := context.Background()
	const mappingPath = "/api/catalog/polaris/v1/warehouse/namespaces/db%1Fschema/policies/retention/mappings"
	var attached, detached int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.EscapedPath() != mappingPath {
			t.Errorf("request path = %q", req.URL.EscapedPath())
		}
		var body struct {
			Target struct {
				Type string   `json:"type"`
				Path []string `json:"path"`
			} `json:"target"`
			Parameters map[string]string `json:"parameters"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode mapping body: %v", err)
		}
		if body.Target.Type != "table-like" || !reflect.DeepEqual(body.Target.Path, []string{"db", "schema", "events"}) {
			t.Errorf("mapping target = %#v", body.Target)
		}
		switch req.Method {
		case http.MethodPut:
			if body.Parameters["mode"] != "strict" {
				t.Errorf("mapping parameters = %#v", body.Parameters)
			}
			attached++
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPost:
			detached++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.RequestURI())
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
	ready := metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready", Message: "ready", LastTransitionTime: metav1.Now()}
	connection := &polarisv1alpha1.PolarisConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: "default"},
		Spec: polarisv1alpha1.PolarisConnectionSpec{
			ManagementURL: server.URL + "/api/management/v1",
			CatalogURL:    server.URL + "/api/catalog",
			Auth: polarisv1alpha1.PolarisConnectionAuth{BearerTokenSecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "auth"}, Key: "token",
			}},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("test-token")},
	}
	policy := &polarisv1alpha1.PolarisPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "retention", Namespace: "default"},
		Spec: polarisv1alpha1.PolarisPolicySpec{
			CatalogRef: "warehouse-resource", Namespace: []string{"db", "schema"}, PolicyName: "retention", Type: "system.retention",
		},
		Status: polarisv1alpha1.PolarisPolicyStatus{
			Conditions: []metav1.Condition{ready}, ObservedCatalogRef: "warehouse-resource",
			ObservedCatalogName: "warehouse", ObservedConnectionRef: "remote",
			ObservedNamespace: []string{"db", "schema"}, ObservedPolicyName: "retention", ObservedType: "system.retention",
		},
	}
	binding := &polarisv1alpha1.PolarisPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "retention-on-events", Namespace: "default"},
		Spec: polarisv1alpha1.PolarisPolicyBindingSpec{
			PolicyRef: policy.Name, TargetType: "table-like", Namespace: []string{"db", "schema"},
			TargetName: "events", Parameters: map[string]string{"mode": "strict"},
		},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisPolicy{}, &polarisv1alpha1.PolarisPolicyBinding{}).
		WithObjects(connection, secret, policy, binding).Build()
	reconciler := &PolarisPolicyBindingReconciler{Client: kubeClient, Scheme: scheme}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: binding.Namespace, Name: binding.Name}}

	for range 3 {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			t.Fatalf("attach reconcile: %v", err)
		}
	}
	stored := &polarisv1alpha1.PolarisPolicyBinding{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if attached != 1 || !meta.IsStatusConditionTrue(stored.Status.Conditions, "Ready") {
		t.Fatalf("attachment state: calls=%d status=%#v", attached, stored.Status)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("steady-state reconcile: %v", err)
	}
	if attached != 1 {
		t.Fatalf("steady-state reconcile repeated attachment %d times", attached)
	}
	if err := kubeClient.Delete(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("detach reconcile: %v", err)
	}
	if detached != 1 {
		t.Fatalf("detach calls = %d, want 1", detached)
	}
}

func TestPolicyTargetValidation(t *testing.T) {
	tests := []struct {
		name    string
		spec    polarisv1alpha1.PolarisPolicyBindingSpec
		wantErr bool
	}{
		{name: "catalog", spec: polarisv1alpha1.PolarisPolicyBindingSpec{TargetType: "catalog"}},
		{name: "namespace", spec: polarisv1alpha1.PolarisPolicyBindingSpec{TargetType: "namespace", Namespace: []string{"db"}}},
		{name: "table-like", spec: polarisv1alpha1.PolarisPolicyBindingSpec{TargetType: "table-like", Namespace: []string{"db"}, TargetName: "events"}},
		{name: "catalog with namespace", spec: polarisv1alpha1.PolarisPolicyBindingSpec{TargetType: "catalog", Namespace: []string{"db"}}, wantErr: true},
		{name: "missing namespace", spec: polarisv1alpha1.PolarisPolicyBindingSpec{TargetType: "namespace"}, wantErr: true},
		{name: "empty namespace part", spec: polarisv1alpha1.PolarisPolicyBindingSpec{TargetType: "namespace", Namespace: []string{"db", ""}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := policyTarget(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Fatalf("policyTarget() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}
