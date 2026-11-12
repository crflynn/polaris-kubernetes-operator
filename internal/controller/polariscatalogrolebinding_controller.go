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
	catalogRoleBindingFinalizer          = "polaris.apache.org/catalog-role-binding-cleanup"
	catalogRoleBindingReadyConditionType = "Ready"
	catalogRoleBindingRequeueAfter       = 30 * time.Second
)

// PolarisCatalogRoleBindingReconciler reconciles one principal-role to catalog-role assignment.
type PolarisCatalogRoleBindingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogrolebindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogrolebindings/finalizers,verbs=update
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisprincipalroles,verbs=get;list;watch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogroles,verbs=get;list;watch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisconnections,verbs=get;list;watch
func (r *PolarisCatalogRoleBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	binding := &polarisv1alpha1.PolarisCatalogRoleBinding{}
	if err := r.Get(ctx, req.NamespacedName, binding); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	deleting := !binding.DeletionTimestamp.IsZero()
	if deleting && !controllerutil.ContainsFinalizer(binding, catalogRoleBindingFinalizer) {
		return ctrl.Result{}, nil
	}
	if !deleting && !controllerutil.ContainsFinalizer(binding, catalogRoleBindingFinalizer) {
		controllerutil.AddFinalizer(binding, catalogRoleBindingFinalizer)
		if err := r.Update(ctx, binding); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if binding.Status.ObservedPrincipalRoleName == "" {
		if deleting {
			controllerutil.RemoveFinalizer(binding, catalogRoleBindingFinalizer)
			return ctrl.Result{}, r.Update(ctx, binding)
		}
		principalRole := &polarisv1alpha1.PolarisPrincipalRole{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.PrincipalRoleRef}, principalRole); err != nil {
			return r.catalogRoleBindingFailure(ctx, binding, "PrincipalRoleNotFound", errors.New("referenced PolarisPrincipalRole was not found"))
		}
		catalogRole := &polarisv1alpha1.PolarisCatalogRole{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.CatalogRoleRef}, catalogRole); err != nil {
			return r.catalogRoleBindingFailure(ctx, binding, "CatalogRoleNotFound", errors.New("referenced PolarisCatalogRole was not found"))
		}
		if !meta.IsStatusConditionTrue(principalRole.Status.Conditions, "Ready") ||
			!meta.IsStatusConditionTrue(catalogRole.Status.Conditions, "Ready") {
			return r.catalogRoleBindingFailure(ctx, binding, "RolesNotReady", errors.New("referenced Polaris roles must be Ready before assigning them"))
		}
		if principalRole.Spec.ConnectionRef != catalogRole.Status.ObservedConnectionRef {
			return r.catalogRoleBindingFailure(ctx, binding, "ConnectionMismatch", errors.New("principal role and catalog role must use the same PolarisConnection"))
		}
		if catalogRole.Status.ObservedCatalogName == "" || catalogRole.Status.ObservedRoleName == "" {
			return r.catalogRoleBindingFailure(ctx, binding, "CatalogIdentityUnknown", errors.New("catalog role has no recorded remote identity"))
		}
		binding.Status.ObservedPrincipalRoleRef = binding.Spec.PrincipalRoleRef
		binding.Status.ObservedPrincipalRoleName = principalRole.Spec.RoleName
		binding.Status.ObservedCatalogRoleRef = binding.Spec.CatalogRoleRef
		binding.Status.ObservedCatalogName = catalogRole.Status.ObservedCatalogName
		binding.Status.ObservedCatalogRoleName = catalogRole.Status.ObservedRoleName
		binding.Status.ObservedConnectionRef = principalRole.Spec.ConnectionRef
		if err := r.Status().Update(ctx, binding); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if !deleting {
		principalRole := &polarisv1alpha1.PolarisPrincipalRole{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.PrincipalRoleRef}, principalRole); err != nil {
			return r.catalogRoleBindingFailure(ctx, binding, "PrincipalRoleNotFound", errors.New("referenced PolarisPrincipalRole was not found"))
		}
		catalogRole := &polarisv1alpha1.PolarisCatalogRole{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.CatalogRoleRef}, catalogRole); err != nil {
			return r.catalogRoleBindingFailure(ctx, binding, "CatalogRoleNotFound", errors.New("referenced PolarisCatalogRole was not found"))
		}
		if !meta.IsStatusConditionTrue(principalRole.Status.Conditions, "Ready") ||
			!meta.IsStatusConditionTrue(catalogRole.Status.Conditions, "Ready") {
			return r.catalogRoleBindingFailure(ctx, binding, "RolesNotReady", errors.New("referenced Polaris roles must remain Ready"))
		}
		if binding.Spec.PrincipalRoleRef != binding.Status.ObservedPrincipalRoleRef ||
			binding.Spec.CatalogRoleRef != binding.Status.ObservedCatalogRoleRef ||
			principalRole.Spec.RoleName != binding.Status.ObservedPrincipalRoleName ||
			principalRole.Spec.ConnectionRef != binding.Status.ObservedConnectionRef ||
			catalogRole.Status.ObservedCatalogName != binding.Status.ObservedCatalogName ||
			catalogRole.Status.ObservedRoleName != binding.Status.ObservedCatalogRoleName ||
			catalogRole.Status.ObservedConnectionRef != binding.Status.ObservedConnectionRef {
			return r.catalogRoleBindingFailure(ctx, binding, "IdentityChanged",
				errors.New("referenced role identity changed; restore the recorded role, catalog, and connection to manage this assignment"))
		}
	}

	connection := &polarisv1alpha1.PolarisConnection{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Status.ObservedConnectionRef}, connection); err != nil {
		return r.catalogRoleBindingFailure(ctx, binding, "ConnectionNotFound", errors.New("binding's recorded PolarisConnection was not found"))
	}
	apiClient, err := (&PolarisConnectionReconciler{Client: r.Client}).newClient(ctx, connection)
	if err != nil {
		return r.catalogRoleBindingFailure(ctx, binding, "ConnectionUnavailable", err)
	}
	if deleting {
		err := apiClient.RevokeCatalogRoleFromPrincipalRole(ctx, binding.Status.ObservedPrincipalRoleName,
			binding.Status.ObservedCatalogName, binding.Status.ObservedCatalogRoleName)
		if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil {
			if !ok || httpErr.StatusCode != http.StatusNotFound {
				return r.catalogRoleBindingFailure(ctx, binding, "RevokeFailed", err)
			}
		}
		controllerutil.RemoveFinalizer(binding, catalogRoleBindingFinalizer)
		return ctrl.Result{}, r.Update(ctx, binding)
	}

	roles, err := apiClient.ListCatalogRolesForPrincipalRole(ctx, binding.Status.ObservedPrincipalRoleName, binding.Status.ObservedCatalogName)
	if err != nil {
		return r.catalogRoleBindingFailure(ctx, binding, "AssignmentReadFailed", err)
	}
	assigned := false
	for i := range roles {
		if roles[i].Name == binding.Status.ObservedCatalogRoleName {
			assigned = true
			break
		}
	}
	if !assigned {
		if err := apiClient.AssignCatalogRoleToPrincipalRole(ctx, binding.Status.ObservedPrincipalRoleName,
			binding.Status.ObservedCatalogName, binding.Status.ObservedCatalogRoleName); err != nil {
			return r.catalogRoleBindingFailure(ctx, binding, "AssignmentFailed", err)
		}
	}

	now := metav1.Now()
	binding.Status.ObservedGeneration = binding.Generation
	binding.Status.LastReconciledTime = &now
	meta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
		Type: catalogRoleBindingReadyConditionType, Status: metav1.ConditionTrue, ObservedGeneration: binding.Generation,
		LastTransitionTime: now, Reason: "CatalogRoleAssigned", Message: "Polaris catalog role is assigned to the principal role",
	})
	if err := r.Status().Update(ctx, binding); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: catalogRoleBindingRequeueAfter}, nil
}

