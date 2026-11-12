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
	"net/http"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
	polarisclient "github.com/apache/polaris-kubernetes-operator/internal/polaris"
)

const (
	polarisPolicyFinalizer    = "polaris.apache.org/policy-cleanup"
	polarisPolicyRequeueAfter = 30 * time.Second
)

// PolarisPolicyReconciler reconciles one policy in an existing Polaris catalog.
type PolarisPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarispolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarispolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarispolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarispolicybindings,verbs=get;list;watch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogs,verbs=get;list;watch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisconnections,verbs=get;list;watch
// Reconcile compares the desired policy definition with the remote policy.
func (r *PolarisPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	policy := &polarisv1alpha1.PolarisPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	deleting := !policy.DeletionTimestamp.IsZero()
	if deleting && !controllerutil.ContainsFinalizer(policy, polarisPolicyFinalizer) {
		return ctrl.Result{}, nil
	}
	if !deleting && !controllerutil.ContainsFinalizer(policy, polarisPolicyFinalizer) {
		controllerutil.AddFinalizer(policy, polarisPolicyFinalizer)
		if err := r.Update(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if policy.Status.ObservedPolicyName == "" {
		if deleting {
			controllerutil.RemoveFinalizer(policy, polarisPolicyFinalizer)
			return ctrl.Result{}, r.Update(ctx, policy)
		}
		catalog := &polarisv1alpha1.PolarisCatalog{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: policy.Namespace, Name: policy.Spec.CatalogRef}, catalog); err != nil {
			return r.policyFailure(ctx, policy, "CatalogNotFound", errors.New("referenced PolarisCatalog was not found"))
		}
		if !meta.IsStatusConditionTrue(catalog.Status.Conditions, "Ready") {
			return r.policyFailure(ctx, policy, "CatalogNotReady", errors.New("referenced PolarisCatalog must be Ready"))
		}
		policy.Status.ObservedCatalogRef = policy.Spec.CatalogRef
		policy.Status.ObservedCatalogName = catalog.Spec.CatalogName
		policy.Status.ObservedConnectionRef = catalog.Spec.ConnectionRef
		policy.Status.ObservedNamespace = append([]string(nil), policy.Spec.Namespace...)
		policy.Status.ObservedPolicyName = policy.Spec.PolicyName
		policy.Status.ObservedType = policy.Spec.Type
		if err := r.Status().Update(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if policy.Spec.CatalogRef != policy.Status.ObservedCatalogRef ||
		policy.Spec.PolicyName != policy.Status.ObservedPolicyName ||
		policy.Spec.Type != policy.Status.ObservedType ||
		!reflect.DeepEqual(policy.Spec.Namespace, policy.Status.ObservedNamespace) {
		return r.policyFailure(ctx, policy, "IdentityChangeNotAllowed",
			errors.New("catalogRef, namespace, policyName, and type cannot change after the remote policy identity is recorded"))
	}

	if !deleting {
		catalog := &polarisv1alpha1.PolarisCatalog{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: policy.Namespace, Name: policy.Status.ObservedCatalogRef}, catalog); err != nil {
			return r.policyFailure(ctx, policy, "CatalogNotFound", errors.New("recorded PolarisCatalog was not found"))
		}
		if !meta.IsStatusConditionTrue(catalog.Status.Conditions, "Ready") {
			return r.policyFailure(ctx, policy, "CatalogNotReady", errors.New("referenced PolarisCatalog must remain Ready"))
		}
		if catalog.Spec.CatalogName != policy.Status.ObservedCatalogName || catalog.Spec.ConnectionRef != policy.Status.ObservedConnectionRef {
			return r.policyFailure(ctx, policy, "CatalogIdentityChanged",
				errors.New("the referenced PolarisCatalog changed its remote name or connection; restore the recorded identity"))
		}
	}

	connection := &polarisv1alpha1.PolarisConnection{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: policy.Namespace, Name: policy.Status.ObservedConnectionRef}, connection); err != nil {
		return r.policyFailure(ctx, policy, "ConnectionNotFound", errors.New("policy's recorded PolarisConnection was not found"))
	}
	if connection.Spec.CatalogURL == "" {
		return r.policyFailure(ctx, policy, "CatalogEndpointMissing", errors.New("PolarisConnection must configure catalogURL to manage policies"))
	}
	apiClient, err := (&PolarisConnectionReconciler{Client: r.Client}).newClient(ctx, connection)
	if err != nil {
		return r.policyFailure(ctx, policy, "ConnectionUnavailable", err)
	}

	if deleting {
		bindings := &polarisv1alpha1.PolarisPolicyBindingList{}
		if err := r.List(ctx, bindings, client.InNamespace(policy.Namespace)); err != nil {
			return ctrl.Result{}, err
		}
		for i := range bindings.Items {
			binding := &bindings.Items[i]
			if binding.Spec.PolicyRef == policy.Name || binding.Status.ObservedPolicyRef == policy.Name {
				return r.policyFailure(ctx, policy, "PolicyBindingsRemain",
					errors.New("delete PolarisPolicyBinding resources first so their finalizers detach managed mappings"))
			}
		}
		err := apiClient.DeletePolicy(ctx, policy.Status.ObservedCatalogName, policy.Status.ObservedNamespace,
			policy.Status.ObservedPolicyName, policy.Spec.DetachAllOnDelete)
		if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil && (!ok || httpErr.StatusCode != http.StatusNotFound) {
			return r.policyFailure(ctx, policy, "RemoteDeleteFailed", err)
		}
		controllerutil.RemoveFinalizer(policy, polarisPolicyFinalizer)
		return ctrl.Result{}, r.Update(ctx, policy)
	}

	remote, err := apiClient.GetPolicy(ctx, policy.Status.ObservedCatalogName, policy.Status.ObservedNamespace, policy.Status.ObservedPolicyName)
	if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil && ok && httpErr.StatusCode == http.StatusNotFound {
		remote, err = apiClient.CreatePolicy(ctx, policy.Status.ObservedCatalogName, policy.Status.ObservedNamespace, polarisclient.Policy{
			Name: policy.Status.ObservedPolicyName, Type: policy.Status.ObservedType,
			Description: policy.Spec.Description, Content: policy.Spec.Content,
		})
	} else if err == nil {
		if remote.Type != policy.Status.ObservedType {
			return r.policyFailure(ctx, policy, "RemoteTypeMismatch", errors.New("existing Polaris policy has a different immutable type"))
		}
		if remote.Description != policy.Spec.Description || remote.Content != policy.Spec.Content {
			remote.Description = policy.Spec.Description
			remote.Content = policy.Spec.Content
			remote, err = apiClient.UpdatePolicy(ctx, policy.Status.ObservedCatalogName, policy.Status.ObservedNamespace, remote)
		}
	}
	if err != nil {
		return r.policyFailure(ctx, policy, "ReconcileFailed", err)
	}

	now := metav1.Now()
	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.PolicyVersion = remote.Version
	policy.Status.Inheritable = remote.Inheritable
	policy.Status.LastReconciledTime = &now
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, ObservedGeneration: policy.Generation,
		LastTransitionTime: now, Reason: "PolicyReady", Message: "Polaris policy matches the desired state",
	})
	if err := r.Status().Update(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: polarisPolicyRequeueAfter}, nil
}

