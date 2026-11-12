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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	polarisTableFinalizer    = "polaris.apache.org/table-protection"
	polarisTableRequeueAfter = 30 * time.Second
)

// PolarisTableReconciler observes or registers one Iceberg table in Polaris.
type PolarisTableReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polaristables,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polaristables/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polaristables/finalizers,verbs=update
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogs;polarisconnections,verbs=get;list;watch
func (r *PolarisTableReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	table := &polarisv1alpha1.PolarisTable{}
	if err := r.Get(ctx, req.NamespacedName, table); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	catalog := &polarisv1alpha1.PolarisCatalog{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: table.Namespace, Name: table.Spec.CatalogRef}, catalog); err != nil {
		if apierrors.IsNotFound(err) {
			return r.tableFailure(ctx, table, "CatalogNotFound", errors.New("referenced PolarisCatalog was not found"))
		}
		return ctrl.Result{}, err
	}
	if !meta.IsStatusConditionTrue(catalog.Status.Conditions, catalogReadyConditionType) {
		return r.tableFailure(ctx, table, "CatalogNotReady", errors.New("referenced PolarisCatalog is not Ready"))
	}

	connection := &polarisv1alpha1.PolarisConnection{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: table.Namespace, Name: catalog.Spec.ConnectionRef}, connection); err != nil {
		if apierrors.IsNotFound(err) {
			return r.tableFailure(ctx, table, "ConnectionNotFound", errors.New("referenced PolarisConnection was not found"))
		}
		return ctrl.Result{}, err
	}
	if !meta.IsStatusConditionTrue(connection.Status.Conditions, polarisv1alpha1.PolarisConnectionReadyConditionType) {
		return r.tableFailure(ctx, table, "ConnectionNotReady", errors.New("referenced PolarisConnection is not Ready"))
	}
	apiClient, err := (&PolarisConnectionReconciler{Client: r.Client}).newClient(ctx, connection)
	if err != nil {
		return r.tableFailure(ctx, table, "ConnectionUnavailable", err)
	}

	mode := table.Spec.OwnershipMode
	if mode == "" {
		mode = polarisv1alpha1.PolarisOwnershipObserve
	}
	deletionPolicy := table.Spec.DeletionPolicy
	if deletionPolicy == "" {
		deletionPolicy = polarisv1alpha1.PolarisDeletionOrphan
	}
	if !table.DeletionTimestamp.IsZero() {
		return r.reconcileTableDelete(ctx, apiClient, catalog, table, mode, deletionPolicy)
	}
	if mode == polarisv1alpha1.PolarisOwnershipEnforce && deletionPolicy != polarisv1alpha1.PolarisDeletionOrphan {
		if !controllerutil.ContainsFinalizer(table, polarisTableFinalizer) {
			controllerutil.AddFinalizer(table, polarisTableFinalizer)
			if err := r.Update(ctx, table); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
	} else if controllerutil.ContainsFinalizer(table, polarisTableFinalizer) {
		controllerutil.RemoveFinalizer(table, polarisTableFinalizer)
		if err := r.Update(ctx, table); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if mode != polarisv1alpha1.PolarisOwnershipObserve && table.Status.RemoteCatalogName != "" &&
		(table.Status.RemoteCatalogName != catalog.Spec.CatalogName || !slices.Equal(table.Status.RemoteNamespace, table.Spec.Namespace) || table.Status.RemoteTableName != table.Spec.TableName) {
		return r.tableFailure(ctx, table, "IdentityChanged", errors.New("catalog or table identity cannot change after adoption or enforcement"))
	}

	remote, err := apiClient.GetTable(ctx, catalog.Spec.CatalogName, table.Spec.Namespace, table.Spec.TableName)
	if isHTTPStatus(err, http.StatusNotFound) {
		if mode != polarisv1alpha1.PolarisOwnershipEnforce {
			return r.tableFailure(ctx, table, "RemoteTableNotFound", errors.New("table is absent in Polaris and ownershipMode does not permit registration"))
		}
		remote, err = apiClient.RegisterTable(ctx, catalog.Spec.CatalogName, table.Spec.Namespace, table.Spec.TableName, table.Spec.MetadataLocation)
		if isHTTPStatus(err, http.StatusConflict) {
			remote, err = apiClient.GetTable(ctx, catalog.Spec.CatalogName, table.Spec.Namespace, table.Spec.TableName)
		}
	}
	if err != nil {
		return r.tableFailure(ctx, table, "TableReadOrRegisterFailed", err)
	}

	var drifted []string
	if table.Spec.MetadataLocation != "" && remote.MetadataLocation != table.Spec.MetadataLocation {
		drifted = []string{"metadataLocation"}
	}
	return r.tableSuccess(ctx, table, catalog.Spec.CatalogName, remote.MetadataLocation, drifted)
}

func (r *PolarisTableReconciler) reconcileTableDelete(ctx context.Context, apiClient *polarisclient.Client, catalog *polarisv1alpha1.PolarisCatalog, table *polarisv1alpha1.PolarisTable, mode polarisv1alpha1.PolarisOwnershipMode, deletionPolicy polarisv1alpha1.PolarisDeletionPolicy) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(table, polarisTableFinalizer) {
		return ctrl.Result{}, nil
	}
	if mode != polarisv1alpha1.PolarisOwnershipEnforce || deletionPolicy == polarisv1alpha1.PolarisDeletionOrphan {
		controllerutil.RemoveFinalizer(table, polarisTableFinalizer)
		return ctrl.Result{}, r.Update(ctx, table)
	}
	var err error
	if deletionPolicy == polarisv1alpha1.PolarisDeletionUnregister {
		err = apiClient.UnregisterTable(ctx, catalog.Spec.CatalogName, table.Spec.Namespace, table.Spec.TableName)
	} else {
		err = apiClient.DeleteTable(ctx, catalog.Spec.CatalogName, table.Spec.Namespace, table.Spec.TableName)
	}
	if err != nil && !isHTTPStatus(err, http.StatusNotFound) {
		return r.tableFailure(ctx, table, "TableDeleteFailed", err)
	}
	controllerutil.RemoveFinalizer(table, polarisTableFinalizer)
	return ctrl.Result{}, r.Update(ctx, table)
}

