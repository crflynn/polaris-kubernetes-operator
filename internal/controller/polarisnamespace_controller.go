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
	namespaceFinalizer    = "polaris.apache.org/namespace-cleanup"
	namespaceRequeueAfter = 30 * time.Second
)

// PolarisNamespaceReconciler reconciles one nested Polaris namespace.
type PolarisNamespaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisnamespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisnamespaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisnamespaces/finalizers,verbs=update
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogs,verbs=get;list;watch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisconnections,verbs=get;list;watch
func (r *PolarisNamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	namespace := &polarisv1alpha1.PolarisNamespace{}
	if err := r.Get(ctx, req.NamespacedName, namespace); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	catalog := &polarisv1alpha1.PolarisCatalog{}
	catalogKey := types.NamespacedName{Namespace: namespace.Namespace, Name: namespace.Spec.CatalogRef}
	if err := r.Get(ctx, catalogKey, catalog); err != nil {
		if apierrors.IsNotFound(err) {
			return r.namespaceFailure(ctx, namespace, "CatalogNotFound", errors.New("referenced PolarisCatalog was not found"))
		}
		return ctrl.Result{}, err
	}
	if !meta.IsStatusConditionTrue(catalog.Status.Conditions, "Ready") {
		return r.namespaceFailure(ctx, namespace, "CatalogNotReady", errors.New("referenced PolarisCatalog is not Ready"))
	}

	connection := &polarisv1alpha1.PolarisConnection{}
	connectionKey := types.NamespacedName{Namespace: namespace.Namespace, Name: catalog.Spec.ConnectionRef}
	if err := r.Get(ctx, connectionKey, connection); err != nil {
		if apierrors.IsNotFound(err) {
			return r.namespaceFailure(ctx, namespace, "ConnectionNotFound", errors.New("referenced PolarisConnection was not found"))
		}
		return ctrl.Result{}, err
	}
	if !meta.IsStatusConditionTrue(connection.Status.Conditions, polarisv1alpha1.PolarisConnectionReadyConditionType) {
		return r.namespaceFailure(ctx, namespace, "ConnectionNotReady", errors.New("referenced PolarisConnection is not Ready"))
	}

	connectionReconciler := &PolarisConnectionReconciler{Client: r.Client}
	apiClient, err := connectionReconciler.newClient(ctx, connection)
	if err != nil {
		return r.namespaceFailure(ctx, namespace, "ConnectionUnavailable", err)
	}

	mode := namespaceOwnershipMode(namespace)
	deletionPolicy := namespaceDeletionPolicy(namespace)
	if !namespace.DeletionTimestamp.IsZero() {
		return r.reconcileNamespaceDelete(ctx, apiClient, catalog, namespace, mode, deletionPolicy)
	}
	if mode == polarisv1alpha1.PolarisOwnershipEnforce && deletionPolicy == polarisv1alpha1.PolarisDeletionDelete {
		if !controllerutil.ContainsFinalizer(namespace, namespaceFinalizer) {
			controllerutil.AddFinalizer(namespace, namespaceFinalizer)
			if err := r.Update(ctx, namespace); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
	} else if controllerutil.ContainsFinalizer(namespace, namespaceFinalizer) {
		controllerutil.RemoveFinalizer(namespace, namespaceFinalizer)
		if err := r.Update(ctx, namespace); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if namespace.Status.RemoteCatalogName != "" &&
		(namespace.Status.RemoteCatalogName != catalog.Spec.CatalogName || !slices.Equal(namespace.Status.RemoteNamespace, namespace.Spec.Namespace)) {
		return r.namespaceFailure(ctx, namespace, "IdentityChanged", errors.New("catalogRef and namespace identity cannot change after the remote identity is recorded"))
	}

	remote, err := apiClient.GetNamespace(ctx, catalog.Spec.CatalogName, namespace.Spec.Namespace)
	if isHTTPStatus(err, http.StatusNotFound) {
		if mode != polarisv1alpha1.PolarisOwnershipEnforce {
			return r.namespaceFailure(ctx, namespace, "RemoteNamespaceNotFound", errors.New("namespace is absent in Polaris and ownershipMode does not permit creation"))
		}
		remote, err = apiClient.CreateNamespace(ctx, catalog.Spec.CatalogName, polarisclient.Namespace{
			Namespace:  namespace.Spec.Namespace,
			Properties: namespace.Spec.Properties,
		})
		if err != nil {
			return r.namespaceFailure(ctx, namespace, "NamespaceCreateFailed", err)
		}
	}
	if err != nil {
		return r.namespaceFailure(ctx, namespace, "NamespaceReadFailed", err)
	}
	if !slices.Equal(remote.Namespace, namespace.Spec.Namespace) {
		return r.namespaceFailure(ctx, namespace, "RemoteIdentityMismatch", errors.New("Polaris returned a different namespace identity"))
	}

	adopting := mode == polarisv1alpha1.PolarisOwnershipAdopt && namespace.Status.RemoteCatalogName == ""
	managedKeys := namespace.Status.ManagedPropertyKeys
	var drifted []string
	if adopting {
		drifted = namespacePropertyDrift(namespace.Spec.Properties, remote.Properties, managedKeys)
	} else {
		managedKeys, drifted, err = r.reconcileNamespaceProperties(ctx, apiClient, catalog.Spec.CatalogName, namespace, remote, mode)
		if err != nil {
			return r.namespaceFailure(ctx, namespace, "NamespacePropertiesFailed", err)
		}
	}
	return r.namespaceSuccess(ctx, namespace, catalog.Spec.CatalogName, remote.Namespace, managedKeys, drifted)
}

func (r *PolarisNamespaceReconciler) reconcileNamespaceDelete(
	ctx context.Context,
	apiClient *polarisclient.Client,
	catalog *polarisv1alpha1.PolarisCatalog,
	namespace *polarisv1alpha1.PolarisNamespace,
	mode polarisv1alpha1.PolarisOwnershipMode,
	deletionPolicy polarisv1alpha1.PolarisDeletionPolicy,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(namespace, namespaceFinalizer) {
		return ctrl.Result{}, nil
	}
	if mode == polarisv1alpha1.PolarisOwnershipEnforce && deletionPolicy == polarisv1alpha1.PolarisDeletionDelete {
		err := apiClient.DeleteNamespace(ctx, catalog.Spec.CatalogName, namespace.Spec.Namespace)
		if err != nil && !isHTTPStatus(err, http.StatusNotFound) {
			reason := "NamespaceDeleteFailed"
			if isHTTPStatus(err, http.StatusConflict) {
				reason = "NamespaceNotEmpty"
			}
			return r.namespaceFailure(ctx, namespace, reason, err)
		}
	}
	controllerutil.RemoveFinalizer(namespace, namespaceFinalizer)
	return ctrl.Result{}, r.Update(ctx, namespace)
}

func (r *PolarisNamespaceReconciler) reconcileNamespaceProperties(
	ctx context.Context,
	apiClient *polarisclient.Client,
	catalogName string,
	namespace *polarisv1alpha1.PolarisNamespace,
	remote polarisclient.Namespace,
	mode polarisv1alpha1.PolarisOwnershipMode,
) ([]string, []string, error) {
	desired := namespace.Spec.Properties
	actual := remote.Properties
	managed := namespace.Status.ManagedPropertyKeys
	drifted := namespacePropertyDrift(desired, actual, managed)
	if mode == polarisv1alpha1.PolarisOwnershipObserve {
		return managed, drifted, nil
	}
	if actual == nil && (len(desired) > 0 || len(managed) > 0) {
		return nil, nil, errors.New("Polaris does not report namespace properties, so managed property changes cannot be reconciled safely")
	}

	updates := make(map[string]string, len(desired))
	for key, value := range desired {
		if actual[key] != value {
			updates[key] = value
		}
	}
	removals := make([]string, 0)
	for _, key := range managed {
		if _, stillDesired := desired[key]; stillDesired {
			continue
		}
		if _, exists := actual[key]; exists {
			removals = append(removals, key)
		}
	}
	slices.Sort(removals)
	if len(updates) > 0 || len(removals) > 0 {
		if _, err := apiClient.UpdateNamespaceProperties(ctx, catalogName, namespace.Spec.Namespace, updates, removals); err != nil {
			return nil, nil, err
		}
	}
	managed = make([]string, 0, len(desired))
	for key := range desired {
		managed = append(managed, key)
	}
	slices.Sort(managed)
	return managed, nil, nil
}

func namespacePropertyDrift(desired, actual map[string]string, managed []string) []string {
	drifted := make([]string, 0)
	for key, value := range desired {
		if actual == nil || actual[key] != value {
			drifted = append(drifted, "properties."+key)
		}
	}
	for _, key := range managed {
		if _, stillDesired := desired[key]; stillDesired {
			continue
		}
		if actual != nil {
			if _, exists := actual[key]; exists {
				drifted = append(drifted, "properties."+key)
			}
		}
	}
	slices.Sort(drifted)
	return drifted
}

func namespaceOwnershipMode(namespace *polarisv1alpha1.PolarisNamespace) polarisv1alpha1.PolarisOwnershipMode {
	if namespace.Spec.OwnershipMode == "" {
		return polarisv1alpha1.PolarisOwnershipObserve
	}
	return namespace.Spec.OwnershipMode
}

func namespaceDeletionPolicy(namespace *polarisv1alpha1.PolarisNamespace) polarisv1alpha1.PolarisDeletionPolicy {
	if namespace.Spec.DeletionPolicy == "" {
		return polarisv1alpha1.PolarisDeletionOrphan
	}
	return namespace.Spec.DeletionPolicy
}

func (r *PolarisNamespaceReconciler) namespaceSuccess(
	ctx context.Context,
	namespace *polarisv1alpha1.PolarisNamespace,
	catalogName string,
	remoteNamespace, managedKeys, drifted []string,
) (ctrl.Result, error) {
	now := metav1.Now()
	namespace.Status.ObservedGeneration = namespace.Generation
	namespace.Status.RemoteCatalogName = catalogName
	namespace.Status.RemoteNamespace = slices.Clone(remoteNamespace)
	namespace.Status.ManagedPropertyKeys = slices.Clone(managedKeys)
	namespace.Status.DriftedFields = slices.Clone(drifted)
	namespace.Status.LastReconciledTime = &now
	meta.SetStatusCondition(&namespace.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, ObservedGeneration: namespace.Generation,
		LastTransitionTime: now, Reason: "NamespaceObserved", Message: "Polaris namespace was reconciled",
	})
	driftStatus, driftReason, driftMessage := metav1.ConditionFalse, "NoDrift", "Polaris namespace matches managed properties"
	if len(drifted) > 0 {
		driftStatus, driftReason, driftMessage = metav1.ConditionTrue, "DriftDetected", fmt.Sprintf("Polaris namespace differs in %v", drifted)
	}
	meta.SetStatusCondition(&namespace.Status.Conditions, metav1.Condition{
		Type: "Drifted", Status: driftStatus, ObservedGeneration: namespace.Generation,
		LastTransitionTime: now, Reason: driftReason, Message: driftMessage,
	})
	if err := r.Status().Update(ctx, namespace); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: namespaceRequeueAfter}, nil
}

