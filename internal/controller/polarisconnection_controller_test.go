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
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
)

var _ = Describe("PolarisConnection Controller", func() {
	It("marks a connection ready after checking management and catalog APIs", func() {
		ctx := context.Background()
		namespace := testDefaultNamespace
		name := "test-connection"

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth/token":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"test-token"}`))
			case "/api/management/v1/catalogs":
				if got := r.Header.Get("Authorization"); got != testBearerAuthorization {
					http.Error(w, "unexpected authorization", http.StatusUnauthorized)
					return
				}
				w.WriteHeader(http.StatusOK)
			case "/api/catalog/v1/config":
				if got := r.Header.Get("Authorization"); got != testBearerAuthorization {
					http.Error(w, "unexpected authorization", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"endpoints":["GET /v1/{prefix}/namespaces"]}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "polaris-admin", Namespace: namespace},
			Data: map[string][]byte{
				clientIDSecretKey:     []byte(clientIDSecretKey),
				clientSecretSecretKey: []byte(clientSecretSecretKey),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		defer func() {
			_ = k8sClient.Delete(ctx, secret)
		}()

		connection := &polarisv1alpha1.PolarisConnection{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: polarisv1alpha1.PolarisConnectionSpec{
				ManagementURL: server.URL + "/api/management/v1",
				CatalogURL:    server.URL + "/api/catalog",
				TokenURL:      server.URL + "/oauth/token",
				Auth: polarisv1alpha1.PolarisConnectionAuth{
					ClientCredentials: &polarisv1alpha1.PolarisClientCredentials{
						SecretRef: corev1.LocalObjectReference{Name: secret.Name},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, connection)).To(Succeed())
		defer func() {
			_ = k8sClient.Delete(ctx, connection)
		}()

		reconciler := &PolarisConnectionReconciler{Client: k8sClient}
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		}})
		Expect(err).NotTo(HaveOccurred())

		updated := &polarisv1alpha1.PolarisConnection{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, updated)).To(Succeed())
		condition := meta.FindStatusCondition(updated.Status.Conditions, polarisv1alpha1.PolarisConnectionReadyConditionType)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		Expect(updated.Status.Capabilities).To(ConsistOf("GET /v1/{prefix}/namespaces"))
	})

})
