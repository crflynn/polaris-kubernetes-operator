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
	"fmt"
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
	catalogRoleGrantFinalizer          = "polaris.apache.org/catalog-role-grant-cleanup"
	catalogRoleGrantReadyConditionType = readyConditionType
	catalogRoleGrantRequeueAfter       = 30 * time.Second
	catalogGrantType                   = "catalog"
	namespaceGrantType                 = "namespace"
)

// PolarisCatalogRoleGrantReconciler reconciles one privilege grant.
type PolarisCatalogRoleGrantReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogrolegrants,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogrolegrants/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogrolegrants/finalizers,verbs=update
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogroles,verbs=get;list;watch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisconnections,verbs=get;list;watch
func (r *PolarisCatalogRoleGrantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	grantResource := &polarisv1alpha1.PolarisCatalogRoleGrant{}
	if err := r.Get(ctx, req.NamespacedName, grantResource); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	deleting := !grantResource.DeletionTimestamp.IsZero()
	if deleting && !controllerutil.ContainsFinalizer(grantResource, catalogRoleGrantFinalizer) {
		return ctrl.Result{}, nil
	}
	if !deleting && !controllerutil.ContainsFinalizer(grantResource, catalogRoleGrantFinalizer) {
		controllerutil.AddFinalizer(grantResource, catalogRoleGrantFinalizer)
		if err := r.Update(ctx, grantResource); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	if result, handled, err := r.initializeCatalogRoleGrant(ctx, grantResource, deleting); handled {
		return result, err
	}
	if result, handled, err := r.validateObservedCatalogRoleGrant(ctx, grantResource, deleting); handled {
		return result, err
	}

	connection := &polarisv1alpha1.PolarisConnection{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: grantResource.Namespace, Name: grantResource.Status.ObservedConnectionRef}, connection); err != nil {
		return r.catalogRoleGrantFailure(ctx, grantResource, "ConnectionNotFound", errors.New("grant's recorded PolarisConnection was not found"))
	}
	apiClient, err := (&PolarisConnectionReconciler{Client: r.Client}).newClient(ctx, connection)
	if err != nil {
		return r.catalogRoleGrantFailure(ctx, grantResource, "ConnectionUnavailable", err)
	}
	if grantResource.Status.ObservedGrant == nil {
		return r.catalogRoleGrantFailure(ctx, grantResource, "GrantIdentityUnknown", errors.New("grant provenance is missing"))
	}
	grant := toPolarisGrant(*grantResource.Status.ObservedGrant)
	if deleting {
		return r.reconcileCatalogRoleGrantDeletion(ctx, grantResource, apiClient, grant)
	}

	grants, err := apiClient.ListCatalogRoleGrants(ctx, grantResource.Status.ObservedCatalogName, grantResource.Status.ObservedCatalogRoleName)
	if err != nil {
		return r.catalogRoleGrantFailure(ctx, grantResource, "GrantReadFailed", err)
	}
	if !containsGrant(grants, grant) {
		if err := apiClient.AddCatalogRoleGrant(ctx, grantResource.Status.ObservedCatalogName,
			grantResource.Status.ObservedCatalogRoleName, grant); err != nil {
			return r.catalogRoleGrantFailure(ctx, grantResource, "GrantFailed", err)
		}
	}

	now := metav1.Now()
	grantResource.Status.ObservedGeneration = grantResource.Generation
	grantResource.Status.LastReconciledTime = &now
	meta.SetStatusCondition(&grantResource.Status.Conditions, metav1.Condition{
		Type: catalogRoleGrantReadyConditionType, Status: metav1.ConditionTrue, ObservedGeneration: grantResource.Generation,
		LastTransitionTime: now, Reason: "GrantPresent", Message: "Polaris catalog role holds the requested privilege",
	})
	if err := r.Status().Update(ctx, grantResource); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: catalogRoleGrantRequeueAfter}, nil
}

