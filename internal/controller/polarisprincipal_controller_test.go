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

func TestPolarisPrincipalCreatesAndExplicitlyRotatesCredentials(t *testing.T) {
	ctx := context.Background()
	var remote *polarisclient.Principal
	var rotations int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.URL.Path == "/api/management/v1/principals/analyst" && r.Method == http.MethodGet:
			if remote == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(remote)
		case r.URL.Path == "/api/management/v1/principals/analyst" && r.Method == http.MethodPut:
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
		case r.URL.Path == "/api/management/v1/principals/analyst" && r.Method == http.MethodDelete:
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
				Credentials: polarisclient.PrincipalCredentials{ClientID: "generated-id", ClientSecret: "generated-secret"},
			})
		case r.URL.Path == "/api/management/v1/principals/analyst/rotate" && r.Method == http.MethodPost:
			rotations++
			remote.EntityVersion++
			_ = json.NewEncoder(w).Encode(polarisclient.PrincipalWithCredentials{
				Principal:   *remote,
				Credentials: polarisclient.PrincipalCredentials{ClientID: "rotated-id", ClientSecret: "rotated-secret"},
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
		ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: "default"},
		Spec: polarisv1alpha1.PolarisConnectionSpec{
			ManagementURL: server.URL + "/api/management/v1",
			Auth: polarisv1alpha1.PolarisConnectionAuth{BearerTokenSecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "auth"}, Key: "token",
			}},
		},
	}
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"}, Data: map[string][]byte{"token": []byte("test-token")}}
	principal := &polarisv1alpha1.PolarisPrincipal{
		ObjectMeta: metav1.ObjectMeta{Name: "analyst", Namespace: "default"},
		Spec: polarisv1alpha1.PolarisPrincipalSpec{
			ConnectionRef: "remote", PrincipalName: "analyst", CredentialsSecretName: "analyst-auth",
			CredentialsSecretTemplate: &polarisv1alpha1.PolarisPrincipalSecretTemplate{
				Metadata: &polarisv1alpha1.PolarisPrincipalSecretTemplateMetadata{
					Labels: map[string]string{"template-label": "old", "removed-label": "remove"},
					Annotations: map[string]string{
						"reflector.v1.k8s.emberstack.com/reflection-allowed":            "true",
						"reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces": "analytics",
						"template.example.com/removed":                                  "remove",
					},
				},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisPrincipal{}).
		WithObjects(connection, authSecret, principal).Build()
	reconciler := &PolarisPrincipalReconciler{Client: k8sClient, Scheme: scheme}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: principal.Namespace, Name: principal.Name}}

	for range 2 {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			t.Fatalf("initial reconcile: %v", err)
		}
	}
	if remote == nil || remote.Name != "analyst" {
		t.Fatalf("remote principal = %#v", remote)
	}
	if rotations != 0 {
		t.Fatalf("routine reconciliation rotated credentials %d times", rotations)
	}
	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "analyst-auth"}, secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["client-id"]) != "generated-id" || string(secret.Data["client-secret"]) != "generated-secret" {
		t.Fatalf("creation credentials were not persisted: %#v", secret.Data)
	}
	if secret.Labels["template-label"] != "old" || secret.Labels["removed-label"] != "remove" ||
		secret.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed"] != "true" ||
		secret.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces"] != "analytics" {
		t.Fatalf("credentials Secret template was not applied at creation: labels=%v annotations=%v", secret.Labels, secret.Annotations)
	}

	secret.Labels = map[string]string{"external-label": "preserve"}
	secret.Annotations = map[string]string{"external.example.com/keep": "preserve"}
	if err := k8sClient.Update(ctx, secret); err != nil {
		t.Fatalf("add external Secret metadata: %v", err)
	}

	stored := &polarisv1alpha1.PolarisPrincipal{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.CredentialsSecretTemplate = &polarisv1alpha1.PolarisPrincipalSecretTemplate{
		Metadata: &polarisv1alpha1.PolarisPrincipalSecretTemplateMetadata{
			Labels: map[string]string{
				"template-label": "old",
				"removed-label":  "remove",
			},
			Annotations: map[string]string{
				"reflector.v1.k8s.emberstack.com/reflection-allowed":            "true",
				"reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces": "analytics",
				"template.example.com/removed":                                  "remove",
			},
		},
	}
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatalf("set credentials Secret template: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("apply credentials Secret template: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "analyst-auth"}, secret); err != nil {
		t.Fatal(err)
	}
	if secret.Labels["template-label"] != "old" || secret.Labels["removed-label"] != "remove" ||
		secret.Labels["external-label"] != "preserve" ||
		secret.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed"] != "true" ||
		secret.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces"] != "analytics" ||
		secret.Annotations["external.example.com/keep"] != "preserve" {
		t.Fatalf("credentials Secret template metadata was not applied without clobbering external metadata: labels=%v annotations=%v", secret.Labels, secret.Annotations)
	}

	stored = &polarisv1alpha1.PolarisPrincipal{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.CredentialsSecretTemplate.Metadata = &polarisv1alpha1.PolarisPrincipalSecretTemplateMetadata{
		Labels: map[string]string{"template-label": "updated"},
		Annotations: map[string]string{
			"template.example.com/updated": "value",
		},
	}
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatalf("update credentials Secret template: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("reconcile updated credentials Secret template: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "analyst-auth"}, secret); err != nil {
		t.Fatal(err)
	}
	if secret.Labels["template-label"] != "updated" || secret.Labels["removed-label"] != "" ||
		secret.Labels["external-label"] != "preserve" ||
		secret.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed"] != "" ||
		secret.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces"] != "" ||
		secret.Annotations["template.example.com/removed"] != "" ||
		secret.Annotations["template.example.com/updated"] != "value" ||
		secret.Annotations["external.example.com/keep"] != "preserve" ||
		string(secret.Data["client-id"]) != "generated-id" ||
		string(secret.Data["client-secret"]) != "generated-secret" {
		t.Fatalf("credentials Secret template update did not preserve data and unrelated metadata while removing stale managed keys: labels=%v annotations=%v data=%v", secret.Labels, secret.Annotations, secret.Data)
	}
	owner := metav1.GetControllerOf(secret)
	if owner == nil || owner.Kind != "PolarisPrincipal" || owner.Name != principal.Name {
		t.Fatalf("credentials Secret controller owner = %#v", owner)
	}
	stored = &polarisv1alpha1.PolarisPrincipal{}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}

	stored.Spec.Properties = map[string]string{"description": "updated identity"}
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("property update reconcile: %v", err)
	}
	if remote.Properties["description"] != "updated identity" {
		t.Fatalf("updated remote properties = %#v", remote.Properties)
	}

	stored = &polarisv1alpha1.PolarisPrincipal{}
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
	if rotations != 1 {
		t.Fatalf("rotation requests = %d, want 1", rotations)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "analyst-auth"}, secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["client-id"]) != "rotated-id" || string(secret.Data["client-secret"]) != "rotated-secret" {
		t.Fatalf("rotated credentials were not persisted: %#v", secret.Data)
	}
	if secret.Labels["template-label"] != "updated" || secret.Labels["external-label"] != "preserve" ||
		secret.Annotations["template.example.com/updated"] != "value" ||
		secret.Annotations["external.example.com/keep"] != "preserve" {
		t.Fatalf("credential rotation lost credentials Secret metadata: labels=%v annotations=%v", secret.Labels, secret.Annotations)
	}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.LastCredentialRotationRequestID != rotationID {
		t.Fatalf("last rotation ID = %d", stored.Status.LastCredentialRotationRequestID)
	}
	for _, condition := range stored.Status.Conditions {
		if condition.Message == "generated-secret" || condition.Message == "rotated-secret" {
			t.Fatal("credential leaked into status")
		}
	}
	stored.Spec.CredentialsSecretTemplate = nil
	if err := k8sClient.Update(ctx, stored); err != nil {
		t.Fatalf("remove credentials Secret template: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("reconcile removed credentials Secret template: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "analyst-auth"}, secret); err != nil {
		t.Fatal(err)
	}
	if secret.Labels["template-label"] != "" || secret.Labels["external-label"] != "preserve" ||
		secret.Annotations["template.example.com/updated"] != "" ||
		secret.Annotations["external.example.com/keep"] != "preserve" ||
		string(secret.Data["client-secret"]) != "rotated-secret" {
		t.Fatalf("removing the template did not clear only template-managed metadata: labels=%v annotations=%v", secret.Labels, secret.Annotations)
	}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("steady-state reconcile after rotation: %v", err)
	}
	if rotations != 1 {
		t.Fatalf("steady-state reconciliation retried rotation: %d", rotations)
	}
	if err := k8sClient.Get(ctx, request.NamespacedName, stored); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Delete(ctx, stored); err != nil {
		t.Fatalf("delete principal resource: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}
	if remote != nil {
		t.Fatalf("remote principal was not deleted: %#v", remote)
	}
}