func (r *PolarisCatalogRoleBindingReconciler) catalogRoleBindingFailure(ctx context.Context, binding *polarisv1alpha1.PolarisCatalogRoleBinding, reason string, cause error) (ctrl.Result, error) {
	meta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
		Type: catalogRoleBindingReadyConditionType, Status: metav1.ConditionFalse, ObservedGeneration: binding.Generation,
		LastTransitionTime: metav1.Now(), Reason: reason, Message: cause.Error(),
	})
	if err := r.Status().Update(ctx, binding); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: catalogRoleBindingRequeueAfter}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PolarisCatalogRoleBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisCatalogRoleBinding{}).
		Watches(&polarisv1alpha1.PolarisPrincipalRole{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			bindings := &polarisv1alpha1.PolarisCatalogRoleBindingList{}
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
		Watches(&polarisv1alpha1.PolarisCatalogRole{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			bindings := &polarisv1alpha1.PolarisCatalogRoleBindingList{}
			if err := r.List(ctx, bindings, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0, len(bindings.Items))
			for i := range bindings.Items {
				binding := &bindings.Items[i]
				if binding.Spec.CatalogRoleRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: binding.Namespace, Name: binding.Name}})
				}
			}
			return requests
		})).
		Watches(&polarisv1alpha1.PolarisConnection{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			bindings := &polarisv1alpha1.PolarisCatalogRoleBindingList{}
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
		Named("polariscatalogrolebinding").
		Complete(r)
}
