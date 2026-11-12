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
	"slices"
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
	polarisPolicyBindingFinalizer    = "polaris.apache.org/policy-binding-cleanup"
	polarisPolicyBindingRequeueAfter = 30 * time.Second
)

// PolarisPolicyBindingReconciler reconciles one policy attachment.
type PolarisPolicyBindingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarispolicybindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarispolicybindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarispolicybindings/finalizers,verbs=update
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarispolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisconnections,verbs=get;list;watch
// Reconcile ensures the policy mapping exists and detaches it during deletion.
func (r *PolarisPolicyBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	binding := &polarisv1alpha1.PolarisPolicyBinding{}
	if err := r.Get(ctx, req.NamespacedName, binding); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	deleting := !binding.DeletionTimestamp.IsZero()
	if deleting && !controllerutil.ContainsFinalizer(binding, polarisPolicyBindingFinalizer) {
		return ctrl.Result{}, nil
	}
	if !deleting && !controllerutil.ContainsFinalizer(binding, polarisPolicyBindingFinalizer) {
		controllerutil.AddFinalizer(binding, polarisPolicyBindingFinalizer)
		if err := r.Update(ctx, binding); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	if result, handled, err := r.initializePolicyBinding(ctx, binding, deleting); handled {
		return result, err
	}

	if result, handled, err := r.validateObservedPolicyBinding(ctx, binding, deleting); handled {
		return result, err
	}

	connection := &polarisv1alpha1.PolarisConnection{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Status.ObservedConnectionRef}, connection); err != nil {
		return r.policyBindingFailure(ctx, binding, "ConnectionNotFound", errors.New("binding's recorded PolarisConnection was not found"))
	}
	if connection.Spec.CatalogURL == "" {
		return r.policyBindingFailure(ctx, binding, "CatalogEndpointMissing", errors.New("PolarisConnection must configure catalogURL to manage policy mappings"))
	}
	apiClient, err := (&PolarisConnectionReconciler{Client: r.Client}).newClient(ctx, connection)
	if err != nil {
		return r.policyBindingFailure(ctx, binding, "ConnectionUnavailable", err)
	}
	target := polarisclient.PolicyTarget{
		Type: binding.Status.ObservedTargetType,
		Path: append([]string(nil), binding.Status.ObservedTargetNamespace...),
	}
	if target.Type == "table-like" {
		target.Path = append(target.Path, binding.Status.ObservedTargetName)
	}
	if !deleting {
		target, err = policyTarget(binding.Spec)
		if err != nil {
			return r.policyBindingFailure(ctx, binding, "InvalidTarget", err)
		}
	}

	if deleting {
		err := apiClient.DetachPolicy(ctx, binding.Status.ObservedCatalogName, binding.Status.ObservedNamespace,
			binding.Status.ObservedPolicyName, target)
		if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil && (!ok || httpErr.StatusCode != http.StatusNotFound) {
			return r.policyBindingFailure(ctx, binding, "DetachFailed", err)
		}
		controllerutil.RemoveFinalizer(binding, polarisPolicyBindingFinalizer)
		return ctrl.Result{}, r.Update(ctx, binding)
	}

	bindings := &polarisv1alpha1.PolarisPolicyBindingList{}
	if err := r.List(ctx, bindings, client.InNamespace(binding.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	for i := range bindings.Items {
		other := &bindings.Items[i]
		if other.Name == binding.Name || other.Spec.PolicyRef != binding.Spec.PolicyRef {
			continue
		}
		if other.Spec.TargetType == binding.Spec.TargetType &&
			other.Spec.TargetName == binding.Spec.TargetName &&
			reflect.DeepEqual(other.Spec.Namespace, binding.Spec.Namespace) {
			return r.policyBindingFailure(ctx, binding, "DuplicateTarget",
				errors.New("another PolarisPolicyBinding already manages this policy target"))
		}
	}
	parameters := binding.Spec.Parameters
	if parameters == nil {
		parameters = map[string]string{}
	}
	if err := apiClient.AttachPolicy(ctx, binding.Status.ObservedCatalogName, binding.Status.ObservedNamespace,
		binding.Status.ObservedPolicyName, target, parameters); err != nil {
		return r.policyBindingFailure(ctx, binding, "AttachFailed", err)
	}

	now := metav1.Now()
	binding.Status.ObservedGeneration = binding.Generation
	binding.Status.LastReconciledTime = &now
	meta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
		Type: readyConditionType, Status: metav1.ConditionTrue, ObservedGeneration: binding.Generation,
		LastTransitionTime: now, Reason: "PolicyAttached", Message: "Polaris policy is attached to the target",
	})
	if err := r.Status().Update(ctx, binding); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *PolarisPolicyBindingReconciler) initializePolicyBinding(
	ctx context.Context,
	binding *polarisv1alpha1.PolarisPolicyBinding,
	deleting bool,
) (ctrl.Result, bool, error) {
	if binding.Status.ObservedPolicyName != "" {
		return ctrl.Result{}, false, nil
	}
	if deleting {
		controllerutil.RemoveFinalizer(binding, polarisPolicyBindingFinalizer)
		return ctrl.Result{}, true, r.Update(ctx, binding)
	}
	if err := policyTargetError(binding.Spec); err != nil {
		result, failureErr := r.policyBindingFailure(ctx, binding, "InvalidTarget", err)
		return result, true, failureErr
	}
	policy := &polarisv1alpha1.PolarisPolicy{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.PolicyRef}, policy); err != nil {
		result, failureErr := r.policyBindingFailure(ctx, binding, "PolicyNotFound", errors.New("referenced PolarisPolicy was not found"))
		return result, true, failureErr
	}
	if !meta.IsStatusConditionTrue(policy.Status.Conditions, readyConditionType) || !policy.DeletionTimestamp.IsZero() {
		result, failureErr := r.policyBindingFailure(ctx, binding, "PolicyNotReady", errors.New("referenced PolarisPolicy must be Ready and not deleting"))
		return result, true, failureErr
	}
	if policy.Status.ObservedPolicyName == "" || policy.Status.ObservedCatalogName == "" || policy.Status.ObservedConnectionRef == "" {
		result, failureErr := r.policyBindingFailure(ctx, binding, "PolicyIdentityUnknown", errors.New("referenced PolarisPolicy has no recorded remote identity"))
		return result, true, failureErr
	}
	binding.Status.ObservedPolicyRef = binding.Spec.PolicyRef
	binding.Status.ObservedCatalogRef = policy.Status.ObservedCatalogRef
	binding.Status.ObservedCatalogName = policy.Status.ObservedCatalogName
	binding.Status.ObservedConnectionRef = policy.Status.ObservedConnectionRef
	binding.Status.ObservedNamespace = append([]string(nil), policy.Status.ObservedNamespace...)
	binding.Status.ObservedPolicyName = policy.Status.ObservedPolicyName
	binding.Status.ObservedTargetType = binding.Spec.TargetType
	binding.Status.ObservedTargetNamespace = append([]string(nil), binding.Spec.Namespace...)
	binding.Status.ObservedTargetName = binding.Spec.TargetName
	if err := r.Status().Update(ctx, binding); err != nil {
		return ctrl.Result{}, true, err
	}
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

