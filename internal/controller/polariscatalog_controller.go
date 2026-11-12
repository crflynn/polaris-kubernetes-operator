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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"time"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
	polarisclient "github.com/apache/polaris-kubernetes-operator/internal/polaris"
	corev1 "k8s.io/api/core/v1"
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
	catalogFinalizer    = "polaris.apache.org/catalog-cleanup"
	catalogRequeueAfter = 30 * time.Second
)

const (
	readyConditionType        = "Ready"
	catalogReadyConditionType = readyConditionType
	driftedConditionType      = "Drifted"
	noDriftReason             = "NoDrift"
	driftDetectedReason       = "DriftDetected"
	clientIDSecretKey         = "client-id"
	clientSecretSecretKey     = "client-secret"
)

// PolarisCatalogReconciler reconciles a PolarisCatalog object.
type PolarisCatalogReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polariscatalogs/finalizers,verbs=update
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisconnections,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
func (r *PolarisCatalogReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	catalog := &polarisv1alpha1.PolarisCatalog{}
	if err := r.Get(ctx, req.NamespacedName, catalog); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	connection := &polarisv1alpha1.PolarisConnection{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: catalog.Namespace, Name: catalog.Spec.ConnectionRef}, connection); err != nil {
		if apierrors.IsNotFound(err) {
			return r.catalogFailure(ctx, catalog, "ConnectionNotFound", errors.New("referenced PolarisConnection was not found"))
		}
		return ctrl.Result{}, err
	}
	connectionReconciler := &PolarisConnectionReconciler{Client: r.Client}
	apiClient, err := connectionReconciler.newClient(ctx, connection)
	if err != nil {
		return r.catalogFailure(ctx, catalog, "ConnectionUnavailable", err)
	}

	if !catalog.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(catalog, catalogFinalizer) {
			return ctrl.Result{}, nil
		}
		err := apiClient.DeleteCatalog(ctx, catalog.Spec.CatalogName)
		if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil {
			if !ok {
				return r.catalogFailure(ctx, catalog, "RemoteDeleteFailed", err)
			}
			if httpErr.StatusCode != http.StatusNotFound {
				return r.catalogFailure(ctx, catalog, "RemoteDeleteFailed", err)
			}
		}
		controllerutil.RemoveFinalizer(catalog, catalogFinalizer)
		return ctrl.Result{}, r.Update(ctx, catalog)
	}

	if !controllerutil.ContainsFinalizer(catalog, catalogFinalizer) {
		controllerutil.AddFinalizer(catalog, catalogFinalizer)
		if err := r.Update(ctx, catalog); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	var external *polarisclient.ExternalConnectionConfig
	var authSecretName, authSecretVersion, authConfigHash string
	if catalog.Spec.Type == "EXTERNAL" {
		external, authSecretName, authSecretVersion, err = r.externalConnectionConfig(ctx, catalog)
		if err != nil {
			return r.catalogFailure(ctx, catalog, "ExternalAuthenticationUnavailable", err)
		}
		authConfigHash, err = externalAuthenticationHash(catalog.Spec.ExternalCatalog.Authentication)
		if err != nil {
			return r.catalogFailure(ctx, catalog, "ExternalAuthenticationUnavailable", err)
		}
	}

	remote, err := apiClient.GetCatalog(ctx, catalog.Spec.CatalogName)
	if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil && ok && httpErr.StatusCode == http.StatusNotFound {
		if external != nil {
			catalog.Status.AuthenticationSecretName = authSecretName
			catalog.Status.AuthenticationSecretResourceVersion = authSecretVersion
			catalog.Status.AuthenticationConfigurationHash = authConfigHash
			if err := r.Status().Update(ctx, catalog); err != nil {
				return ctrl.Result{}, err
			}
		}
		remote, err = apiClient.CreateCatalog(ctx, desiredCatalog(catalog, external))
	} else if err == nil {
		remote, err = r.updateExistingCatalog(ctx, apiClient, catalog, external, authSecretName, authSecretVersion, authConfigHash, remote)
	}
	if err != nil {
		return r.catalogFailure(ctx, catalog, "ReconcileFailed", err)
	}

	now := metav1.Now()
	catalog.Status.ObservedGeneration = catalog.Generation
	catalog.Status.EntityVersion = remote.EntityVersion
	catalog.Status.LastReconciledTime = &now
	if external == nil {
		catalog.Status.AuthenticationSecretName = ""
		catalog.Status.AuthenticationSecretResourceVersion = ""
		catalog.Status.AuthenticationConfigurationHash = ""
	} else {
		catalog.Status.AuthenticationSecretName = authSecretName
		catalog.Status.AuthenticationSecretResourceVersion = authSecretVersion
		catalog.Status.AuthenticationConfigurationHash = authConfigHash
	}
	metaSetCatalogReady(catalog, metav1.ConditionTrue, "CatalogReady", "Polaris catalog matches the desired state")
	if err := r.Status().Update(ctx, catalog); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: catalogRequeueAfter}, nil
}