func (r *PolarisCatalogRoleGrantReconciler) initializeCatalogRoleGrant(
	ctx context.Context,
	grantResource *polarisv1alpha1.PolarisCatalogRoleGrant,
	deleting bool,
) (ctrl.Result, bool, error) {
	if grantResource.Status.ObservedCatalogRoleName != "" {
		return ctrl.Result{}, false, nil
	}
	if deleting {
		controllerutil.RemoveFinalizer(grantResource, catalogRoleGrantFinalizer)
		return ctrl.Result{}, true, r.Update(ctx, grantResource)
	}
	role := &polarisv1alpha1.PolarisCatalogRole{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: grantResource.Namespace, Name: grantResource.Spec.CatalogRoleRef}, role); err != nil {
		result, failureErr := r.catalogRoleGrantFailure(ctx, grantResource, "CatalogRoleNotFound", errors.New("referenced PolarisCatalogRole was not found"))
		return result, true, failureErr
	}
	if !meta.IsStatusConditionTrue(role.Status.Conditions, readyConditionType) {
		result, failureErr := r.catalogRoleGrantFailure(ctx, grantResource, "CatalogRoleNotReady", errors.New("referenced PolarisCatalogRole must be Ready before granting privileges"))
		return result, true, failureErr
	}
	if role.Status.ObservedCatalogName == "" || role.Status.ObservedRoleName == "" || role.Status.ObservedConnectionRef == "" {
		result, failureErr := r.catalogRoleGrantFailure(ctx, grantResource, "CatalogRoleIdentityUnknown", errors.New("catalog role has no recorded remote identity"))
		return result, true, failureErr
	}
	if err := validateGrant(grantResource.Spec.Grant); err != nil {
		result, failureErr := r.catalogRoleGrantFailure(ctx, grantResource, "InvalidGrant", err)
		return result, true, failureErr
	}
	grantResource.Status.ObservedCatalogRoleRef = grantResource.Spec.CatalogRoleRef
	grantResource.Status.ObservedCatalogName = role.Status.ObservedCatalogName
	grantResource.Status.ObservedCatalogRoleName = role.Status.ObservedRoleName
	grantResource.Status.ObservedConnectionRef = role.Status.ObservedConnectionRef
	observedGrant := grantResource.Spec.Grant
	grantResource.Status.ObservedGrant = &observedGrant
	if err := r.Status().Update(ctx, grantResource); err != nil {
		return ctrl.Result{}, true, err
	}
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

func (r *PolarisCatalogRoleGrantReconciler) validateObservedCatalogRoleGrant(
	ctx context.Context,
	grantResource *polarisv1alpha1.PolarisCatalogRoleGrant,
	deleting bool,
) (ctrl.Result, bool, error) {
	if deleting {
		return ctrl.Result{}, false, nil
	}
	role := &polarisv1alpha1.PolarisCatalogRole{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: grantResource.Namespace, Name: grantResource.Spec.CatalogRoleRef}, role); err != nil {
		result, failureErr := r.catalogRoleGrantFailure(ctx, grantResource, "CatalogRoleNotFound", errors.New("referenced PolarisCatalogRole was not found"))
		return result, true, failureErr
	}
	if !meta.IsStatusConditionTrue(role.Status.Conditions, readyConditionType) {
		result, failureErr := r.catalogRoleGrantFailure(ctx, grantResource, "CatalogRoleNotReady", errors.New("referenced PolarisCatalogRole must remain Ready"))
		return result, true, failureErr
	}
	if grantResource.Spec.CatalogRoleRef != grantResource.Status.ObservedCatalogRoleRef ||
		role.Status.ObservedCatalogName != grantResource.Status.ObservedCatalogName ||
		role.Status.ObservedRoleName != grantResource.Status.ObservedCatalogRoleName ||
		role.Status.ObservedConnectionRef != grantResource.Status.ObservedConnectionRef {
		result, failureErr := r.catalogRoleGrantFailure(ctx, grantResource, "IdentityChanged",
			errors.New("catalog role identity changed; restore its recorded identity or delete this grant"))
		return result, true, failureErr
	}
	if grantResource.Status.ObservedGrant == nil || !reflect.DeepEqual(grantResource.Spec.Grant, *grantResource.Status.ObservedGrant) {
		result, failureErr := r.catalogRoleGrantFailure(ctx, grantResource, "GrantIdentityChanged",
			errors.New("grant identity is immutable; delete this resource and create a new grant"))
		return result, true, failureErr
	}
	if err := validateGrant(grantResource.Spec.Grant); err != nil {
		result, failureErr := r.catalogRoleGrantFailure(ctx, grantResource, "InvalidGrant", err)
		return result, true, failureErr
	}
	return ctrl.Result{}, false, nil
}
func (r *PolarisCatalogRoleGrantReconciler) reconcileCatalogRoleGrantDeletion(
	ctx context.Context,
	grantResource *polarisv1alpha1.PolarisCatalogRoleGrant,
	apiClient *polarisclient.Client,
	grant polarisclient.Grant,
) (ctrl.Result, error) {
	if !grantResource.Spec.Cascade {
		grants, err := apiClient.ListCatalogRoleGrants(ctx, grantResource.Status.ObservedCatalogName, grantResource.Status.ObservedCatalogRoleName)
		if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil && ok && httpErr.StatusCode == http.StatusNotFound {
			grants = nil
			err = nil
		}
		if err != nil {
			return r.catalogRoleGrantFailure(ctx, grantResource, "GrantReadFailed", err)
		}
		if !containsGrant(grants, grant) {
			controllerutil.RemoveFinalizer(grantResource, catalogRoleGrantFinalizer)
			return ctrl.Result{}, r.Update(ctx, grantResource)
		}
	}
	err := apiClient.RevokeCatalogRoleGrant(ctx, grantResource.Status.ObservedCatalogName,
		grantResource.Status.ObservedCatalogRoleName, grant, grantResource.Spec.Cascade)
	if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil {
		if !ok || httpErr.StatusCode != http.StatusNotFound {
			return r.catalogRoleGrantFailure(ctx, grantResource, "RevokeFailed", err)
		}
	}
	controllerutil.RemoveFinalizer(grantResource, catalogRoleGrantFinalizer)
	return ctrl.Result{}, r.Update(ctx, grantResource)
}

