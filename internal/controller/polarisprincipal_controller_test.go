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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	principalTestBearerAuthorization = testBearerAuthorization
	principalTestManagementPath      = "/api/management/v1/principals/" + testAnalystPrincipalName
	principalTestGeneratedSecret     = "generated-secret"
	principalTestRemovedLabel        = "remove"
	principalTestTrueValue           = "true"
	principalTestPreserveValue       = "preserve"
	principalTestUpdatedValue        = "updated"
	principalTestValue               = "value"
)

func TestPolarisPrincipalCreatesAndExplicitlyRotatesCredentials(t *testing.T) {
	ctx := context.Background()
	var remote *polarisclient.Principal
	var rotations int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != principalTestBearerAuthorization {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.URL.Path == principalTestManagementPath && r.Method == http.MethodGet:
			if remote == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(remote)
		case r.URL.Path == principalTestManagementPath && r.Method == http.MethodPut:
			var body struct {
				CurrentEntityVersion int64             `json:"currentEntityVersion"`
				Properties           map[string]string `json:"properties"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode update request: %v", err)
			}
			if remote == nil || body.CurrentEntityVersion != remote.EntityVersion {
				http.Error(w, "version conflict", http.StatusConflict)
				return
			}
			remote.Properties = body.Properties
			remote.EntityVersion++
			_ = json.NewEncoder(w).Encode(remote)
		case r.URL.Path == principalTestManagementPath && r.Method == http.MethodDelete:
			remote = nil
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/management/v1/principals" && r.Method == http.MethodPost:
			var body struct {
				Principal polarisclient.Principal `json:"principal"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create request: %v", err)
			}
			body.Principal.EntityVersion = 1
			remote = &body.Principal
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(polarisclient.PrincipalWithCredentials{
				Principal:   *remote,
				Credentials: polarisclient.PrincipalCredentials{ClientID: testGeneratedClientID, ClientSecret: principalTestGeneratedSecret},
			})
		case r.URL.Path == principalTestManagementPath+"/rotate" && r.Method == http.MethodPost:
			rotations++
			remote.EntityVersion++
			_ = json.NewEncoder(w).Encode(polarisclient.PrincipalWithCredentials{
				Principal:   *remote,
				Credentials: polarisclient.PrincipalCredentials{ClientID: "rotated-id", ClientSecret: testRotatedSecretValue},
			})
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
	principal := &polarisv1alpha1.PolarisPrincipal{
		ObjectMeta: metav1.ObjectMeta{Name: testAnalystPrincipalName, Namespace: testDefaultNamespace},
		Spec: polarisv1alpha1.PolarisPrincipalSpec{
			ConnectionRef: testRemoteConnectionName, PrincipalName: testAnalystPrincipalName, CredentialsSecretName: testAnalystCredentialsSecret,
			CredentialsSecretTemplate: principalTestSecretTemplate(),
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisPrincipal{}).
		WithObjects(connection, authSecret, principal).Build()
	reconciler := &PolarisPrincipalReconciler{Client: k8sClient, Scheme: scheme}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: principal.Namespace, Name: principal.Name}}

	assertPrincipalCreation(t, ctx, k8sClient, reconciler, request, &remote, &rotations)
	assertPrincipalTemplateLifecycle(t, ctx, k8sClient, reconciler, request)
	assertPrincipalPropertiesUpdated(t, ctx, k8sClient, reconciler, request, &remote)
	assertPrincipalRotation(t, ctx, k8sClient, reconciler, request, &rotations)
	assertPrincipalTemplateRemoval(t, ctx, k8sClient, reconciler, request)
	assertPrincipalDeleted(t, ctx, k8sClient, reconciler, request, &remote, &rotations)
}

