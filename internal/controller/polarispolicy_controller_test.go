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
	"fmt"
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

func TestPolarisPolicyLifecycleAndDeleteOrdering(t *testing.T) {
	ctx := context.Background()
	const collectionPath = "/api/catalog/polaris/v1/warehouse/namespaces/db%1Fschema/policies"
	const policyPath = collectionPath + "/retention"
	var created, deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		expectedPath := policyPath
		if req.Method == http.MethodPost {
			expectedPath = collectionPath
		}
		if req.URL.EscapedPath() != expectedPath {
			t.Errorf("request path = %q, want %q", req.URL.EscapedPath(), expectedPath)
		}
		switch req.Method {
		case http.MethodGet:
			http.NotFound(w, req)
		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			if body["name"] != "retention" || body["type"] != "system.retention" {
				t.Errorf("create body = %#v", body)
			}
			created = true
			_, _ = w.Write([]byte(`{"policy":{"policy-type":"system.retention","inheritable":false,"name":"retention","description":"keep records","content":"{}","version":0}}`))
		case http.MethodDelete:
			if req.URL.Query().Get("detach-all") != "true" {
				t.Errorf("delete query = %q", req.URL.RawQuery)
			}
			deleted = true
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
	catalog := &polarisv1alpha1.PolarisCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: "warehouse-resource", Namespace: "default"},
		Spec:       polarisv1alpha1.PolarisCatalogSpec{ConnectionRef: "remote", CatalogName: "warehouse"},
		Status:     polarisv1alpha1.PolarisCatalogStatus{Conditions: []metav1.Condition{ready}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("test-token")},
	}
	policy := &polarisv1alpha1.PolarisPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "retention", Namespace: "default"},
		Spec: polarisv1alpha1.PolarisPolicySpec{
			CatalogRef: catalog.Name, Namespace: []string{"db", "schema"}, PolicyName: "retention",
			Type: "system.retention", Description: "keep records", Content: "{}", DetachAllOnDelete: true,
		},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisPolicy{}, &polarisv1alpha1.PolarisPolicyBinding{}, &polarisv1alpha1.PolarisCatalog{}).
		WithObjects(connection, catalog, secret, policy).Build()
	reconciler := &PolarisPolicyReconciler{Client: kubeClient, Scheme: scheme}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}}

	for range 3 {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			t.Fatalf("create reconcile: %v", err)
		}
	}
	stored := &polarisv1alpha1.PolarisPolicy{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if !created || !meta.IsStatusConditionTrue(stored.Status.Conditions, "Ready") || stored.Status.PolicyVersion != 0 {
		t.Fatalf("policy create state: created=%t status=%#v", created, stored.Status)
	}

	binding := &polarisv1alpha1.PolarisPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "retention-on-events", Namespace: policy.Namespace},
		Spec:       polarisv1alpha1.PolarisPolicyBindingSpec{PolicyRef: policy.Name, TargetType: "table-like", Namespace: []string{"db", "schema"}, TargetName: "events"},
	}
	if err := kubeClient.Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	if err := kubeClient.Delete(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("blocked delete reconcile: %v", err)
	}
	if deleted {
		t.Fatal("policy was deleted before its managed binding was removed")
	}
	if err := kubeClient.Delete(ctx, binding); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("final delete reconcile: %v", err)
	}
	if !deleted {
		t.Fatal("policy delete was not sent to Polaris after bindings were removed")
	}
}

func TestPolarisPolicyUpdateRetriesWithFreshVersion(t *testing.T) {
	ctx := context.Background()
	const policyPath = "/api/catalog/polaris/v1/warehouse/namespaces/db/policies/retention-policy"
	var getCalls int
	var updateVersions []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.EscapedPath() != policyPath {
			t.Errorf("request path = %q", req.URL.EscapedPath())
		}
		switch req.Method {
		case http.MethodGet:
			getCalls++
			version := 4
			if getCalls > 1 {
				version = 5
			}
			_, _ = w.Write([]byte(fmt.Sprintf(
				`{"policy":{"policy-type":"system.retention","inheritable":false,"name":"retention-policy","description":"old","content":"{}","version":%d}}`,
				version,
			)))
		case http.MethodPut:
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode update body: %v", err)
			}
			version, ok := body["current-policy-version"].(float64)
			if !ok {
				t.Errorf("update body missing policy version: %#v", body)
			}
			updateVersions = append(updateVersions, int(version))
			if len(updateVersions) == 1 {
				w.WriteHeader(http.StatusConflict)
				return
			}
			_, _ = w.Write([]byte(`{"policy":{"policy-type":"system.retention","inheritable":false,"name":"retention-policy","description":"new","content":"{\"days\":30}","version":6}}`))
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
	catalog := &polarisv1alpha1.PolarisCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: "warehouse-resource", Namespace: "default"},
		Spec:       polarisv1alpha1.PolarisCatalogSpec{ConnectionRef: "remote", CatalogName: "warehouse"},
		Status:     polarisv1alpha1.PolarisCatalogStatus{Conditions: []metav1.Condition{ready}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("test-token")},
	}
	policy := &polarisv1alpha1.PolarisPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "retention-policy", Namespace: "default", Generation: 2,
			Finalizers: []string{polarisPolicyFinalizer},
		},
		Spec: polarisv1alpha1.PolarisPolicySpec{
			CatalogRef: catalog.Name, Namespace: []string{"db"}, PolicyName: "retention-policy",
			Type: "system.retention", Description: "new", Content: `{"days":30}`,
		},
		Status: polarisv1alpha1.PolarisPolicyStatus{
			Conditions: []metav1.Condition{ready}, ObservedCatalogRef: catalog.Name,
			ObservedCatalogName: "warehouse", ObservedConnectionRef: "remote",
			ObservedNamespace: []string{"db"}, ObservedPolicyName: "retention-policy", ObservedType: "system.retention",
		},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisPolicy{}, &polarisv1alpha1.PolarisPolicyBinding{}, &polarisv1alpha1.PolarisCatalog{}).
		WithObjects(connection, catalog, secret, policy).Build()
	reconciler := &PolarisPolicyReconciler{Client: kubeClient, Scheme: scheme}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}}

	for range 2 {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			t.Fatalf("update reconcile: %v", err)
		}
	}
	stored := &polarisv1alpha1.PolarisPolicy{}
	if err := kubeClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updateVersions, []int{4, 5}) {
		t.Fatalf("update versions = %v, want [4 5]", updateVersions)
	}
	if stored.Status.PolicyVersion != 6 || !meta.IsStatusConditionTrue(stored.Status.Conditions, "Ready") {
		t.Fatalf("policy status after version retry = %#v", stored.Status)
	}
}