func (r *PolarisPolicyBindingReconciler) validateObservedPolicyBinding(
	ctx context.Context,
	binding *polarisv1alpha1.PolarisPolicyBinding,
	deleting bool,
) (ctrl.Result, bool, error) {
	if deleting {
		return ctrl.Result{}, false, nil
	}
	if binding.Spec.PolicyRef != binding.Status.ObservedPolicyRef ||
		binding.Spec.TargetType != binding.Status.ObservedTargetType ||
		!reflect.DeepEqual(binding.Spec.Namespace, binding.Status.ObservedTargetNamespace) ||
		binding.Spec.TargetName != binding.Status.ObservedTargetName {
		result, err := r.policyBindingFailure(ctx, binding, "IdentityChangeNotAllowed",
			errors.New("policyRef and target identity cannot change after the remote mapping is recorded"))
		return result, true, err
	}
	policy := &polarisv1alpha1.PolarisPolicy{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Status.ObservedPolicyRef}, policy); err != nil {
		result, failureErr := r.policyBindingFailure(ctx, binding, "PolicyNotFound", errors.New("recorded PolarisPolicy was not found"))
		return result, true, failureErr
	}
	if !meta.IsStatusConditionTrue(policy.Status.Conditions, readyConditionType) || !policy.DeletionTimestamp.IsZero() {
		result, failureErr := r.policyBindingFailure(ctx, binding, "PolicyNotReady", errors.New("referenced PolarisPolicy must remain Ready and not deleting"))
		return result, true, failureErr
	}
	if policy.Status.ObservedCatalogRef != binding.Status.ObservedCatalogRef ||
		policy.Status.ObservedCatalogName != binding.Status.ObservedCatalogName ||
		policy.Status.ObservedConnectionRef != binding.Status.ObservedConnectionRef ||
		policy.Status.ObservedPolicyName != binding.Status.ObservedPolicyName ||
		!reflect.DeepEqual(policy.Status.ObservedNamespace, binding.Status.ObservedNamespace) {
		result, err := r.policyBindingFailure(ctx, binding, "PolicyIdentityChanged",
			errors.New("referenced PolarisPolicy identity changed; restore the recorded catalog, namespace, policy, and connection"))
		return result, true, err
	}
	if binding.Status.ObservedGeneration == binding.Generation && meta.IsStatusConditionTrue(binding.Status.Conditions, readyConditionType) {
		return ctrl.Result{}, true, nil
	}
	return ctrl.Result{}, false, nil
}