func (r *PolarisPolicyReconciler) policyFailure(ctx context.Context, policy *polarisv1alpha1.PolarisPolicy, reason string, cause error) (ctrl.Result, error) {
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, ObservedGeneration: policy.Generation,
		LastTransitionTime: metav1.Now(), Reason: reason, Message: cause.Error(),
	})
	if err := r.Status().Update(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: polarisPolicyRequeueAfter}, nil
}

// SetupWithManager watches referenced catalogs, connections, and bindings to order cleanup.
func (r *PolarisPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapPolicyRefs := func(ctx context.Context, object client.Object) []reconcile.Request {
		policies := &polarisv1alpha1.PolarisPolicyList{}
		if err := r.List(ctx, policies, client.InNamespace(object.GetNamespace())); err != nil {
			return nil
		}
		requests := make([]reconcile.Request, 0)
		for i := range policies.Items {
			policy := &policies.Items[i]
			switch object.(type) {
			case *polarisv1alpha1.PolarisCatalog:
				if policy.Spec.CatalogRef == object.GetName() || policy.Status.ObservedCatalogRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}})
				}
			case *polarisv1alpha1.PolarisConnection:
				if policy.Status.ObservedConnectionRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}})
				}
			case *polarisv1alpha1.PolarisPolicyBinding:
				if policy.Name == policyBindingRefName(object) {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}})
				}
			}
		}
		return requests
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisPolicy{}).
		Watches(&polarisv1alpha1.PolarisCatalog{}, handler.EnqueueRequestsFromMapFunc(mapPolicyRefs)).
		Watches(&polarisv1alpha1.PolarisConnection{}, handler.EnqueueRequestsFromMapFunc(mapPolicyRefs)).
		Watches(&polarisv1alpha1.PolarisPolicyBinding{}, handler.EnqueueRequestsFromMapFunc(mapPolicyRefs)).
		Named("polarispolicy").Complete(r)
}

func policyBindingRefName(object client.Object) string {
	binding, ok := object.(*polarisv1alpha1.PolarisPolicyBinding)
	if !ok {
		return ""
	}
	if binding.Status.ObservedPolicyRef != "" {
		return binding.Status.ObservedPolicyRef
	}
	return binding.Spec.PolicyRef
}
