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
	"testing"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
	polarisclient "github.com/apache/polaris-kubernetes-operator/internal/polaris"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const connectionTestPolarisResourceName = "polaris"
const (
	connectionTestOAuth                 = "OAUTH"
	connectionTestBearer                = "BEARER"
	connectionTestRemoteUnavailable     = "RemoteUnavailable"
	connectionTestCredentialsSecretName = "credentials"
	connectionTestNamespace             = "team-a"
	connectionTestOtherNamespace        = "team-b"
	connectionTestCABundleSecretName    = "polaris-ca"
)

func TestConnectionSecretReferences(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: connectionTestCredentialsSecretName, Namespace: connectionTestNamespace}}
	credentialAuth := polarisv1alpha1.PolarisConnectionAuth{
		ClientCredentials: &polarisv1alpha1.PolarisClientCredentials{SecretRef: corev1.LocalObjectReference{Name: connectionTestCredentialsSecretName}},
	}
	bearerAuth := polarisv1alpha1.PolarisConnectionAuth{
		BearerTokenSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: connectionTestCredentialsSecretName}, Key: testTokenKey},
	}
	caTLS := &polarisv1alpha1.PolarisTLSConfig{
		CASecretRef: corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: connectionTestCredentialsSecretName}, Key: "ca.crt"},
	}

	tests := []struct {
		name       string
		connection *polarisv1alpha1.PolarisConnection
		secret     *corev1.Secret
		want       bool
	}{
		{
			name: "client credentials in the connection namespace",
			connection: &polarisv1alpha1.PolarisConnection{
				ObjectMeta: metav1.ObjectMeta{Namespace: connectionTestNamespace}, Spec: polarisv1alpha1.PolarisConnectionSpec{Auth: credentialAuth},
			},
			secret: secret,
			want:   true,
		},
		{
			name: "bearer token in the connection namespace",
			connection: &polarisv1alpha1.PolarisConnection{
				ObjectMeta: metav1.ObjectMeta{Namespace: connectionTestNamespace}, Spec: polarisv1alpha1.PolarisConnectionSpec{Auth: bearerAuth},
			},
			secret: secret,
			want:   true,
		},
		{
			name: "CA bundle in the connection namespace",
			connection: &polarisv1alpha1.PolarisConnection{
				ObjectMeta: metav1.ObjectMeta{Namespace: connectionTestNamespace}, Spec: polarisv1alpha1.PolarisConnectionSpec{TLS: caTLS},
			},
			secret: secret,
			want:   true,
		},
		{
			name: "unreferenced Secret",
			connection: &polarisv1alpha1.PolarisConnection{
				ObjectMeta: metav1.ObjectMeta{Namespace: connectionTestNamespace}, Spec: polarisv1alpha1.PolarisConnectionSpec{Auth: bearerAuth},
			},
			secret: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: connectionTestNamespace}},
		},
		{
			name: "same Secret name in another namespace",
			connection: &polarisv1alpha1.PolarisConnection{
				ObjectMeta: metav1.ObjectMeta{Namespace: connectionTestNamespace}, Spec: polarisv1alpha1.PolarisConnectionSpec{Auth: bearerAuth},
			},
			secret: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: connectionTestCredentialsSecretName, Namespace: connectionTestOtherNamespace}},
		},
	}

	reconciler := &PolarisConnectionReconciler{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reconciler.referencesSecret(tt.connection, tt.secret); got != tt.want {
				t.Fatalf("referencesSecret() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestCatalogSecretReferences(t *testing.T) {
	oauth := &polarisv1alpha1.PolarisCatalog{
		ObjectMeta: metav1.ObjectMeta{Namespace: connectionTestNamespace},
		Spec: polarisv1alpha1.PolarisCatalogSpec{ExternalCatalog: &polarisv1alpha1.PolarisExternalCatalogConfig{
			Authentication: polarisv1alpha1.PolarisExternalCatalogAuthentication{
				Type: connectionTestOAuth, ClientCredentials: &polarisv1alpha1.PolarisExternalCatalogClientCredentials{
					SecretRef: corev1.LocalObjectReference{Name: connectionTestCredentialsSecretName},
				},
			},
		}},
	}
	bearer := &polarisv1alpha1.PolarisCatalog{
		ObjectMeta: metav1.ObjectMeta{Namespace: connectionTestNamespace},
		Spec: polarisv1alpha1.PolarisCatalogSpec{ExternalCatalog: &polarisv1alpha1.PolarisExternalCatalogConfig{
			Authentication: polarisv1alpha1.PolarisExternalCatalogAuthentication{
				Type: connectionTestBearer, BearerTokenSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: connectionTestCredentialsSecretName}, Key: "token"},
			},
		}},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: connectionTestCredentialsSecretName, Namespace: connectionTestNamespace}}

	tests := []struct {
		name    string
		catalog *polarisv1alpha1.PolarisCatalog
		secret  *corev1.Secret
		want    bool
	}{
		{name: "OAuth client credentials", catalog: oauth, secret: secret, want: true},
		{name: "bearer token", catalog: bearer, secret: secret, want: true},
		{name: "different Secret", catalog: bearer, secret: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: connectionTestNamespace}}},
		{name: "different namespace", catalog: bearer, secret: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: connectionTestCredentialsSecretName, Namespace: connectionTestOtherNamespace}}},
		{name: "internal catalog", catalog: &polarisv1alpha1.PolarisCatalog{ObjectMeta: metav1.ObjectMeta{Namespace: connectionTestNamespace}}, secret: secret},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := catalogReferencesSecret(tt.catalog, tt.secret); got != tt.want {
				t.Fatalf("catalogReferencesSecret() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestReadCABundleFromReferencedSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: connectionTestCABundleSecretName, Namespace: connectionTestNamespace},
		Data:       map[string][]byte{"bundle.pem": []byte("test CA bundle")},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	reconciler := &PolarisConnectionReconciler{Client: kubeClient}

	connection := &polarisv1alpha1.PolarisConnection{ObjectMeta: metav1.ObjectMeta{Namespace: connectionTestNamespace}}
	bundle, err := reconciler.readCABundle(context.Background(), connection)
	if err != nil || bundle != nil {
		t.Fatalf("readCABundle() without TLS = %q, %v", bundle, err)
	}
	connection.Spec.TLS = &polarisv1alpha1.PolarisTLSConfig{
		CASecretRef: corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: connectionTestCABundleSecretName}, Key: "bundle.pem"},
	}
	bundle, err = reconciler.readCABundle(context.Background(), connection)
	if err != nil || string(bundle) != "test CA bundle" {
		t.Fatalf("readCABundle() = %q, %v", bundle, err)
	}

	connection.Spec.TLS.CASecretRef.Name = "missing"
	if _, err := reconciler.readCABundle(context.Background(), connection); err == nil {
		t.Fatal("readCABundle() succeeded for a missing Secret")
	}
	connection.Spec.TLS.CASecretRef.Name = connectionTestCABundleSecretName
	connection.Spec.TLS.CASecretRef.Key = "missing.pem"
	if _, err := reconciler.readCABundle(context.Background(), connection); err == nil {
		t.Fatal("readCABundle() succeeded for a missing CA key")
	}
}