func (r *PolarisCatalogReconciler) updateExistingCatalog(
	ctx context.Context,
	apiClient *polarisclient.Client,
	catalog *polarisv1alpha1.PolarisCatalog,
	external *polarisclient.ExternalConnectionConfig,
	authSecretName, authSecretVersion, authConfigHash string,
	remote polarisclient.Catalog,
) (polarisclient.Catalog, error) {
	desired := desiredCatalog(catalog, external)
	if remote.Type != desired.Type {
		return remote, errors.New("existing Polaris catalog type cannot be changed")
	}
	if external != nil && (catalog.Status.AuthenticationSecretName == "" ||
		catalog.Status.AuthenticationConfigurationHash == "") {
		return remote, errors.New("external catalog already exists without recorded authentication Secret provenance; delete and recreate it")
	}
	if external != nil && (catalog.Status.AuthenticationSecretName != authSecretName ||
		catalog.Status.AuthenticationSecretResourceVersion != authSecretVersion ||
		catalog.Status.AuthenticationConfigurationHash != authConfigHash) {
		return remote, errors.New("external catalog authentication Secret or reference changed; delete and recreate the catalog because Polaris does not support updating connection authentication")
	}
	if external != nil && !externalConnectionMatches(remote.ConnectionConfigInfo, external) {
		return remote, errors.New("external catalog connection configuration cannot be updated; delete and recreate the catalog")
	}
	desired.EntityVersion = remote.EntityVersion
	if !reflect.DeepEqual(remote.Properties, desired.Properties) ||
		!reflect.DeepEqual(remote.StorageConfigInfo, desired.StorageConfigInfo) {
		return apiClient.UpdateCatalog(ctx, desired)
	}
	return remote, nil
}

func desiredCatalog(catalog *polarisv1alpha1.PolarisCatalog, external *polarisclient.ExternalConnectionConfig) polarisclient.Catalog {
	catalogType := catalog.Spec.Type
	if catalogType == "" {
		catalogType = "INTERNAL"
	}
	storage := polarisclient.StorageConfig{
		StorageType:      catalog.Spec.StorageConfigInfo.StorageType,
		AllowedLocations: catalog.Spec.StorageConfigInfo.AllowedLocations,
		StorageName:      catalog.Spec.StorageConfigInfo.StorageName,
	}
	return polarisclient.Catalog{
		Name: catalog.Spec.CatalogName, Type: catalogType,
		Properties: catalog.Spec.Properties, StorageConfigInfo: storage,
		ConnectionConfigInfo: external,
	}
}

func (r *PolarisCatalogReconciler) externalConnectionConfig(ctx context.Context, catalog *polarisv1alpha1.PolarisCatalog) (*polarisclient.ExternalConnectionConfig, string, string, error) {
	config := catalog.Spec.ExternalCatalog
	if config == nil {
		return nil, "", "", errors.New("externalCatalog configuration is required for EXTERNAL catalogs")
	}
	connectionReconciler := &PolarisConnectionReconciler{Client: r.Client}
	result := &polarisclient.ExternalConnectionConfig{
		ConnectionType:    "ICEBERG_REST",
		URI:               config.URI,
		RemoteCatalogName: config.RemoteCatalogName,
		Properties:        config.Properties,
	}
	auth := polarisclient.ExternalAuthentication{AuthenticationType: config.Authentication.Type}
	var secretName, secretVersion string
	switch config.Authentication.Type {
	case "OAUTH":
		credentials := config.Authentication.ClientCredentials
		if credentials == nil {
			return nil, "", "", errors.New("OAuth client credentials are required")
		}
		secret, err := connectionReconciler.getSecret(ctx, catalog.Namespace, credentials.SecretRef.Name)
		if err != nil {
			return nil, "", "", fmt.Errorf("read external catalog OAuth Secret: %w", err)
		}
		clientIDKey := credentials.ClientIDKey
		if clientIDKey == "" {
			clientIDKey = clientIDSecretKey
		}
		clientSecretKey := credentials.ClientSecretKey
		if clientSecretKey == "" {
			clientSecretKey = clientSecretSecretKey
		}
		auth.TokenURI = credentials.TokenURL
		auth.ClientID, err = secretValue(secret, clientIDKey)
		if err != nil {
			return nil, "", "", fmt.Errorf("read external catalog client ID: %w", err)
		}
		auth.ClientSecret, err = secretValue(secret, clientSecretKey)
		if err != nil {
			return nil, "", "", fmt.Errorf("read external catalog client secret: %w", err)
		}
		auth.Scopes = credentials.Scopes
		secretName, secretVersion = secret.Name, secret.ResourceVersion
	case "BEARER":
		ref := config.Authentication.BearerTokenSecretRef
		if ref == nil {
			return nil, "", "", errors.New("bearer token Secret reference is required")
		}
		secret, err := connectionReconciler.getSecret(ctx, catalog.Namespace, ref.Name)
		if err != nil {
			return nil, "", "", fmt.Errorf("read external catalog bearer token Secret: %w", err)
		}
		auth.BearerToken, err = secretValue(secret, ref.Key)
		if err != nil {
			return nil, "", "", fmt.Errorf("read external catalog bearer token: %w", err)
		}
		secretName, secretVersion = secret.Name, secret.ResourceVersion
	default:
		return nil, "", "", fmt.Errorf("unsupported external catalog authentication type %q", config.Authentication.Type)
	}
	result.AuthenticationParameters = auth
	return result, secretName, secretVersion, nil
}

