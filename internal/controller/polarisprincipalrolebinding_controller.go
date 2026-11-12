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
	principalRoleBindingFinalizer          = "polaris.apache.org/principal-role-binding-cleanup"
	principalRoleBindingReadyConditionType = "Ready"
	principalRoleBindingRequeueAfter       = 30 * time.Second
)

// PolarisPrincipalRoleBindingReconciler reconciles one Polaris principal-role assignment.
type PolarisPrincipalRoleBindingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisprincipalrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisprincipalrolebindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisprincipalrolebindings/finalizers,verbs=update
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisprincipals,verbs=get;list;watch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisprincipalroles,verbs=get;list;watch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisconnections,verbs=get;list;watch
func (r *PolarisPrincipalRoleBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	binding := &polarisv1alpha1.PolarisPrincipalRoleBinding{}
	if err := r.Get(ctx, req.NamespacedName, binding); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	deleting := !binding.DeletionTimestamp.IsZero()
	if deleting && !controllerutil.ContainsFinalizer(binding, principalRoleBindingFinalizer) {
		return ctrl.Result{}, nil
	}
	if !deleting && !controllerutil.ContainsFinalizer(binding, principalRoleBindingFinalizer) {
		controllerutil.AddFinalizer(binding, principalRoleBindingFinalizer)
		if err := r.Update(ctx, binding); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if binding.Status.ObservedPrincipalName == "" {
		if deleting {
			// Assignment creation is gated on recording the identity snapshot first.
			controllerutil.RemoveFinalizer(binding, principalRoleBindingFinalizer)
			return ctrl.Result{}, r.Update(ctx, binding)
		}
		principal := &polarisv1alpha1.PolarisPrincipal{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.PrincipalRef}, principal); err != nil {
			return r.principalRoleBindingFailure(ctx, binding, "PrincipalNotFound", errors.New("referenced PolarisPrincipal was not found"))
		}
		role := &polarisv1alpha1.PolarisPrincipalRole{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.PrincipalRoleRef}, role); err != nil {
			return r.principalRoleBindingFailure(ctx, binding, "PrincipalRoleNotFound", errors.New("referenced PolarisPrincipalRole was not found"))
		}
		if principal.Spec.ConnectionRef != role.Spec.ConnectionRef {
			return r.principalRoleBindingFailure(ctx, binding, "ConnectionMismatch", errors.New("principal and principal role must use the same PolarisConnection"))
		}
		binding.Status.ObservedPrincipalName = principal.Spec.PrincipalName
		binding.Status.ObservedPrincipalRoleName = role.Spec.RoleName
		binding.Status.ObservedConnectionRef = principal.Spec.ConnectionRef
		if err := r.Status().Update(ctx, binding); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if !deleting {
		principal := &polarisv1alpha1.PolarisPrincipal{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.PrincipalRef}, principal); err != nil {
			return r.principalRoleBindingFailure(ctx, binding, "PrincipalNotFound", errors.New("referenced PolarisPrincipal was not found"))
		}
		role := &polarisv1alpha1.PolarisPrincipalRole{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.PrincipalRoleRef}, role); err != nil {
			return r.principalRoleBindingFailure(ctx, binding, "PrincipalRoleNotFound", errors.New("referenced PolarisPrincipalRole was not found"))
		}
		if principal.Spec.PrincipalName != binding.Status.ObservedPrincipalName ||
			role.Spec.RoleName != binding.Status.ObservedPrincipalRoleName ||
			principal.Spec.ConnectionRef != binding.Status.ObservedConnectionRef ||
			role.Spec.ConnectionRef != binding.Status.ObservedConnectionRef {
			return r.principalRoleBindingFailure(ctx, binding, "IdentityChanged",
				errors.New("referenced principal or role identity changed; restore the recorded names and connection to manage this assignment"))
		}
	}

	connection := &polarisv1alpha1.PolarisConnection{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Status.ObservedConnectionRef}, connection); err != nil {
		return r.principalRoleBindingFailure(ctx, binding, "ConnectionNotFound", errors.New("binding's recorded PolarisConnection was not found"))
	}
	apiClient, err := (&PolarisConnectionReconciler{Client: r.Client}).newClient(ctx, connection)
	if err != nil {
		return r.principalRoleBindingFailure(ctx, binding, "ConnectionUnavailable", err)
	}
	if deleting {
		err := apiClient.RevokePrincipalRole(ctx, binding.Status.ObservedPrincipalName, binding.Status.ObservedPrincipalRoleName)
		if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil {
			if !ok || httpErr.StatusCode != http.StatusNotFound {
				return r.principalRoleBindingFailure(ctx, binding, "RevokeFailed", err)
			}
		}
		controllerutil.RemoveFinalizer(binding, principalRoleBindingFinalizer)
		return ctrl.Result{}, r.Update(ctx, binding)
	}

	roles, err := apiClient.ListPrincipalRolesForPrincipal(ctx, binding.Status.ObservedPrincipalName)
	if err != nil {
		return r.principalRoleBindingFailure(ctx, binding, "AssignmentReadFailed", err)
	}
	assigned := false
	for i := range roles {
		if roles[i].Name == binding.Status.ObservedPrincipalRoleName {
			assigned = true
			break
		}
	}
	if !assigned {
		if err := apiClient.AssignPrincipalRole(ctx, binding.Status.ObservedPrincipalName, binding.Status.ObservedPrincipalRoleName); err != nil {
			return r.principalRoleBindingFailure(ctx, binding, "AssignmentFailed", err)
		}
	}

	now := metav1.Now()
	binding.Status.ObservedGeneration = binding.Generation
	binding.Status.LastReconciledTime = &now
	meta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
		Type: principalRoleBindingReadyConditionType, Status: metav1.ConditionTrue, ObservedGeneration: binding.Generation,
		LastTransitionTime: now, Reason: "PrincipalRoleAssigned", Message: "Polaris principal role is assigned to the principal",
	})
	if err := r.Status().Update(ctx, binding); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: principalRoleBindingRequeueAfter}, nil
}

