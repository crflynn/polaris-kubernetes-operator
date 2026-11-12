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
	"errors"
	"testing"
	"time"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const principalStatusTestCredentialsAvailable = "CredentialsAvailable"
const principalStatusTestNamespace = "team-a"

func TestPrincipalFailureUpdatesConditionsAndRequeues(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := polarisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	principal := &polarisv1alpha1.PolarisPrincipal{
		ObjectMeta: metav1.ObjectMeta{Name: testAnalystPrincipalName, Namespace: principalStatusTestNamespace, Generation: 3},
		Status: polarisv1alpha1.PolarisPrincipalStatus{Conditions: []metav1.Condition{{
			Type: principalCredentialsConditionType, Status: metav1.ConditionTrue, Reason: principalStatusTestCredentialsAvailable,
		}}},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(principal).
		WithObjects(principal).Build()
	reconciler := &PolarisPrincipalReconciler{Client: kubeClient}
	cause := errors.New("remote rejected principal")
	result, err := reconciler.principalFailure(ctx, principal, "RemoteUnavailable", cause)
	if err != nil {
		t.Fatalf("principalFailure() error = %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("RequeueAfter = %s, want 30s", result.RequeueAfter)
	}

	stored := &polarisv1alpha1.PolarisPrincipal{}
	key := types.NamespacedName{Namespace: principal.Namespace, Name: principal.Name}
	if err := kubeClient.Get(ctx, key, stored); err != nil {
		t.Fatal(err)
	}
	for _, conditionType := range []string{readyConditionType, principalCredentialsConditionType} {
		condition := meta.FindStatusCondition(stored.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "RemoteUnavailable" ||
			condition.Message != cause.Error() || condition.ObservedGeneration != principal.Generation {
			t.Errorf("%s condition = %#v", conditionType, condition)
		}
	}
}

func TestPrincipalReadyFailurePreservesCredentialCondition(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := polarisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	principal := &polarisv1alpha1.PolarisPrincipal{
		ObjectMeta: metav1.ObjectMeta{Name: testAnalystPrincipalName, Namespace: principalStatusTestNamespace, Generation: 2},
		Status: polarisv1alpha1.PolarisPrincipalStatus{Conditions: []metav1.Condition{{
			Type: principalCredentialsConditionType, Status: metav1.ConditionTrue, Reason: principalStatusTestCredentialsAvailable,
		}}},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(principal).
		WithObjects(principal).Build()
	reconciler := &PolarisPrincipalReconciler{Client: kubeClient}
	cause := errors.New("principal state could not be read")
	result, err := reconciler.principalReadyFailure(ctx, principal, "RemoteUnavailable", cause)
	if err != nil {
		t.Fatalf("principalReadyFailure() error = %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("RequeueAfter = %s, want 30s", result.RequeueAfter)
	}

	stored := &polarisv1alpha1.PolarisPrincipal{}
	key := types.NamespacedName{Namespace: principal.Namespace, Name: principal.Name}
	if err := kubeClient.Get(ctx, key, stored); err != nil {
		t.Fatal(err)
	}
	ready := meta.FindStatusCondition(stored.Status.Conditions, readyConditionType)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "RemoteUnavailable" || ready.Message != cause.Error() {
		t.Fatalf("Ready condition = %#v", ready)
	}
	credentials := meta.FindStatusCondition(stored.Status.Conditions, principalCredentialsConditionType)
	if credentials == nil || credentials.Status != metav1.ConditionTrue || credentials.Reason != principalStatusTestCredentialsAvailable {
		t.Fatalf("CredentialsReady condition changed: %#v", credentials)
	}
}

func TestPolicyBindingRefNamePrefersObservedPolicy(t *testing.T) {
	binding := &polarisv1alpha1.PolarisPolicyBinding{
		Spec:   polarisv1alpha1.PolarisPolicyBindingSpec{PolicyRef: "desired-policy"},
		Status: polarisv1alpha1.PolarisPolicyBindingStatus{ObservedPolicyRef: "observed-policy"},
	}
	if got := policyBindingRefName(binding); got != "observed-policy" {
		t.Fatalf("policyBindingRefName() = %q, want observed-policy", got)
	}
	binding.Status.ObservedPolicyRef = ""
	if got := policyBindingRefName(binding); got != "desired-policy" {
		t.Fatalf("policyBindingRefName() fallback = %q, want desired-policy", got)
	}
	if got := policyBindingRefName(&polarisv1alpha1.PolarisCatalog{}); got != "" {
		t.Fatalf("policyBindingRefName(non-binding) = %q, want empty", got)
	}
}