func assertPrincipalCreation(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	reconciler *PolarisPrincipalReconciler,
	request reconcile.Request,
	remote **polarisclient.Principal,
	rotations *int,
) {
	t.Helper()
	for range 2 {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			t.Fatalf("initial reconcile: %v", err)
		}
	}
	if *remote == nil || (*remote).Name != testAnalystPrincipalName {
		t.Fatalf("remote principal = %#v", *remote)
	}
	if *rotations != 0 {
		t.Fatalf("routine reconciliation rotated credentials %d times", *rotations)
	}
	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testDefaultNamespace, Name: testAnalystCredentialsSecret}, secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data[clientIDSecretKey]) != testGeneratedClientID || string(secret.Data[clientSecretSecretKey]) != principalTestGeneratedSecret {
		t.Fatalf("creation credentials were not persisted: %#v", secret.Data)
	}
	if secret.Labels["template-label"] != testOldTemplateValue || secret.Labels["removed-label"] != principalTestRemovedLabel ||
		secret.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed"] != principalTestTrueValue ||
		secret.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces"] != testAnalyticsNamespace {
		t.Fatalf("credentials Secret template was not applied at creation: labels=%v annotations=%v", secret.Labels, secret.Annotations)
	}
}

func assertPrincipalTemplateLifecycle(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	reconciler *PolarisPrincipalReconciler,
	request reconcile.Request,
) {
	t.Helper()
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{Namespace: testDefaultNamespace, Name: testAnalystCredentialsSecret}
	if err := k8sClient.Get(ctx, secretKey, secret); err != nil {
		t.Fatal(err)
	}
	secret.Labels = map[string]string{"external-label": principalTestPreserveValue}
	secret.Annotations = map[string]string{"external.example.com/keep": principalTestPreserveValue}
	if err := k8sClient.Update(ctx, secret); err != nil {
		t.Fatalf("add external Secret metadata: %v", err)
	}

	stored := &polarisv1alpha1.PolarisPrincipal{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.CredentialsSecretTemplate = principalTestSecretTemplate()
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatalf("set credentials Secret template: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("apply credentials Secret template: %v", err)
	}
	if err := k8sClient.Get(ctx, secretKey, secret); err != nil {
		t.Fatal(err)
	}
	if secret.Labels["template-label"] != testOldTemplateValue || secret.Labels["removed-label"] != principalTestRemovedLabel ||
		secret.Labels["external-label"] != principalTestPreserveValue ||
		secret.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed"] != principalTestTrueValue ||
		secret.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces"] != testAnalyticsNamespace ||
		secret.Annotations["external.example.com/keep"] != principalTestPreserveValue {
		t.Fatalf("credentials Secret template metadata was not applied without clobbering external metadata: labels=%v annotations=%v", secret.Labels, secret.Annotations)
	}

	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.CredentialsSecretTemplate.Metadata = &polarisv1alpha1.PolarisPrincipalSecretTemplateMetadata{
		Labels: map[string]string{"template-label": principalTestUpdatedValue},
		Annotations: map[string]string{
			"template.example.com/updated": principalTestValue,
		},
	}
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatalf("update credentials Secret template: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("reconcile updated credentials Secret template: %v", err)
	}
	if err := k8sClient.Get(ctx, secretKey, secret); err != nil {
		t.Fatal(err)
	}
	if secret.Labels["template-label"] != principalTestUpdatedValue || secret.Labels["removed-label"] != "" ||
		secret.Labels["external-label"] != principalTestPreserveValue ||
		secret.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed"] != "" ||
		secret.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces"] != "" ||
		secret.Annotations["template.example.com/removed"] != "" ||
		secret.Annotations["template.example.com/updated"] != principalTestValue ||
		secret.Annotations["external.example.com/keep"] != principalTestPreserveValue ||
		string(secret.Data[clientIDSecretKey]) != testGeneratedClientID ||
		string(secret.Data[clientSecretSecretKey]) != principalTestGeneratedSecret {
		t.Fatalf("credentials Secret template update did not preserve data and unrelated metadata while removing stale managed keys: labels=%v annotations=%v data=%v", secret.Labels, secret.Annotations, secret.Data)
	}
	owner := metav1.GetControllerOf(secret)
	if owner == nil || owner.Kind != "PolarisPrincipal" || owner.Name != testAnalystPrincipalName {
		t.Fatalf("credentials Secret controller owner = %#v", owner)
	}
}

func principalTestSecretTemplate() *polarisv1alpha1.PolarisPrincipalSecretTemplate {
	return &polarisv1alpha1.PolarisPrincipalSecretTemplate{
		Metadata: &polarisv1alpha1.PolarisPrincipalSecretTemplateMetadata{
			Labels: map[string]string{
				"template-label": testOldTemplateValue,
				"removed-label":  principalTestRemovedLabel,
			},
			Annotations: map[string]string{
				"reflector.v1.k8s.emberstack.com/reflection-allowed":            principalTestTrueValue,
				"reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces": testAnalyticsNamespace,
				"template.example.com/removed":                                  principalTestRemovedLabel,
			},
		},
	}
}

func assertPrincipalPropertiesUpdated(t *testing.T, ctx context.Context, k8sClient client.Client, reconciler *PolarisPrincipalReconciler, request reconcile.Request, remote **polarisclient.Principal) {
	t.Helper()
	stored := &polarisv1alpha1.PolarisPrincipal{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Properties = map[string]string{testDescriptionProperty: "updated identity"}
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("property update reconcile: %v", err)
	}
	if (*remote).Properties[testDescriptionProperty] != "updated identity" {
		t.Fatalf("updated remote properties = %#v", (*remote).Properties)
	}
}

func assertPrincipalRotation(t *testing.T, ctx context.Context, k8sClient client.Client, reconciler *PolarisPrincipalReconciler, request reconcile.Request, rotations *int) {
	t.Helper()
	stored := &polarisv1alpha1.PolarisPrincipal{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	rotationID := int64(1)
	stored.Spec.CredentialRotationRequestID = &rotationID
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("rotation reconcile: %v", err)
	}
	if *rotations != 1 {
		t.Fatalf("rotation requests = %d, want 1", *rotations)
	}
	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testDefaultNamespace, Name: testAnalystCredentialsSecret}, secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data[clientIDSecretKey]) != "rotated-id" || string(secret.Data[clientSecretSecretKey]) != testRotatedSecretValue {
		t.Fatalf("rotated credentials were not persisted: %#v", secret.Data)
	}
	if secret.Labels["template-label"] != principalTestUpdatedValue || secret.Labels["external-label"] != principalTestPreserveValue ||
		secret.Annotations["template.example.com/updated"] != principalTestValue ||
		secret.Annotations["external.example.com/keep"] != principalTestPreserveValue {
		t.Fatalf("credential rotation lost credentials Secret metadata: labels=%v annotations=%v", secret.Labels, secret.Annotations)
	}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.LastCredentialRotationRequestID != rotationID {
		t.Fatalf("last rotation ID = %d", stored.Status.LastCredentialRotationRequestID)
	}
	for _, condition := range stored.Status.Conditions {
		if condition.Message == principalTestGeneratedSecret || condition.Message == testRotatedSecretValue {
			t.Fatal("credential leaked into status")
		}
	}
}

