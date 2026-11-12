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
	catalogRoleFinalizer          = "polaris.apache.org/catalog-role-cleanup"
	catalogRoleReadyConditionType = "Ready"
	catalogRoleRequeueAfter       = 30 * time.Second
)

// PolarisCatalogRoleReconciler reconciles a PolarisCatalogRole object.
type PolarisCatalogRoleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogroles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogroles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogroles/finalizers,verbs=update
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogs,verbs=get;list;watch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisconnections,verbs=get;list;watch
func (r *PolarisCatalogRoleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	role := &polarisv1alpha1.PolarisCatalogRole{}
	if err := r.Get(ctx, req.NamespacedName, role); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	deleting := !role.DeletionTimestamp.IsZero()
	if deleting && !controllerutil.ContainsFinalizer(role, catalogRoleFinalizer) {
		return ctrl.Result{}, nil
	}
	if !deleting && !controllerutil.ContainsFinalizer(role, catalogRoleFinalizer) {
		controllerutil.AddFinalizer(role, catalogRoleFinalizer)
		if err := r.Update(ctx, role); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if role.Status.ObservedCatalogRef == "" {
		catalog := &polarisv1alpha1.PolarisCatalog{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: role.Namespace, Name: role.Spec.CatalogRef}, catalog); err != nil {
			return r.catalogRoleFailure(ctx, role, "CatalogNotFound", errors.New("referenced PolarisCatalog was not found"))
		}
		role.Status.ObservedCatalogRef = role.Spec.CatalogRef
		role.Status.ObservedCatalogName = catalog.Spec.CatalogName
		role.Status.ObservedConnectionRef = catalog.Spec.ConnectionRef
		role.Status.ObservedRoleName = role.Spec.CatalogRoleName
		if err := r.Status().Update(ctx, role); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if role.Spec.CatalogRef != role.Status.ObservedCatalogRef || role.Spec.CatalogRoleName != role.Status.ObservedRoleName {
		return r.catalogRoleFailure(ctx, role, "IdentityChangeNotAllowed",
			errors.New("catalogRef and catalogRoleName cannot change after the remote role identity is recorded"))
	}

	if !deleting {
		catalog := &polarisv1alpha1.PolarisCatalog{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: role.Namespace, Name: role.Status.ObservedCatalogRef}, catalog); err != nil {
			return r.catalogRoleFailure(ctx, role, "CatalogNotFound", errors.New("referenced PolarisCatalog was not found"))
		}
		if catalog.Spec.CatalogName != role.Status.ObservedCatalogName ||
			catalog.Spec.ConnectionRef != role.Status.ObservedConnectionRef {
			return r.catalogRoleFailure(ctx, role, "CatalogIdentityChanged",
				errors.New("the referenced PolarisCatalog changed its remote name or connection; restore the original catalog identity to manage this role"))
		}
	}

	connection := &polarisv1alpha1.PolarisConnection{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: role.Namespace, Name: role.Status.ObservedConnectionRef,
	}, connection); err != nil {
		return r.catalogRoleFailure(ctx, role, "ConnectionNotFound", errors.New("role's recorded PolarisConnection was not found"))
	}
	apiClient, err := (&PolarisConnectionReconciler{Client: r.Client}).newClient(ctx, connection)
	if err != nil {
		return r.catalogRoleFailure(ctx, role, "ConnectionUnavailable", err)
	}

	if deleting {
		err := apiClient.DeleteCatalogRole(ctx, role.Status.ObservedCatalogName, role.Status.ObservedRoleName)
		if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil {
			if !ok || httpErr.StatusCode != http.StatusNotFound {
				return r.catalogRoleFailure(ctx, role, "RemoteDeleteFailed", err)
			}
		}
		controllerutil.RemoveFinalizer(role, catalogRoleFinalizer)
		return ctrl.Result{}, r.Update(ctx, role)
	}

	remote, err := apiClient.GetCatalogRole(ctx, role.Status.ObservedCatalogName, role.Status.ObservedRoleName)
	if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil && ok && httpErr.StatusCode == http.StatusNotFound {
		remote, err = apiClient.CreateCatalogRole(ctx, role.Status.ObservedCatalogName, desiredCatalogRole(role))
	} else if err == nil && !reflect.DeepEqual(remote.Properties, role.Spec.Properties) {
		remote.Properties = role.Spec.Properties
		remote, err = apiClient.UpdateCatalogRole(ctx, role.Status.ObservedCatalogName, remote)
	}
	if err != nil {
		return r.catalogRoleFailure(ctx, role, "ReconcileFailed", err)
	}

	now := metav1.Now()
	role.Status.ObservedGeneration = role.Generation
	role.Status.EntityVersion = remote.EntityVersion
	role.Status.LastReconciledTime = &now
	meta.SetStatusCondition(&role.Status.Conditions, metav1.Condition{
		Type: catalogRoleReadyConditionType, Status: metav1.ConditionTrue, ObservedGeneration: role.Generation,
		LastTransitionTime: now, Reason: "CatalogRoleReady", Message: "Polaris catalog role matches the desired state",
	})
	if err := r.Status().Update(ctx, role); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: catalogRoleRequeueAfter}, nil
}

func desiredCatalogRole(role *polarisv1alpha1.PolarisCatalogRole) polarisclient.CatalogRole {
	return polarisclient.CatalogRole{Name: role.Status.ObservedRoleName, Properties: role.Spec.Properties}
}

func (r *PolarisCatalogRoleReconciler) catalogRoleFailure(ctx context.Context, role *polarisv1alpha1.PolarisCatalogRole, reason string, cause error) (ctrl.Result, error) {
	meta.SetStatusCondition(&role.Status.Conditions, metav1.Condition{
		Type: catalogRoleReadyConditionType, Status: metav1.ConditionFalse, ObservedGeneration: role.Generation,
		LastTransitionTime: metav1.Now(), Reason: reason, Message: cause.Error(),
	})
	if err := r.Status().Update(ctx, role); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: catalogRoleRequeueAfter}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PolarisCatalogRoleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisCatalogRole{}).
		Watches(&polarisv1alpha1.PolarisCatalog{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			roles := &polarisv1alpha1.PolarisCatalogRoleList{}
			if err := r.List(ctx, roles, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0, len(roles.Items))
			for i := range roles.Items {
				role := &roles.Items[i]
				if role.Spec.CatalogRef == object.GetName() || role.Status.ObservedCatalogRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
						Namespace: role.Namespace, Name: role.Name,
					}})
				}
			}
			return requests
		})).
		Watches(&polarisv1alpha1.PolarisConnection{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			roles := &polarisv1alpha1.PolarisCatalogRoleList{}
			if err := r.List(ctx, roles, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0, len(roles.Items))
			for i := range roles.Items {
				if roles.Items[i].Status.ObservedConnectionRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
						Namespace: roles.Items[i].Namespace, Name: roles.Items[i].Name,
					}})
				}
			}
			return requests
		})).
		Named("polariscatalogrole").
		Complete(r)
}