func policyTargetError(spec polarisv1alpha1.PolarisPolicyBindingSpec) error {
	_, err := policyTarget(spec)
	return err
}
func policyTarget(spec polarisv1alpha1.PolarisPolicyBindingSpec) (polarisclient.PolicyTarget, error) {
	if slices.Contains(spec.Namespace, "") {
		return polarisclient.PolicyTarget{}, errors.New("namespace path segments must not be empty")
	}
	target := polarisclient.PolicyTarget{Type: spec.TargetType}
	switch spec.TargetType {
	case "catalog":
		if len(spec.Namespace) != 0 || spec.TargetName != "" {
			return polarisclient.PolicyTarget{}, errors.New("catalog targets cannot specify namespace or targetName")
		}
	case "namespace":
		if len(spec.Namespace) == 0 || spec.TargetName != "" {
			return polarisclient.PolicyTarget{}, errors.New("namespace targets require namespace and cannot specify targetName")
		}
		target.Path = append([]string(nil), spec.Namespace...)
	case "table-like":
		if len(spec.Namespace) == 0 || spec.TargetName == "" {
			return polarisclient.PolicyTarget{}, errors.New("table-like targets require namespace and targetName")
		}
		target.Path = append([]string(nil), spec.Namespace...)
		target.Path = append(target.Path, spec.TargetName)
	default:
		return polarisclient.PolicyTarget{}, errors.New("targetType must be catalog, namespace, or table-like")
	}
	return target, nil
}

func (r *PolarisPolicyBindingReconciler) policyBindingFailure(ctx context.Context, binding *polarisv1alpha1.PolarisPolicyBinding, reason string, cause error) (ctrl.Result, error) {
	meta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
		Type: readyConditionType, Status: metav1.ConditionFalse, ObservedGeneration: binding.Generation,
		LastTransitionTime: metav1.Now(), Reason: reason, Message: cause.Error(),
	})
	if err := r.Status().Update(ctx, binding); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: polarisPolicyBindingRequeueAfter}, nil
}

// SetupWithManager watches the referenced policy so bindings wait for policy readiness and deletion.
func (r *PolarisPolicyBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisPolicyBinding{}).
		Watches(&polarisv1alpha1.PolarisPolicy{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			bindings := &polarisv1alpha1.PolarisPolicyBindingList{}
			if err := r.List(ctx, bindings, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0)
			for i := range bindings.Items {
				binding := &bindings.Items[i]
				if binding.Spec.PolicyRef == object.GetName() || binding.Status.ObservedPolicyRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: binding.Namespace, Name: binding.Name}})
				}
			}
			return requests
		})).
		Named("polarispolicybinding").Complete(r)
}