func assertPrincipalTemplateRemoval(t *testing.T, ctx context.Context, k8sClient client.Client, reconciler *PolarisPrincipalReconciler, request reconcile.Request) {
	t.Helper()
	stored := &polarisv1alpha1.PolarisPrincipal{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.CredentialsSecretTemplate = nil
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatalf("remove credentials Secret template: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("reconcile removed credentials Secret template: %v", err)
	}
	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testDefaultNamespace, Name: testAnalystCredentialsSecret}, secret); err != nil {
		t.Fatal(err)
	}
	if secret.Labels["template-label"] != "" || secret.Labels["external-label"] != principalTestPreserveValue ||
		secret.Annotations["template.example.com/updated"] != "" ||
		secret.Annotations["external.example.com/keep"] != principalTestPreserveValue ||
		string(secret.Data[clientSecretSecretKey]) != testRotatedSecretValue {
		t.Fatalf("removing the template did not clear only template-managed metadata: labels=%v annotations=%v", secret.Labels, secret.Annotations)
	}
}

func assertPrincipalDeleted(t *testing.T, ctx context.Context, k8sClient client.Client, reconciler *PolarisPrincipalReconciler, request reconcile.Request, remote **polarisclient.Principal, rotations *int) {
	t.Helper()
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("steady-state reconcile after rotation: %v", err)
	}
	if *rotations != 1 {
		t.Fatalf("steady-state reconciliation retried rotation: %d", *rotations)
	}
	stored := &polarisv1alpha1.PolarisPrincipal{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Delete(ctx, stored); err != nil {
		t.Fatalf("delete principal resource: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}
	if *remote != nil {
		t.Fatalf("remote principal was not deleted: %#v", *remote)
	}
}
