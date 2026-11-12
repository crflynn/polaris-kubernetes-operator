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
	polarisViewFinalizer    = "polaris.apache.org/view-protection"
	polarisViewRequeueAfter = 30 * time.Second
)

// PolarisViewReconciler observes or registers one Iceberg view in Polaris.
type PolarisViewReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisviews,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisviews/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisviews/finalizers,verbs=update
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogs;polarisconnections,verbs=get;list;watch
func (r *PolarisViewReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	view := &polarisv1alpha1.PolarisView{}
	if err := r.Get(ctx, req.NamespacedName, view); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	catalog := &polarisv1alpha1.PolarisCatalog{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: view.Namespace, Name: view.Spec.CatalogRef}, catalog); err != nil {
		if apierrors.IsNotFound(err) {
			return r.viewFailure(ctx, view, "CatalogNotFound", errors.New("referenced PolarisCatalog was not found"))
		}
		return ctrl.Result{}, err
	}
	if !meta.IsStatusConditionTrue(catalog.Status.Conditions, readyConditionType) {
		return r.viewFailure(ctx, view, "CatalogNotReady", errors.New("referenced PolarisCatalog is not Ready"))
	}

	connection := &polarisv1alpha1.PolarisConnection{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: view.Namespace, Name: catalog.Spec.ConnectionRef}, connection); err != nil {
		if apierrors.IsNotFound(err) {
			return r.viewFailure(ctx, view, "ConnectionNotFound", errors.New("referenced PolarisConnection was not found"))
		}
		return ctrl.Result{}, err
	}
	if !meta.IsStatusConditionTrue(connection.Status.Conditions, polarisv1alpha1.PolarisConnectionReadyConditionType) {
		return r.viewFailure(ctx, view, "ConnectionNotReady", errors.New("referenced PolarisConnection is not Ready"))
	}
	apiClient, err := (&PolarisConnectionReconciler{Client: r.Client}).newClient(ctx, connection)
	if err != nil {
		return r.viewFailure(ctx, view, "ConnectionUnavailable", err)
	}

	mode := view.Spec.OwnershipMode
	if mode == "" {
		mode = polarisv1alpha1.PolarisOwnershipObserve
	}
	deletionPolicy := view.Spec.DeletionPolicy
	if deletionPolicy == "" {
		deletionPolicy = polarisv1alpha1.PolarisDeletionOrphan
	}
	if !view.DeletionTimestamp.IsZero() {
		return r.reconcileViewDelete(ctx, apiClient, catalog, view, mode, deletionPolicy)
	}
	if mode == polarisv1alpha1.PolarisOwnershipEnforce && deletionPolicy == polarisv1alpha1.PolarisDeletionDelete {
		if !controllerutil.ContainsFinalizer(view, polarisViewFinalizer) {
			controllerutil.AddFinalizer(view, polarisViewFinalizer)
			if err := r.Update(ctx, view); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
	} else if controllerutil.ContainsFinalizer(view, polarisViewFinalizer) {
		controllerutil.RemoveFinalizer(view, polarisViewFinalizer)
		if err := r.Update(ctx, view); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if mode != polarisv1alpha1.PolarisOwnershipObserve && view.Status.RemoteCatalogName != "" &&
		(view.Status.RemoteCatalogName != catalog.Spec.CatalogName || !slices.Equal(view.Status.RemoteNamespace, view.Spec.Namespace) || view.Status.RemoteViewName != view.Spec.ViewName) {
		return r.viewFailure(ctx, view, "IdentityChanged", errors.New("catalog or view identity cannot change after adoption or enforcement"))
	}

	remote, err := apiClient.GetView(ctx, catalog.Spec.CatalogName, view.Spec.Namespace, view.Spec.ViewName)
	if isHTTPStatus(err, http.StatusNotFound) {
		if mode != polarisv1alpha1.PolarisOwnershipEnforce {
			return r.viewFailure(ctx, view, "RemoteViewNotFound", errors.New("view is absent in Polaris and ownershipMode does not permit registration"))
		}
		remote, err = apiClient.RegisterView(ctx, catalog.Spec.CatalogName, view.Spec.Namespace, view.Spec.ViewName, view.Spec.MetadataLocation)
		if isHTTPStatus(err, http.StatusConflict) {
			remote, err = apiClient.GetView(ctx, catalog.Spec.CatalogName, view.Spec.Namespace, view.Spec.ViewName)
		}
	}
	if err != nil {
		return r.viewFailure(ctx, view, "ViewReadOrRegisterFailed", err)
	}

	var drifted []string
	if view.Spec.MetadataLocation != "" && remote.MetadataLocation != view.Spec.MetadataLocation {
		drifted = []string{"metadataLocation"}
	}
	return r.viewSuccess(ctx, view, catalog.Spec.CatalogName, remote.MetadataLocation, drifted)
}

func (r *PolarisViewReconciler) reconcileViewDelete(ctx context.Context, apiClient *polarisclient.Client, catalog *polarisv1alpha1.PolarisCatalog, view *polarisv1alpha1.PolarisView, mode polarisv1alpha1.PolarisOwnershipMode, deletionPolicy polarisv1alpha1.PolarisDeletionPolicy) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(view, polarisViewFinalizer) {
		return ctrl.Result{}, nil
	}
	if mode != polarisv1alpha1.PolarisOwnershipEnforce || deletionPolicy != polarisv1alpha1.PolarisDeletionDelete {
		controllerutil.RemoveFinalizer(view, polarisViewFinalizer)
		return ctrl.Result{}, r.Update(ctx, view)
	}
	if err := apiClient.DeleteView(ctx, catalog.Spec.CatalogName, view.Spec.Namespace, view.Spec.ViewName); err != nil && !isHTTPStatus(err, http.StatusNotFound) {
		return r.viewFailure(ctx, view, "ViewDeleteFailed", err)
	}
	controllerutil.RemoveFinalizer(view, polarisViewFinalizer)
	return ctrl.Result{}, r.Update(ctx, view)
}

