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
	"slices"
	"time"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
	polarisclient "github.com/apache/polaris-kubernetes-operator/internal/polaris"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	genericTableFinalizer    = "polaris.apache.org/generic-table-cleanup"
	genericTableRequeueAfter = 30 * time.Second
)

// PolarisGenericTableReconciler reconciles a PolarisGenericTable object.
type PolarisGenericTableReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisgenerictables,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisgenerictables/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisgenerictables/finalizers,verbs=update
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogs,verbs=get;list;watch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisconnections,verbs=get;list;watch
func (r *PolarisGenericTableReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	table := &polarisv1alpha1.PolarisGenericTable{}
	if err := r.Get(ctx, req.NamespacedName, table); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	mode := genericTableOwnershipMode(table)
	deletionPolicy := genericTableDeletionPolicy(table)
	if !table.DeletionTimestamp.IsZero() {
		return r.reconcileGenericTableDelete(ctx, table, mode, deletionPolicy)
	}
	if mode == polarisv1alpha1.PolarisOwnershipEnforce && deletionPolicy == polarisv1alpha1.PolarisDeletionDelete {
		if !controllerutil.ContainsFinalizer(table, genericTableFinalizer) {
			controllerutil.AddFinalizer(table, genericTableFinalizer)
			if err := r.Update(ctx, table); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
	} else if controllerutil.ContainsFinalizer(table, genericTableFinalizer) {
		controllerutil.RemoveFinalizer(table, genericTableFinalizer)
		if err := r.Update(ctx, table); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	catalog := &polarisv1alpha1.PolarisCatalog{}
	catalogKey := types.NamespacedName{Namespace: table.Namespace, Name: table.Spec.CatalogRef}
	if err := r.Get(ctx, catalogKey, catalog); err != nil {
		if apierrors.IsNotFound(err) {
			return r.genericTableFailure(ctx, table, "CatalogNotFound", errors.New("referenced PolarisCatalog was not found"), true)
		}
		return ctrl.Result{}, err
	}
	if !meta.IsStatusConditionTrue(catalog.Status.Conditions, "Ready") {
		return r.genericTableFailure(ctx, table, "CatalogNotReady", errors.New("referenced PolarisCatalog is not Ready"), true)
	}

	connection := &polarisv1alpha1.PolarisConnection{}
	connectionKey := types.NamespacedName{Namespace: table.Namespace, Name: catalog.Spec.ConnectionRef}
	if err := r.Get(ctx, connectionKey, connection); err != nil {
		if apierrors.IsNotFound(err) {
			return r.genericTableFailure(ctx, table, "ConnectionNotFound", errors.New("referenced PolarisConnection was not found"), true)
		}
		return ctrl.Result{}, err
	}
	if !meta.IsStatusConditionTrue(connection.Status.Conditions, polarisv1alpha1.PolarisConnectionReadyConditionType) {
		return r.genericTableFailure(ctx, table, "ConnectionNotReady", errors.New("referenced PolarisConnection is not Ready"), true)
	}
	if !connection.Spec.EnableBetaGenericTables {
		return r.genericTableFailure(ctx, table, "BetaFeatureDisabled", errors.New("Polaris Generic Table API is beta and must be explicitly enabled on PolarisConnection.spec.enableBetaGenericTables"), false)
	}
	if connection.Status.CatalogURL == "" {
		return r.genericTableFailure(ctx, table, "Unsupported", polarisclient.ErrUnsupported, false)
	}

	connectionReconciler := &PolarisConnectionReconciler{Client: r.Client}
	apiClient, err := connectionReconciler.newClient(ctx, connection)
	if err != nil {
		return r.genericTableFailure(ctx, table, "ConnectionUnavailable", err, true)
	}
	genericTables := polarisclient.NewGenericTableClient(apiClient)
	if mode != polarisv1alpha1.PolarisOwnershipObserve && table.Status.RemoteCatalogName != "" &&
		(table.Status.RemoteCatalogName != catalog.Spec.CatalogName ||
			!slices.Equal(table.Status.RemoteNamespace, table.Spec.Namespace) ||
			table.Status.RemoteTableName != table.Spec.TableName) {
		return r.genericTableFailure(ctx, table, "IdentityChanged", errors.New("catalog and generic-table identity cannot change after adoption or enforcement"), false)
	}

	remote, err := genericTables.GetTable(ctx, catalog.Spec.CatalogName, table.Spec.Namespace, table.Spec.TableName)
	if isHTTPStatus(err, http.StatusNotFound) {
		if mode != polarisv1alpha1.PolarisOwnershipEnforce {
			return r.genericTableFailure(ctx, table, "RemoteTableNotFound", errors.New("generic table is absent in Polaris and ownershipMode does not permit creation"), true)
		}
		remote, err = genericTables.CreateTable(ctx, catalog.Spec.CatalogName, table.Spec.Namespace, polarisclient.GenericTable{
			Name: table.Spec.TableName, Format: table.Spec.Format, BaseLocation: table.Spec.BaseLocation,
			Doc: table.Spec.Doc, Properties: table.Spec.Properties,
		})
		if isHTTPStatus(err, http.StatusConflict) {
			remote, err = genericTables.GetTable(ctx, catalog.Spec.CatalogName, table.Spec.Namespace, table.Spec.TableName)
		}
		if err != nil {
			return r.genericTableFailure(ctx, table, "TableCreateFailed", err, true)
		}
	} else if err != nil {
		reason := "TableReadFailed"
		if errors.Is(err, polarisclient.ErrUnsupported) {
			reason = "Unsupported"
		}
		return r.genericTableFailure(ctx, table, reason, err, reason != "Unsupported")
	}
	if remote.Name != table.Spec.TableName {
		return r.genericTableFailure(ctx, table, "RemoteIdentityMismatch", errors.New("Polaris returned a different generic-table name"), false)
	}

	drifted := genericTableDrift(table.Spec, remote)
	return r.genericTableSuccess(ctx, table, catalog.Spec.CatalogName, remote, drifted)
}

func (r *PolarisGenericTableReconciler) reconcileGenericTableDelete(
	ctx context.Context,
	table *polarisv1alpha1.PolarisGenericTable,
	mode polarisv1alpha1.PolarisOwnershipMode,
	deletionPolicy polarisv1alpha1.PolarisDeletionPolicy,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(table, genericTableFinalizer) {
		return ctrl.Result{}, nil
	}
	if mode == polarisv1alpha1.PolarisOwnershipEnforce && deletionPolicy == polarisv1alpha1.PolarisDeletionDelete {
		catalog := &polarisv1alpha1.PolarisCatalog{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: table.Namespace, Name: table.Spec.CatalogRef}, catalog); err != nil {
			if apierrors.IsNotFound(err) {
				return r.genericTableFailure(ctx, table, "CatalogNotFound", errors.New("referenced PolarisCatalog was not found during finalization"), true)
			}
			return ctrl.Result{}, err
		}
		connection := &polarisv1alpha1.PolarisConnection{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: table.Namespace, Name: catalog.Spec.ConnectionRef}, connection); err != nil {
			if apierrors.IsNotFound(err) {
				return r.genericTableFailure(ctx, table, "ConnectionNotFound", errors.New("referenced PolarisConnection was not found during finalization"), true)
			}
			return ctrl.Result{}, err
		}
		if !meta.IsStatusConditionTrue(catalog.Status.Conditions, "Ready") || !meta.IsStatusConditionTrue(connection.Status.Conditions, polarisv1alpha1.PolarisConnectionReadyConditionType) {
			return r.genericTableFailure(ctx, table, "ConnectionNotReady", errors.New("catalog or connection is not Ready during finalization"), true)
		}
		if connection.Status.CatalogURL == "" {
			return r.genericTableFailure(ctx, table, "Unsupported", polarisclient.ErrUnsupported, false)
		}
		connectionReconciler := &PolarisConnectionReconciler{Client: r.Client}
		apiClient, err := connectionReconciler.newClient(ctx, connection)
		if err != nil {
			return r.genericTableFailure(ctx, table, "ConnectionUnavailable", err, true)
		}
		err = polarisclient.NewGenericTableClient(apiClient).DeleteTable(ctx, catalog.Spec.CatalogName, table.Spec.Namespace, table.Spec.TableName)
		if err != nil && !isHTTPStatus(err, http.StatusNotFound) {
			reason := "TableDeleteFailed"
			if errors.Is(err, polarisclient.ErrUnsupported) {
				reason = "Unsupported"
			}
			return r.genericTableFailure(ctx, table, reason, err, reason != "Unsupported")
		}
	}
	controllerutil.RemoveFinalizer(table, genericTableFinalizer)
	return ctrl.Result{}, r.Update(ctx, table)
}