func externalAuthenticationHash(authentication polarisv1alpha1.PolarisExternalCatalogAuthentication) (string, error) {
	encoded, err := json.Marshal(authentication)
	if err != nil {
		return "", fmt.Errorf("encode external authentication reference: %w", err)
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func externalConnectionMatches(remote, desired *polarisclient.ExternalConnectionConfig) bool {
	if remote == nil || desired == nil {
		return remote == desired
	}
	if remote.ConnectionType != desired.ConnectionType ||
		remote.URI != desired.URI ||
		remote.RemoteCatalogName != desired.RemoteCatalogName ||
		!maps.Equal(remote.Properties, desired.Properties) ||
		remote.AuthenticationParameters.AuthenticationType != desired.AuthenticationParameters.AuthenticationType {
		return false
	}
	if desired.AuthenticationParameters.AuthenticationType == "OAUTH" {
		return remote.AuthenticationParameters.TokenURI == desired.AuthenticationParameters.TokenURI &&
			remote.AuthenticationParameters.ClientID == desired.AuthenticationParameters.ClientID &&
			slices.Equal(remote.AuthenticationParameters.Scopes, desired.AuthenticationParameters.Scopes)
	}
	return true
}

func catalogReferencesSecret(catalog *polarisv1alpha1.PolarisCatalog, secret client.Object) bool {
	if catalog.Namespace != secret.GetNamespace() || catalog.Spec.ExternalCatalog == nil {
		return false
	}
	auth := catalog.Spec.ExternalCatalog.Authentication
	if auth.ClientCredentials != nil && auth.ClientCredentials.SecretRef.Name == secret.GetName() {
		return true
	}
	return auth.BearerTokenSecretRef != nil && auth.BearerTokenSecretRef.Name == secret.GetName()
}

func (r *PolarisCatalogReconciler) catalogFailure(ctx context.Context, catalog *polarisv1alpha1.PolarisCatalog, reason string, reconcileErr error) (ctrl.Result, error) {
	metaSetCatalogReady(catalog, metav1.ConditionFalse, reason, sanitizeErrorMessage(reconcileErr))
	catalog.Status.ObservedGeneration = catalog.Generation
	if err := r.Status().Update(ctx, catalog); err != nil {
		return ctrl.Result{}, err
	}
	logf.FromContext(ctx).Error(reconcileErr, "Polaris catalog reconciliation failed", "name", catalog.Name)
	return ctrl.Result{RequeueAfter: catalogRequeueAfter}, nil
}

func metaSetCatalogReady(catalog *polarisv1alpha1.PolarisCatalog, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&catalog.Status.Conditions, metav1.Condition{
		Type: catalogReadyConditionType, Status: status, ObservedGeneration: catalog.Generation,
		LastTransitionTime: metav1.Now(), Reason: reason, Message: message,
	})
}

func (r *PolarisCatalogReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisCatalog{}).
		Watches(&polarisv1alpha1.PolarisConnection{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			catalogs := &polarisv1alpha1.PolarisCatalogList{}
			if err := r.List(ctx, catalogs, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0, len(catalogs.Items))
			for i := range catalogs.Items {
				if catalogs.Items[i].Spec.ConnectionRef == object.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
						Namespace: catalogs.Items[i].Namespace, Name: catalogs.Items[i].Name,
					}})
				}
			}
			return requests
		})).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
			catalogs := &polarisv1alpha1.PolarisCatalogList{}
			if err := r.List(ctx, catalogs, client.InNamespace(object.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0, len(catalogs.Items))
			for i := range catalogs.Items {
				if catalogReferencesSecret(&catalogs.Items[i], object) {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
						Namespace: catalogs.Items[i].Namespace, Name: catalogs.Items[i].Name,
					}})
				}
			}
			return requests
		})).
		Named("polariscatalog").
		Complete(r)
}