func (r *PolarisTableReconciler) tableSuccess(ctx context.Context, table *polarisv1alpha1.PolarisTable, catalogName, metadataLocation string, drifted []string) (ctrl.Result, error) {
	now := metav1.Now()
	table.Status.ObservedGeneration = table.Generation
	table.Status.RemoteCatalogName = catalogName
	table.Status.RemoteNamespace = slices.Clone(table.Spec.Namespace)
	table.Status.RemoteTableName = table.Spec.TableName
	table.Status.RemoteMetadataLocation = metadataLocation
	table.Status.DriftedFields = slices.Clone(drifted)
	table.Status.LastReconciledTime = &now
	meta.SetStatusCondition(&table.Status.Conditions, metav1.Condition{Type: readyConditionType, Status: metav1.ConditionTrue, ObservedGeneration: table.Generation, LastTransitionTime: now, Reason: "TableObserved", Message: "Polaris table registration was observed"})
	driftStatus, driftReason := metav1.ConditionFalse, noDriftReason
	driftMessage := "Polaris table registration was observed with no metadataLocation constraint"
	if table.Spec.MetadataLocation != "" {
		driftMessage = "Polaris table registration matches the requested metadata location"
	}
	if len(drifted) > 0 {
		driftStatus, driftReason, driftMessage = metav1.ConditionTrue, driftDetectedReason, fmt.Sprintf("Polaris table registration differs in %v; the Iceberg metadata commit remains owned by the table engine", drifted)
	}
	meta.SetStatusCondition(&table.Status.Conditions, metav1.Condition{Type: driftedConditionType, Status: driftStatus, ObservedGeneration: table.Generation, LastTransitionTime: now, Reason: driftReason, Message: driftMessage})
	if err := r.Status().Update(ctx, table); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: polarisTableRequeueAfter}, nil
}

func (r *PolarisTableReconciler) tableFailure(ctx context.Context, table *polarisv1alpha1.PolarisTable, reason string, cause error) (ctrl.Result, error) {
	meta.SetStatusCondition(&table.Status.Conditions, metav1.Condition{Type: readyConditionType, Status: metav1.ConditionFalse, ObservedGeneration: table.Generation, LastTransitionTime: metav1.Now(), Reason: reason, Message: sanitizeErrorMessage(cause)})
	table.Status.ObservedGeneration = table.Generation
	if err := r.Status().Update(ctx, table); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: polarisTableRequeueAfter}, nil
}

// SetupWithManager watches table specs and their catalog and connection dependencies.
func (r *PolarisTableReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisTable{}).
		Watches(&polarisv1alpha1.PolarisCatalog{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			resources := &polarisv1alpha1.PolarisTableList{}
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
			catalogNames := make(map[string]struct{})
			for i := range catalogs.Items {
				if catalogs.Items[i].Spec.ConnectionRef == object.GetName() {
					catalogNames[catalogs.Items[i].Name] = struct{}{}
				}
			}
			resources := &polarisv1alpha1.PolarisTableList{}
			if err := r.List(ctx, resources, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0)
			for i := range resources.Items {
				if _, ok := catalogNames[resources.Items[i].Spec.CatalogRef]; ok {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: resources.Items[i].Namespace, Name: resources.Items[i].Name}})
				}
			}
			return requests
		})).
		Named("polaristable").
		Complete(r)
}
