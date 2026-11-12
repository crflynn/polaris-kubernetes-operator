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
	principalRoleFinalizer          = "polaris.apache.org/principal-role-cleanup"
	principalRoleReadyConditionType = "Ready"
	principalRoleRequeueAfter       = 30 * time.Second
)

// PolarisPrincipalRoleReconciler reconciles a PolarisPrincipalRole object.
type PolarisPrincipalRoleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisprincipalroles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisprincipalroles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisprincipalroles/finalizers,verbs=update
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisconnections,verbs=get;list;watch
func (r *PolarisPrincipalRoleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	role := &polarisv1alpha1.PolarisPrincipalRole{}
	if err := r.Get(ctx, req.NamespacedName, role); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	connection := &polarisv1alpha1.PolarisConnection{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: role.Namespace, Name: role.Spec.ConnectionRef}, connection); err != nil {
		if apierrors.IsNotFound(err) {
			return r.principalRoleFailure(ctx, role, "ConnectionNotFound", errors.New("referenced PolarisConnection was not found"))
		}
		return ctrl.Result{}, err
	}
	apiClient, err := (&PolarisConnectionReconciler{Client: r.Client}).newClient(ctx, connection)
	if err != nil {
		return r.principalRoleFailure(ctx, role, "ConnectionUnavailable", err)
	}

	if !role.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(role, principalRoleFinalizer) {
			return ctrl.Result{}, nil
		}
		err := apiClient.DeletePrincipalRole(ctx, role.Spec.RoleName)
		if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil {
			if !ok || httpErr.StatusCode != http.StatusNotFound {
				return r.principalRoleFailure(ctx, role, "RemoteDeleteFailed", err)
			}
		}
		controllerutil.RemoveFinalizer(role, principalRoleFinalizer)
		return ctrl.Result{}, r.Update(ctx, role)
	}
	if !controllerutil.ContainsFinalizer(role, principalRoleFinalizer) {
		controllerutil.AddFinalizer(role, principalRoleFinalizer)
		if err := r.Update(ctx, role); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	remote, err := apiClient.GetPrincipalRole(ctx, role.Spec.RoleName)
	if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil && ok && httpErr.StatusCode == http.StatusNotFound {
		remote, err = apiClient.CreatePrincipalRole(ctx, desiredPrincipalRole(role))
	} else if err == nil {
		desired := desiredPrincipalRole(role)
		switch {
		case remote.Federated != desired.Federated:
			err = errors.New("federated is immutable for an existing Polaris principal role")
		case !reflect.DeepEqual(remote.Properties, desired.Properties):
			remote.Properties = desired.Properties
			remote, err = apiClient.UpdatePrincipalRole(ctx, remote)
		}
	}
	if err != nil {
		return r.principalRoleFailure(ctx, role, "ReconcileFailed", err)
	}

	now := metav1.Now()
	role.Status.ObservedGeneration = role.Generation
	role.Status.EntityVersion = remote.EntityVersion
	role.Status.LastReconciledTime = &now
	meta.SetStatusCondition(&role.Status.Conditions, metav1.Condition{
		Type: principalRoleReadyConditionType, Status: metav1.ConditionTrue, ObservedGeneration: role.Generation,
		LastTransitionTime: now, Reason: "PrincipalRoleReady", Message: "Polaris principal role matches the desired state",
	})
	if err := r.Status().Update(ctx, role); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: principalRoleRequeueAfter}, nil
}

func desiredPrincipalRole(role *polarisv1alpha1.PolarisPrincipalRole) polarisclient.PrincipalRole {
	return polarisclient.PrincipalRole{
		Name: role.Spec.RoleName, Federated: role.Spec.Federated, Properties: role.Spec.Properties,
	}
}

func (r *PolarisPrincipalRoleReconciler) principalRoleFailure(ctx context.Context, role *polarisv1alpha1.PolarisPrincipalRole, reason string, cause error) (ctrl.Result, error) {
	meta.SetStatusCondition(&role.Status.Conditions, metav1.Condition{
		Type: principalRoleReadyConditionType, Status: metav1.ConditionFalse, ObservedGeneration: role.Generation,
		LastTransitionTime: metav1.Now(), Reason: reason, Message: cause.Error(),
	})
	if err := r.Status().Update(ctx, role); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: principalRoleRequeueAfter}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PolarisPrincipalRoleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisPrincipalRole{}).
		Watches(&polarisv1alpha1.PolarisConnection{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			roles := &polarisv1alpha1.PolarisPrincipalRoleList{}
			if err := r.List(ctx, roles, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0, len(roles.Items))
			for i := range roles.Items {
				if roles.Items[i].Spec.ConnectionRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
						Namespace: roles.Items[i].Namespace, Name: roles.Items[i].Name,
					}})
				}
			}
			return requests
		})).
		Named("polarisprincipalrole").
		Complete(r)
}