func (r *PolarisPrincipalRoleBindingReconciler) principalRoleBindingFailure(ctx context.Context, binding *polarisv1alpha1.PolarisPrincipalRoleBinding, reason string, cause error) (ctrl.Result, error) {
	meta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
		Type: principalRoleBindingReadyConditionType, Status: metav1.ConditionFalse, ObservedGeneration: binding.Generation,
		LastTransitionTime: metav1.Now(), Reason: reason, Message: cause.Error(),
	})
	if err := r.Status().Update(ctx, binding); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: principalRoleBindingRequeueAfter}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PolarisPrincipalRoleBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisPrincipalRoleBinding{}).
		Watches(&polarisv1alpha1.PolarisPrincipal{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			bindings := &polarisv1alpha1.PolarisPrincipalRoleBindingList{}
			if err := r.List(ctx, bindings, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0, len(bindings.Items))
			for i := range bindings.Items {
				binding := &bindings.Items[i]
				if binding.Spec.PrincipalRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: binding.Namespace, Name: binding.Name}})
				}
			}
			return requests
		})).
		Watches(&polarisv1alpha1.PolarisPrincipalRole{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			bindings := &polarisv1alpha1.PolarisPrincipalRoleBindingList{}
			if err := r.List(ctx, bindings, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0, len(bindings.Items))
			for i := range bindings.Items {
				binding := &bindings.Items[i]
				if binding.Spec.PrincipalRoleRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: binding.Namespace, Name: binding.Name}})
				}
			}
			return requests
		})).
		Watches(&polarisv1alpha1.PolarisConnection{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			bindings := &polarisv1alpha1.PolarisPrincipalRoleBindingList{}
			if err := r.List(ctx, bindings, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0, len(bindings.Items))
			for i := range bindings.Items {
				binding := &bindings.Items[i]
				if binding.Status.ObservedConnectionRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: binding.Namespace, Name: binding.Name}})
				}
			}
			return requests
		})).
		Named("polarisprincipalrolebinding").
		Complete(r)
}
