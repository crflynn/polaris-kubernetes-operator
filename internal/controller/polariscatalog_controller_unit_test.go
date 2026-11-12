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
	"strings"
	"testing"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
	polarisclient "github.com/apache/polaris-kubernetes-operator/internal/polaris"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	testBearerAuthorization          = "Bearer test-token"
	testCatalogReady                 = readyConditionType
	testAuthTypeOAuth                = "OAUTH"
	testDefaultNamespace             = "default"
	testAuthSecretName               = "auth"
	testAnalyticsOwner               = "analytics"
	testOtherConfigValue             = "other"
	testCatalogScope                 = catalogGrantType
	testGeneratedClientID            = "generated-id"
	testAnalystPrincipalName         = "analyst"
	testAnalystCredentialsSecret     = "analyst-auth"
	testOldTemplateValue             = "old"
	testAnalyticsNamespace           = testAnalyticsOwner
	testDescriptionProperty          = "description"
	testAnalystRoleName              = "analysts"
	testMetadataLocationProperty     = "metadataLocation"
	testDesiredValue                 = "desired"
	testDeltaTableFormat             = "delta"
	testCatalogGrantType             = catalogGrantType
	testNamespaceGrantType           = namespaceGrantType
	testEventsTableName              = "events"
	testAnalyticsReadersRoleName     = "analytics-readers"
	testRemoteConnectionName         = "remote"
	testTokenKey                     = "token"
	testWarehouseCatalogName         = "warehouse"
	testOwnerProperty                = "owner"
	testRotatedSecretValue           = "rotated-secret"
	testWarehouseCatalogResourceName = "warehouse-resource"
	testWarehouseWritersRoleName     = "warehouse-writers"
	testCatalogRoleWritersName       = "writers"
	testWriteAccessDescription       = "write access"
)

type catalogCreateRequest struct {
	Catalog polarisclient.Catalog `json:"catalog"`
}