func TestConnectionFailureStatusIsRecorded(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := polarisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	connection := &polarisv1alpha1.PolarisConnection{
		ObjectMeta: metav1.ObjectMeta{Name: connectionTestPolarisResourceName, Namespace: connectionTestNamespace, Generation: 4},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(connection).
		WithObjects(connection).Build()
	reconciler := &PolarisConnectionReconciler{Client: kubeClient}
	failure := &polarisclient.HTTPError{Operation: "management API", StatusCode: 401}
	if err := reconciler.setConnectionFailure(ctx, connection, failure); err != nil {
		t.Fatalf("setConnectionFailure() error = %v", err)
	}

	stored := &polarisv1alpha1.PolarisConnection{}
	key := types.NamespacedName{Namespace: connection.Namespace, Name: connection.Name}
	if err := kubeClient.Get(ctx, key, stored); err != nil {
		t.Fatal(err)
	}
	ready := meta.FindStatusCondition(stored.Status.Conditions, polarisv1alpha1.PolarisConnectionReadyConditionType)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "AuthenticationFailed" || ready.Message != failure.Error() {
		t.Fatalf("Ready condition = %#v", ready)
	}
	if stored.Status.ObservedGeneration != connection.Generation || stored.Status.LastCheckedTime == nil {
		t.Fatalf("failure timestamps/generation not recorded: %#v", stored.Status)
	}
}

func TestConnectionFailureReasonClassifiesHTTPStatuses(t *testing.T) {
	tests := []struct {
		name string
		code int
		want string
	}{
		{name: "unauthorized", code: 401, want: "AuthenticationFailed"},
		{name: "forbidden", code: 403, want: "AuthorizationFailed"},
		{name: "bad request", code: 400, want: "InvalidRemoteRequest"},
		{name: "server failure", code: 503, want: connectionTestRemoteUnavailable},
		{name: "other response", code: 404, want: "InvalidConfigurationOrUnavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &polarisclient.HTTPError{Operation: "catalog API", StatusCode: tt.code}
			if got := connectionFailureReason(err); got != tt.want {
				t.Fatalf("connectionFailureReason() = %q, want %q", got, tt.want)
			}
		})
	}
}