func genericTableOwnershipMode(table *polarisv1alpha1.PolarisGenericTable) polarisv1alpha1.PolarisOwnershipMode {
	if table.Spec.OwnershipMode == "" {
		return polarisv1alpha1.PolarisOwnershipObserve
	}
	return table.Spec.OwnershipMode
}

func genericTableDeletionPolicy(table *polarisv1alpha1.PolarisGenericTable) polarisv1alpha1.PolarisDeletionPolicy {
	if table.Spec.DeletionPolicy == "" {
		return polarisv1alpha1.PolarisDeletionOrphan
	}
	return table.Spec.DeletionPolicy
}

func genericTableDrift(desired polarisv1alpha1.PolarisGenericTableSpec, actual polarisclient.GenericTable) []string {
	var drifted []string
	if desired.Format != actual.Format {
		drifted = append(drifted, "format")
	}
	if desired.BaseLocation != "" && desired.BaseLocation != actual.BaseLocation {
		drifted = append(drifted, "baseLocation")
	}
	if desired.Doc != "" && desired.Doc != actual.Doc {
		drifted = append(drifted, "doc")
	}
	if desired.Properties != nil && !mapsEqual(desired.Properties, actual.Properties) {
		drifted = append(drifted, "properties")
	}
	return drifted
}

func mapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		other, ok := right[key]
		if !ok || other != value {
			return false
		}
	}
	return true
}

func (r *PolarisGenericTableReconciler) genericTableSuccess(
	ctx context.Context,
	table *polarisv1alpha1.PolarisGenericTable,
	catalogName string,
	remote polarisclient.GenericTable,
	drifted []string,
) (ctrl.Result, error) {
	now := metav1.Now()
	table.Status.ObservedGeneration = table.Generation
	table.Status.RemoteCatalogName = catalogName
	table.Status.RemoteNamespace = slices.Clone(table.Spec.Namespace)
	table.Status.RemoteTableName = remote.Name
	table.Status.RemoteFormat = remote.Format
	table.Status.RemoteBaseLocation = remote.BaseLocation
	table.Status.DriftedFields = slices.Clone(drifted)
	table.Status.LastReconciledTime = &now
	meta.SetStatusCondition(&table.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, ObservedGeneration: table.Generation,
		LastTransitionTime: now, Reason: "TableObserved", Message: "Polaris generic table was reconciled",
	})
	driftStatus, driftReason, driftMessage := metav1.ConditionFalse, "NoDrift", "Polaris generic table matches desired fields"
	if len(drifted) > 0 {
		driftStatus, driftReason, driftMessage = metav1.ConditionTrue, "DriftDetected", fmt.Sprintf("Polaris generic table differs in %v", drifted)
	}
	meta.SetStatusCondition(&table.Status.Conditions, metav1.Condition{
		Type: "Drifted", Status: driftStatus, ObservedGeneration: table.Generation,
		LastTransitionTime: now, Reason: driftReason, Message: driftMessage,
	})
	if err := r.Status().Update(ctx, table); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: genericTableRequeueAfter}, nil
}

func (r *PolarisGenericTableReconciler) genericTableFailure(
	ctx context.Context,
	table *polarisv1alpha1.PolarisGenericTable,
	reason string,
	cause error,
	requeue bool,
) (ctrl.Result, error) {
	meta.SetStatusCondition(&table.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, ObservedGeneration: table.Generation,
		LastTransitionTime: metav1.Now(), Reason: reason, Message: sanitizeErrorMessage(cause),
	})
	table.Status.ObservedGeneration = table.Generation
	if err := r.Status().Update(ctx, table); err != nil {
		return ctrl.Result{}, err
	}
	logf.FromContext(ctx).Error(cause, "Polaris generic table reconciliation failed", "name", table.Name)
	if requeue {
		return ctrl.Result{RequeueAfter: genericTableRequeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller and watches its catalog dependencies.
func (r *PolarisGenericTableReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisGenericTable{}).
		Watches(&polarisv1alpha1.PolarisCatalog{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			resources := &polarisv1alpha1.PolarisGenericTableList{}
			if err := r.List(ctx, resources, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0)
			for i := range resources.Items {
				if resources.Items[i].Spec.CatalogRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: resources.Items[i].Namespace, Name: resources.Items[i].Name}})
				}
			}
			return requests
		})).
		Watches(&polarisv1alpha1.PolarisConnection{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			catalogs := &polarisv1alpha1.PolarisCatalogList{}
			if err := r.List(ctx, catalogs, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			catalogRefs := make(map[string]struct{})
			for i := range catalogs.Items {
				if catalogs.Items[i].Spec.ConnectionRef == object.GetName() {
					catalogRefs[catalogs.Items[i].Name] = struct{}{}
				}
			}
			resources := &polarisv1alpha1.PolarisGenericTableList{}
			if err := r.List(ctx, resources, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0)
			for i := range resources.Items {
				if _, ok := catalogRefs[resources.Items[i].Spec.CatalogRef]; ok {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: resources.Items[i].Namespace, Name: resources.Items[i].Name}})
				}
			}
			return requests
		})).
		Named("polarisgenerictable").
		Complete(r)
}