func TestPolarisCatalogReconcileCreatesAndUpdatesCatalog(t *testing.T) {
	ctx := context.Background()
	var remote *polarisclient.Catalog
	var updates int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != testBearerAuthorization {
			t.Errorf("Authorization = %q, want bearer token", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/api/management/v1/catalogs" && r.URL.Path != "/api/management/v1/catalogs/warehouse" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			if remote == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(remote)
		case http.MethodPost:
			var body catalogCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			body.Catalog.EntityVersion = 1
			remote = &body.Catalog
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(remote)
		case http.MethodPut:
			var body struct {
				CurrentEntityVersion int64             `json:"currentEntityVersion"`
				Properties           map[string]string `json:"properties"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode update request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if remote == nil || body.CurrentEntityVersion != remote.EntityVersion {
				http.Error(w, "version conflict", http.StatusConflict)
				return
			}
			remote.Properties = body.Properties
			remote.EntityVersion++
			updates++
			_ = json.NewEncoder(w).Encode(remote)
		case http.MethodDelete:
			remote = nil
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testAuthSecretName, Namespace: testDefaultNamespace},
		Data:       map[string][]byte{testTokenKey: []byte("test-token")},
	}
	catalog := &polarisv1alpha1.PolarisCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: testWarehouseCatalogResourceName, Namespace: testDefaultNamespace},
		Spec: polarisv1alpha1.PolarisCatalogSpec{
			ConnectionRef: testRemoteConnectionName, CatalogName: testWarehouseCatalogName, Type: "INTERNAL",
			Properties:        map[string]string{"default-base-location": "s3://bucket/warehouse/", testOwnerProperty: testAnalyticsOwner},
			StorageConfigInfo: polarisv1alpha1.PolarisStorageConfigInfo{StorageType: "S3", AllowedLocations: []string{"s3://bucket/warehouse/"}},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisCatalog{}).
		WithObjects(connection, secret, catalog).Build()
	reconciler := &PolarisCatalogReconciler{Client: k8sClient, Scheme: scheme}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: catalog.Namespace, Name: catalog.Name}}

	assertCatalogCreationAndSteadyState(t, ctx, reconciler, request, &remote, &updates)
	assertCatalogUpdate(t, ctx, k8sClient, reconciler, request, &remote, &updates)
	assertCatalogDelete(t, ctx, k8sClient, reconciler, request, &remote)
}

func assertCatalogCreationAndSteadyState(t *testing.T, ctx context.Context, reconciler *PolarisCatalogReconciler, request reconcile.Request, remote **polarisclient.Catalog, updates *int) {
	t.Helper()
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("create reconcile: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("create after finalizer reconcile: %v", err)
	}
	if *remote == nil || (*remote).Name != testWarehouseCatalogName || (*remote).Properties[testOwnerProperty] != testAnalyticsOwner {
		t.Fatalf("remote catalog after create = %#v", *remote)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("steady-state reconcile: %v", err)
	}
	if *updates != 0 {
		t.Fatalf("steady-state reconcile issued %d update requests, want 0", *updates)
	}
}

func assertCatalogUpdate(t *testing.T, ctx context.Context, k8sClient client.Client, reconciler *PolarisCatalogReconciler, request reconcile.Request, remote **polarisclient.Catalog, updates *int) {
	t.Helper()
	stored := &polarisv1alpha1.PolarisCatalog{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Properties[testOwnerProperty] = "platform"
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("update reconcile: %v", err)
	}
	if *updates != 1 || (*remote).Properties[testOwnerProperty] != "platform" || (*remote).EntityVersion != 2 {
		t.Fatalf("update result: updates=%d remote=%#v", *updates, *remote)
	}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.EntityVersion != 2 || stored.Status.ObservedGeneration != stored.Generation {
		t.Fatalf("catalog status does not reflect successful update: %#v", stored.Status)
	}
}

func assertCatalogDelete(t *testing.T, ctx context.Context, k8sClient client.Client, reconciler *PolarisCatalogReconciler, request reconcile.Request, remote **polarisclient.Catalog) {
	t.Helper()
	stored := &polarisv1alpha1.PolarisCatalog{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Delete(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}
	if *remote != nil {
		t.Fatalf("remote catalog was not deleted: %#v", *remote)
	}
}

func TestExternalCatalogUsesSecretCredentialsAndRejectsSecretRotation(t *testing.T) {
	ctx := context.Background()
	var remote *polarisclient.Catalog
	var submittedSecret string
	var updates int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if remote == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(remote)
		case http.MethodPost:
			var request catalogCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode create request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			submittedSecret = request.Catalog.ConnectionConfigInfo.AuthenticationParameters.ClientSecret
			request.Catalog.EntityVersion = 1
			request.Catalog.ConnectionConfigInfo.AuthenticationParameters.ClientSecret = ""
			remote = &request.Catalog
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(remote)
		case http.MethodPut:
			updates++
			http.Error(w, "external connection config must not be updated", http.StatusBadRequest)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
				LocalObjectReference: corev1.LocalObjectReference{Name: "polaris-auth"}, Key: testTokenKey,
			}},
		},
	}
	managementSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "polaris-auth", Namespace: testDefaultNamespace},
		Data:       map[string][]byte{testTokenKey: []byte("polaris-token")},
	}
	externalSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-catalog-auth", Namespace: testDefaultNamespace, ResourceVersion: "1"},
		Data: map[string][]byte{
			clientIDSecretKey:     []byte("remote-client"),
			clientSecretSecretKey: []byte("remote-secret"),
			"client-secret-alt":   []byte("remote-secret"),
		},
	}
	catalog := &polarisv1alpha1.PolarisCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: "external-warehouse", Namespace: testDefaultNamespace},
		Spec: polarisv1alpha1.PolarisCatalogSpec{
			ConnectionRef: testRemoteConnectionName, CatalogName: "external-warehouse", Type: "EXTERNAL",
			ExternalCatalog: &polarisv1alpha1.PolarisExternalCatalogConfig{
				URI: "https://iceberg.example.com/catalog",
				Authentication: polarisv1alpha1.PolarisExternalCatalogAuthentication{
					Type: testAuthTypeOAuth,
					ClientCredentials: &polarisv1alpha1.PolarisExternalCatalogClientCredentials{
						SecretRef: corev1.LocalObjectReference{Name: externalSecret.Name},
						TokenURL:  "https://iceberg.example.com/oauth/token",
					},
				},
			},
			Properties: map[string]string{"default-base-location": "s3://warehouse/external/"},
			StorageConfigInfo: polarisv1alpha1.PolarisStorageConfigInfo{
				StorageType: "S3", StorageName: "warehouse-storage",
			},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisCatalog{}).
		WithObjects(connection, managementSecret, externalSecret, catalog).Build()
	reconciler := &PolarisCatalogReconciler{Client: k8sClient, Scheme: scheme}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: catalog.Namespace, Name: catalog.Name}}

	assertExternalCatalogCreation(t, ctx, k8sClient, reconciler, request, &remote, &submittedSecret, externalSecret.Name)
	assertExternalCatalogAuthReference(t, ctx, k8sClient, reconciler, request)
	assertExternalCatalogSecretRotation(t, ctx, k8sClient, reconciler, request, externalSecret.Name, &updates)
}

func assertExternalCatalogCreation(t *testing.T, ctx context.Context, k8sClient client.Client, reconciler *PolarisCatalogReconciler, request reconcile.Request, remote **polarisclient.Catalog, submittedSecret *string, secretName string) {
	t.Helper()
	for range 2 {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			t.Fatalf("external catalog create reconcile: %v", err)
		}
	}
	if *remote == nil || (*remote).Type != "EXTERNAL" || *submittedSecret != "remote-secret" {
		t.Fatalf("external create request missing expected catalog/auth data: remote=%#v", *remote)
	}
	stored := &polarisv1alpha1.PolarisCatalog{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.AuthenticationSecretName != secretName ||
		stored.Status.AuthenticationSecretResourceVersion != "1" {
		t.Fatalf("authentication Secret provenance was not recorded: %#v", stored.Status)
	}
	if strings.Contains(meta.FindStatusCondition(stored.Status.Conditions, testCatalogReady).Message, "remote-secret") {
		t.Fatal("external credential leaked into catalog status")
	}
}

func assertExternalCatalogAuthReference(t *testing.T, ctx context.Context, k8sClient client.Client, reconciler *PolarisCatalogReconciler, request reconcile.Request) {
	t.Helper()
	stored := &polarisv1alpha1.PolarisCatalog{}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("external steady-state reconcile: %v", err)
	}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.ExternalCatalog.Authentication.ClientCredentials.ClientSecretKey = "client-secret-alt"
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("external auth reference change reconcile: %v", err)
	}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	ready := meta.FindStatusCondition(stored.Status.Conditions, testCatalogReady)
	if ready == nil || ready.Status != metav1.ConditionFalse {
		t.Fatalf("changed auth key reference was not reported: %#v", ready)
	}

	stored.Spec.ExternalCatalog.Authentication.ClientCredentials.ClientSecretKey = ""
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("restored auth reference reconcile: %v", err)
	}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	ready = meta.FindStatusCondition(stored.Status.Conditions, testCatalogReady)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("restoring the original auth reference did not recover readiness: %#v", ready)
	}
}

func assertExternalCatalogSecretRotation(t *testing.T, ctx context.Context, k8sClient client.Client, reconciler *PolarisCatalogReconciler, request reconcile.Request, secretName string, updates *int) {
	t.Helper()
	rotatedSecret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testDefaultNamespace, Name: secretName}, rotatedSecret); err != nil {
		t.Fatal(err)
	}
	rotatedSecret.Data[clientSecretSecretKey] = []byte(testRotatedSecretValue)
	if err := k8sClient.Update(ctx, rotatedSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("external Secret rotation reconcile: %v", err)
	}
	if *updates != 0 {
		t.Fatalf("external credential change issued %d unsupported update requests", *updates)
	}
	stored := &polarisv1alpha1.PolarisCatalog{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	ready := meta.FindStatusCondition(stored.Status.Conditions, testCatalogReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "ReconcileFailed" {
		t.Fatalf("external credential change did not report degraded readiness: %#v", ready)
	}
}

func TestExternalBearerTokenIsLoadedFromReferencedSecret(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-bearer", Namespace: testDefaultNamespace, ResourceVersion: "7"},
		Data:       map[string][]byte{testTokenKey: []byte("bearer-secret")},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	reconciler := &PolarisCatalogReconciler{Client: k8sClient}
	catalog := &polarisv1alpha1.PolarisCatalog{
		ObjectMeta: metav1.ObjectMeta{Name: "external", Namespace: testDefaultNamespace},
		Spec: polarisv1alpha1.PolarisCatalogSpec{
			ExternalCatalog: &polarisv1alpha1.PolarisExternalCatalogConfig{
				URI: "https://iceberg.example.com/catalog",
				Authentication: polarisv1alpha1.PolarisExternalCatalogAuthentication{
					Type: connectionTestBearer,
					BearerTokenSecretRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: secret.Name},
						Key:                  testTokenKey,
					},
				},
			},
		},
	}
	config, secretName, resourceVersion, err := reconciler.externalConnectionConfig(ctx, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if config.AuthenticationParameters.AuthenticationType != connectionTestBearer ||
		config.AuthenticationParameters.BearerToken != "bearer-secret" ||
		secretName != secret.Name || resourceVersion != "7" {
		t.Fatalf("unexpected bearer auth resolution: type=%q secretName=%q resourceVersion=%q", config.AuthenticationParameters.AuthenticationType, secretName, resourceVersion)
	}
}

func TestExternalConnectionMatchesDetectsConfigurationDrift(t *testing.T) {
	newConfig := func() *polarisclient.ExternalConnectionConfig {
		return &polarisclient.ExternalConnectionConfig{
			ConnectionType:    "ICEBERG_REST",
			URI:               "https://iceberg.example/catalog",
			RemoteCatalogName: "remote-warehouse",
			Properties:        map[string]string{testWarehouseCatalogName: "s3://bucket/warehouse"},
			AuthenticationParameters: polarisclient.ExternalAuthentication{
				AuthenticationType: "OAUTH",
				TokenURI:           "https://identity.example/token",
				ClientID:           clientIDSecretKey,
				ClientSecret:       clientSecretSecretKey,
				Scopes:             []string{testCatalogScope, "tables"},
			},
		}
	}
	tests := []struct {
		name   string
		change func(*polarisclient.ExternalConnectionConfig)
		want   bool
	}{
		{name: "identical configuration", want: true},
		{name: "connection type changed", change: func(config *polarisclient.ExternalConnectionConfig) { config.ConnectionType = "OTHER" }},
		{name: "URI changed", change: func(config *polarisclient.ExternalConnectionConfig) { config.URI += "/" + testOtherConfigValue }},
		{name: "remote catalog changed", change: func(config *polarisclient.ExternalConnectionConfig) { config.RemoteCatalogName = testOtherConfigValue }},
		{name: "properties changed", change: func(config *polarisclient.ExternalConnectionConfig) {
			config.Properties["warehouse"] = "s3://" + testOtherConfigValue
		}},
		{name: "authentication type changed", change: func(config *polarisclient.ExternalConnectionConfig) {
			config.AuthenticationParameters.AuthenticationType = connectionTestBearer
		}},
		{name: "OAuth token URI changed", change: func(config *polarisclient.ExternalConnectionConfig) {
			config.AuthenticationParameters.TokenURI += "/" + testOtherConfigValue
		}},
		{name: "OAuth client ID changed", change: func(config *polarisclient.ExternalConnectionConfig) {
			config.AuthenticationParameters.ClientID = testOtherConfigValue
		}},
		{name: "OAuth scopes changed", change: func(config *polarisclient.ExternalConnectionConfig) {
			config.AuthenticationParameters.Scopes = []string{testCatalogScope}
		}},
		{
			name: "remote OAuth secret is not returned by Polaris",
			change: func(config *polarisclient.ExternalConnectionConfig) {
				config.AuthenticationParameters.ClientSecret = testRotatedSecretValue
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote, desired := newConfig(), newConfig()
			if tt.change != nil {
				tt.change(desired)
			}
			if got := externalConnectionMatches(remote, desired); got != tt.want {
				t.Fatalf("externalConnectionMatches() = %t, want %t", got, tt.want)
			}
		})
	}

	if !externalConnectionMatches(nil, nil) {
		t.Fatal("two absent connections should match")
	}
	if externalConnectionMatches(nil, newConfig()) {
		t.Fatal("an absent remote connection should not match a desired connection")
	}
}
