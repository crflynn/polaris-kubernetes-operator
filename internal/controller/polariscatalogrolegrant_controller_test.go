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

const (
	catalogRoleGrantTestReadProperties      = "CATALOG_READ_PROPERTIES"
	catalogRoleGrantTestReady               = readyConditionType
	catalogRoleGrantTestBearerAuthorization = testBearerAuthorization
	catalogRoleGrantTestValid               = true
	catalogRoleGrantTestReadyMessage        = "ready"
	catalogRoleGrantTestSchema              = "schema"
	catalogRoleGrantTestTable               = "table"
	catalogRoleGrantTestSummary             = "summary"
	catalogRoleGrantTestRetention           = "retention"
	catalogRoleGrantTestCascadeTrue         = "true"
)

func TestPolarisCatalogRoleGrantLifecycleAndCascade(t *testing.T) {
	ctx := context.Background()
	var grants []polarisclient.Grant
	var addCalls, revokeCalls int
	const grantPath = "/api/management/v1/catalogs/warehouse/catalog-roles/writers/grants"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != catalogRoleGrantTestBearerAuthorization {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.URL.Path == grantPath && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"grants": grants})
		case r.URL.Path == grantPath && r.Method == http.MethodPut:
			var body struct {
				Grant polarisclient.Grant `json:"grant"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode add grant: %v", err)
			}
			if body.Grant.Type != testCatalogGrantType || body.Grant.Privilege != catalogRoleGrantTestReadProperties {
				t.Errorf("grant payload = %#v", body.Grant)
			}
			grants = append(grants, body.Grant)
			addCalls++
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == grantPath && r.Method == http.MethodPost:
			if r.URL.Query().Get("cascade") != catalogRoleGrantTestCascadeTrue {
				t.Errorf("cascade = %q", r.URL.Query().Get("cascade"))
			}
			var body struct {
				Grant polarisclient.Grant `json:"grant"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode revoke grant: %v", err)
			}
			if body.Grant.Type != testCatalogGrantType || body.Grant.Privilege != catalogRoleGrantTestReadProperties {
				t.Errorf("revoked grant payload = %#v", body.Grant)
			}
			grants = nil
			revokeCalls++
			w.WriteHeader(http.StatusCreated)
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
		ObjectMeta: metav1.ObjectMeta{Name: testRemoteConnectionName, Namespace: testDefaultNamespace},
		Spec: polarisv1alpha1.PolarisConnectionSpec{
			ManagementURL: server.URL + "/api/management/v1",
			Auth: polarisv1alpha1.PolarisConnectionAuth{BearerTokenSecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: testAuthSecretName}, Key: testTokenKey,
			}},
		},
	}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: testAuthSecretName, Namespace: testDefaultNamespace}, Data: map[string][]byte{testTokenKey: []byte("test-token")}}
	readyCondition := metav1.Condition{Type: catalogRoleGrantTestReady, Status: metav1.ConditionTrue, Reason: catalogRoleGrantTestReady, Message: catalogRoleGrantTestReadyMessage, LastTransitionTime: metav1.Now()}
	catalogRole := &polarisv1alpha1.PolarisCatalogRole{
		ObjectMeta: metav1.ObjectMeta{Name: testCatalogRoleWritersName, Namespace: testDefaultNamespace},
		Spec:       polarisv1alpha1.PolarisCatalogRoleSpec{CatalogRef: testWarehouseCatalogName, CatalogRoleName: testCatalogRoleWritersName},
		Status: polarisv1alpha1.PolarisCatalogRoleStatus{
			Conditions: []metav1.Condition{readyCondition}, ObservedCatalogName: testWarehouseCatalogName,
			ObservedRoleName: testCatalogRoleWritersName, ObservedConnectionRef: testRemoteConnectionName,
		},
	}
	grant := &polarisv1alpha1.PolarisCatalogRoleGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "writers-catalog-read-properties", Namespace: testDefaultNamespace},
		Spec: polarisv1alpha1.PolarisCatalogRoleGrantSpec{
			CatalogRoleRef: catalogRole.Name,
			Grant:          polarisv1alpha1.PolarisGrantSpec{Type: testCatalogGrantType, Privilege: catalogRoleGrantTestReadProperties},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisCatalogRoleGrant{}).
		WithObjects(connection, authSecret, catalogRole, grant).Build()
	reconciler := &PolarisCatalogRoleGrantReconciler{Client: k8sClient, Scheme: scheme}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: grant.Namespace, Name: grant.Name}}

	for range 3 {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			t.Fatalf("grant reconcile: %v", err)
		}
	}
	if addCalls != 1 || len(grants) != 1 {
		t.Fatalf("grant additions=%d grants=%#v", addCalls, grants)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("steady-state reconcile: %v", err)
	}
	if addCalls != 1 {
		t.Fatalf("steady-state reconciliation repeated grant: %d", addCalls)
	}

	stored := &polarisv1alpha1.PolarisCatalogRoleGrant{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Cascade = true
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Delete(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("grant revoke reconcile: %v", err)
	}
	if revokeCalls != 1 || len(grants) != 0 {
		t.Fatalf("grant revocations=%d grants=%#v", revokeCalls, grants)
	}
}
func TestValidateGrantScopeFields(t *testing.T) {
	tests := []struct {
		name  string
		grant polarisv1alpha1.PolarisGrantSpec
		valid bool
	}{
		{name: testCatalogGrantType, grant: polarisv1alpha1.PolarisGrantSpec{Type: testCatalogGrantType, Privilege: catalogRoleGrantTestReadProperties}, valid: catalogRoleGrantTestValid},
		{name: testNamespaceGrantType, grant: polarisv1alpha1.PolarisGrantSpec{Type: testNamespaceGrantType, Namespace: []string{"db", catalogRoleGrantTestSchema}, Privilege: "NAMESPACE_LIST"}, valid: catalogRoleGrantTestValid},
		{name: catalogRoleGrantTestTable, grant: polarisv1alpha1.PolarisGrantSpec{Type: catalogRoleGrantTestTable, Namespace: []string{"db"}, TableName: testEventsTableName, Privilege: "TABLE_READ_DATA"}, valid: catalogRoleGrantTestValid},
		{name: "view", grant: polarisv1alpha1.PolarisGrantSpec{Type: "view", Namespace: []string{"db"}, ViewName: catalogRoleGrantTestSummary, Privilege: "VIEW_LIST"}, valid: catalogRoleGrantTestValid},
		{name: "policy", grant: polarisv1alpha1.PolarisGrantSpec{Type: "policy", Namespace: []string{"db"}, PolicyName: catalogRoleGrantTestRetention, Privilege: "POLICY_READ"}, valid: catalogRoleGrantTestValid},
		{name: "namespace segment empty", grant: polarisv1alpha1.PolarisGrantSpec{Type: testNamespaceGrantType, Namespace: []string{"db", ""}, Privilege: "NAMESPACE_LIST"}},
		{name: "table name missing", grant: polarisv1alpha1.PolarisGrantSpec{Type: catalogRoleGrantTestTable, Namespace: []string{"db"}, Privilege: "TABLE_READ_DATA"}},
		{name: "wrong target field", grant: polarisv1alpha1.PolarisGrantSpec{Type: testCatalogGrantType, TableName: testEventsTableName, Privilege: catalogRoleGrantTestReadProperties}},
		{name: "unknown type", grant: polarisv1alpha1.PolarisGrantSpec{Type: "semantic-model", Namespace: []string{"db"}, Privilege: "SEMANTIC_MODEL_READ"}},
		{name: "privilege missing", grant: polarisv1alpha1.PolarisGrantSpec{Type: testCatalogGrantType}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateGrant(test.grant)
			if test.valid && err != nil {
				t.Fatalf("validateGrant() error = %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("validateGrant() accepted an invalid grant")
			}
		})
	}
}