func validateGrant(grant polarisv1alpha1.PolarisGrantSpec) error {
	if grant.Privilege == "" {
		return errors.New("grant privilege must not be empty")
	}
	switch grant.Type {
	case catalogGrantType:
		if len(grant.Namespace) != 0 || grant.TableName != "" || grant.ViewName != "" || grant.PolicyName != "" {
			return errors.New("catalog grants cannot specify a namespace or child resource name")
		}
	case namespaceGrantType:
		if err := validateGrantNamespace(grant.Namespace); err != nil {
			return err
		}
		if grant.TableName != "" || grant.ViewName != "" || grant.PolicyName != "" {
			return errors.New("namespace grants cannot specify a child resource name")
		}
	case "table":
		if err := validateGrantNamespace(grant.Namespace); err != nil {
			return err
		}
		if grant.TableName == "" || grant.ViewName != "" || grant.PolicyName != "" {
			return errors.New("table grants require only tableName in addition to namespace")
		}
	case "view":
		if err := validateGrantNamespace(grant.Namespace); err != nil {
			return err
		}
		if grant.ViewName == "" || grant.TableName != "" || grant.PolicyName != "" {
			return errors.New("view grants require only viewName in addition to namespace")
		}
	case "policy":
		if err := validateGrantNamespace(grant.Namespace); err != nil {
			return err
		}
		if grant.PolicyName == "" || grant.TableName != "" || grant.ViewName != "" {
			return errors.New("policy grants require only policyName in addition to namespace")
		}
	default:
		return fmt.Errorf("unsupported grant type %q", grant.Type)
	}
	return nil
}

func validateGrantNamespace(namespace []string) error {
	if len(namespace) == 0 {
		return errors.New("namespace path is required for this grant type")
	}
	if slices.Contains(namespace, "") {
		return errors.New("namespace path segments must not be empty")
	}
	return nil
}

func toPolarisGrant(grant polarisv1alpha1.PolarisGrantSpec) polarisclient.Grant {
	return polarisclient.Grant{
		Type: grant.Type, Namespace: grant.Namespace, TableName: grant.TableName, ViewName: grant.ViewName,
		PolicyName: grant.PolicyName, Privilege: grant.Privilege,
	}
}

func containsGrant(grants []polarisclient.Grant, desired polarisclient.Grant) bool {
	for i := range grants {
		if reflect.DeepEqual(grants[i], desired) {
			return true
		}
	}
	return false
}

func (r *PolarisCatalogRoleGrantReconciler) catalogRoleGrantFailure(ctx context.Context, grant *polarisv1alpha1.PolarisCatalogRoleGrant, reason string, cause error) (ctrl.Result, error) {
	meta.SetStatusCondition(&grant.Status.Conditions, metav1.Condition{
		Type: catalogRoleGrantReadyConditionType, Status: metav1.ConditionFalse, ObservedGeneration: grant.Generation,
		LastTransitionTime: metav1.Now(), Reason: reason, Message: cause.Error(),
	})
	if err := r.Status().Update(ctx, grant); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: catalogRoleGrantRequeueAfter}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PolarisCatalogRoleGrantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisCatalogRoleGrant{}).
		Watches(&polarisv1alpha1.PolarisCatalogRole{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			grants := &polarisv1alpha1.PolarisCatalogRoleGrantList{}
			if err := r.List(ctx, grants, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0, len(grants.Items))
			for i := range grants.Items {
				if grants.Items[i].Spec.CatalogRoleRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
						Namespace: grants.Items[i].Namespace, Name: grants.Items[i].Name,
					}})
				}
			}
			return requests
		})).
		Watches(&polarisv1alpha1.PolarisConnection{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			grants := &polarisv1alpha1.PolarisCatalogRoleGrantList{}
			if err := r.List(ctx, grants, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0, len(grants.Items))
			for i := range grants.Items {
				if grants.Items[i].Status.ObservedConnectionRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
						Namespace: grants.Items[i].Namespace, Name: grants.Items[i].Name,
					}})
				}
			}
			return requests
		})).
		Named("polariscatalogrolegrant").
		Complete(r)
}