func (r *PolarisViewReconciler) viewSuccess(ctx context.Context, view *polarisv1alpha1.PolarisView, catalogName, metadataLocation string, drifted []string) (ctrl.Result, error) {
	now := metav1.Now()
	view.Status.ObservedGeneration = view.Generation
	view.Status.RemoteCatalogName = catalogName
	view.Status.RemoteNamespace = slices.Clone(view.Spec.Namespace)
	view.Status.RemoteViewName = view.Spec.ViewName
	view.Status.RemoteMetadataLocation = metadataLocation
	view.Status.DriftedFields = slices.Clone(drifted)
	view.Status.LastReconciledTime = &now
	meta.SetStatusCondition(&view.Status.Conditions, metav1.Condition{Type: readyConditionType, Status: metav1.ConditionTrue, ObservedGeneration: view.Generation, LastTransitionTime: now, Reason: "ViewObserved", Message: "Polaris view registration was observed"})
	driftStatus, driftReason := metav1.ConditionFalse, "NoDrift"
	driftMessage := "Polaris view registration was observed with no metadataLocation constraint"
	if view.Spec.MetadataLocation != "" {
		driftMessage = "Polaris view registration matches the requested metadata location"
	}
	if len(drifted) > 0 {
		driftStatus, driftReason, driftMessage = metav1.ConditionTrue, "DriftDetected", fmt.Sprintf("Polaris view registration differs in %v; view metadata remains owned by the view engine", drifted)
	}
	meta.SetStatusCondition(&view.Status.Conditions, metav1.Condition{Type: "Drifted", Status: driftStatus, ObservedGeneration: view.Generation, LastTransitionTime: now, Reason: driftReason, Message: driftMessage})
	if err := r.Status().Update(ctx, view); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: polarisViewRequeueAfter}, nil
}

func (r *PolarisViewReconciler) viewFailure(ctx context.Context, view *polarisv1alpha1.PolarisView, reason string, cause error) (ctrl.Result, error) {
	meta.SetStatusCondition(&view.Status.Conditions, metav1.Condition{Type: readyConditionType, Status: metav1.ConditionFalse, ObservedGeneration: view.Generation, LastTransitionTime: metav1.Now(), Reason: reason, Message: sanitizeErrorMessage(cause)})
	view.Status.ObservedGeneration = view.Generation
	if err := r.Status().Update(ctx, view); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: polarisViewRequeueAfter}, nil
}

// SetupWithManager watches view specs and their catalog and connection dependencies.
func (r *PolarisViewReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisView{}).
		Watches(&polarisv1alpha1.PolarisCatalog{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			resources := &polarisv1alpha1.PolarisViewList{}
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
			resources := &polarisv1alpha1.PolarisViewList{}
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
		Named("polarisview").
		Complete(r)
}