func (r *PolarisNamespaceReconciler) namespaceFailure(
	ctx context.Context,
	namespace *polarisv1alpha1.PolarisNamespace,
	reason string,
	cause error,
) (ctrl.Result, error) {
	meta.SetStatusCondition(&namespace.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, ObservedGeneration: namespace.Generation,
		LastTransitionTime: metav1.Now(), Reason: reason, Message: sanitizeErrorMessage(cause),
	})
	namespace.Status.ObservedGeneration = namespace.Generation
	if err := r.Status().Update(ctx, namespace); err != nil {
		return ctrl.Result{}, err
	}
	logf.FromContext(ctx).Error(cause, "Polaris namespace reconciliation failed", "name", namespace.Name)
	return ctrl.Result{RequeueAfter: namespaceRequeueAfter}, nil
}

// SetupWithManager sets up the namespace controller and its dependencies.
func (r *PolarisNamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisNamespace{}).
		Watches(&polarisv1alpha1.PolarisCatalog{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			resources := &polarisv1alpha1.PolarisNamespaceList{}
			if err := r.List(ctx, resources, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0)
			for i := range resources.Items {
				if resources.Items[i].Spec.CatalogRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
						Namespace: resources.Items[i].Namespace, Name: resources.Items[i].Name,
					}})
				}
			}
			return requests
		})).
		Watches(&polarisv1alpha1.PolarisConnection{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			catalogs := &polarisv1alpha1.PolarisCatalogList{}
			if err := r.List(ctx, catalogs, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			names := make(map[string]struct{})
			for i := range catalogs.Items {
				if catalogs.Items[i].Spec.ConnectionRef == object.GetName() {
					names[catalogs.Items[i].Name] = struct{}{}
				}
			}
			resources := &polarisv1alpha1.PolarisNamespaceList{}
			if err := r.List(ctx, resources, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0)
			for i := range resources.Items {
				if _, ok := names[resources.Items[i].Spec.CatalogRef]; ok {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
						Namespace: resources.Items[i].Namespace, Name: resources.Items[i].Name,
					}})
				}
			}
			return requests
		})).
		Named("polarisnamespace").
		Complete(r)
}
