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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
	polarisclient "github.com/apache/polaris-kubernetes-operator/internal/polaris"
)

const (
	connectionRequeueAfter = 30 * time.Second
)

// PolarisConnectionReconciler reconciles a PolarisConnection object.
type PolarisConnectionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisconnections,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisconnections/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile validates the connection credentials and probes the configured Polaris APIs.
func (r *PolarisConnectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)
	connection := &polarisv1alpha1.PolarisConnection{}
	if err := r.Get(ctx, req.NamespacedName, connection); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	result, err := r.checkConnection(ctx, connection)
	if err != nil {
		if statusErr := r.setConnectionFailure(ctx, connection, err); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		logger.Error(err, "Polaris connection check failed", "name", connection.Name)
		return ctrl.Result{RequeueAfter: connectionRequeueAfter}, nil
	}

	now := metav1.Now()
	connection.Status.ObservedGeneration = connection.Generation
	connection.Status.ManagementURL = connection.Spec.ManagementURL
	connection.Status.CatalogURL = connection.Spec.CatalogURL
	connection.Status.Capabilities = result.Capabilities
	connection.Status.NamespaceSeparator = result.NamespaceSeparator
	connection.Status.LastCheckedTime = &now
	meta.SetStatusCondition(&connection.Status.Conditions, metav1.Condition{
		Type:               polarisv1alpha1.PolarisConnectionReadyConditionType,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: connection.Generation,
		LastTransitionTime: now,
		Reason:             "ConnectionReady",
		Message:            "Polaris management API authentication and endpoint checks succeeded",
	})
	if err := r.Status().Update(ctx, connection); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: connectionRequeueAfter}, nil
}
func (r *PolarisConnectionReconciler) checkConnection(ctx context.Context, connection *polarisv1alpha1.PolarisConnection) (polarisclient.CheckResult, error) {

	apiClient, err := r.newClient(ctx, connection)
	if err != nil {
		return polarisclient.CheckResult{}, err
	}
	return apiClient.Check(ctx)
}

func (r *PolarisConnectionReconciler) newClient(ctx context.Context, connection *polarisv1alpha1.PolarisConnection) (*polarisclient.Client, error) {
	timeout := polarisclient.DefaultRequestTimeout
	if connection.Spec.TimeoutSeconds != nil {
		timeout = time.Duration(*connection.Spec.TimeoutSeconds) * time.Second
	}

	caPEM, err := r.readCABundle(ctx, connection)
	if err != nil {
		return nil, err
	}
	httpClient, err := polarisclient.NewHTTPClient(timeout, caPEM)
	if err != nil {
		return nil, err
	}
	auth, err := r.readAuth(ctx, connection)
	if err != nil {
		return nil, err
	}
	return polarisclient.NewClient(polarisclient.Config{
		ManagementURL:      connection.Spec.ManagementURL,
		CatalogURL:         connection.Spec.CatalogURL,
		Realm:              connection.Spec.Realm,
		RealmHeader:        connection.Spec.RealmHeader,
		NamespaceSeparator: connection.Status.NamespaceSeparator,
		Auth:               auth,
		HTTPClient:         httpClient,
	})
}

func (r *PolarisConnectionReconciler) readAuth(ctx context.Context, connection *polarisv1alpha1.PolarisConnection) (polarisclient.AuthConfig, error) {
	auth := connection.Spec.Auth
	if auth.ClientCredentials != nil {
		credentials := auth.ClientCredentials
		secret, err := r.getSecret(ctx, connection.Namespace, credentials.SecretRef.Name)
		if err != nil {
			return polarisclient.AuthConfig{}, fmt.Errorf("read client credentials Secret: %w", err)
		}
		clientIDKey := credentials.ClientIDKey
		if clientIDKey == "" {
			clientIDKey = "client-id"
		}
		clientSecretKey := credentials.ClientSecretKey
		if clientSecretKey == "" {
			clientSecretKey = "client-secret"
		}
		clientID, err := secretValue(secret, clientIDKey)
		if err != nil {
			return polarisclient.AuthConfig{}, fmt.Errorf("read client ID: %w", err)
		}
		clientSecret, err := secretValue(secret, clientSecretKey)
		if err != nil {
			return polarisclient.AuthConfig{}, fmt.Errorf("read client secret: %w", err)
		}
		return polarisclient.AuthConfig{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			TokenURL:     connection.Spec.TokenURL,
			Scopes:       credentials.Scopes,
		}, nil
	}

	if auth.BearerTokenSecretRef != nil {
		ref := auth.BearerTokenSecretRef
		secret, err := r.getSecret(ctx, connection.Namespace, ref.Name)
		if err != nil {
			return polarisclient.AuthConfig{}, fmt.Errorf("read bearer token Secret: %w", err)
		}
		token, err := secretValue(secret, ref.Key)
		if err != nil {
			return polarisclient.AuthConfig{}, fmt.Errorf("read bearer token: %w", err)
		}
		return polarisclient.AuthConfig{BearerToken: token}, nil
	}
	return polarisclient.AuthConfig{}, errors.New("exactly one authentication method must be configured")
}

func (r *PolarisConnectionReconciler) readCABundle(ctx context.Context, connection *polarisv1alpha1.PolarisConnection) ([]byte, error) {
	if connection.Spec.TLS == nil {
		return nil, nil
	}
	ref := connection.Spec.TLS.CASecretRef
	secret, err := r.getSecret(ctx, connection.Namespace, ref.Name)
	if err != nil {
		return nil, fmt.Errorf("read CA Secret: %w", err)
	}
	value, err := secretValue(secret, ref.Key)
	if err != nil {
		return nil, fmt.Errorf("read CA bundle: %w", err)
	}
	return []byte(value), nil
}

func (r *PolarisConnectionReconciler) getSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		return nil, err
	}
	return secret, nil
}

func secretValue(secret *corev1.Secret, key string) (string, error) {
	value, ok := secret.Data[key]
	if !ok || len(value) == 0 {
		return "", fmt.Errorf("secret %q does not contain a non-empty %q key", secret.Name, key)
	}
	return string(value), nil
}

func (r *PolarisConnectionReconciler) setConnectionFailure(ctx context.Context, connection *polarisv1alpha1.PolarisConnection, reconcileErr error) error {
	now := metav1.Now()
	connection.Status.ObservedGeneration = connection.Generation
	meta.SetStatusCondition(&connection.Status.Conditions, metav1.Condition{
		Type:               polarisv1alpha1.PolarisConnectionReadyConditionType,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: connection.Generation,
		LastTransitionTime: now,
		Reason:             connectionFailureReason(reconcileErr),
		Message:            sanitizeErrorMessage(reconcileErr),
	})
	connection.Status.LastCheckedTime = &now
	return r.Status().Update(ctx, connection)
}

func connectionFailureReason(err error) string {
	if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); ok {
		switch httpErr.StatusCode {
		case http.StatusUnauthorized:
			return "AuthenticationFailed"
		case http.StatusForbidden:
			return "AuthorizationFailed"
		case http.StatusBadRequest:
			return "InvalidRemoteRequest"
		}
		if httpErr.StatusCode >= http.StatusInternalServerError {
			return "RemoteUnavailable"
		}
	}
	return "InvalidConfigurationOrUnavailable"
}

func sanitizeErrorMessage(err error) string {
	message := strings.TrimSpace(err.Error())
	if len(message) > 512 {
		return message[:512]
	}
	return message
}

func (r *PolarisConnectionReconciler) referencesSecret(connection *polarisv1alpha1.PolarisConnection, secret client.Object) bool {
	if connection.Namespace != secret.GetNamespace() {
		return false
	}
	if connection.Spec.Auth.ClientCredentials != nil && connection.Spec.Auth.ClientCredentials.SecretRef.Name == secret.GetName() {
		return true
	}
	if connection.Spec.Auth.BearerTokenSecretRef != nil && connection.Spec.Auth.BearerTokenSecretRef.Name == secret.GetName() {
		return true
	}
	return connection.Spec.TLS != nil && connection.Spec.TLS.CASecretRef.Name == secret.GetName()
}

// SetupWithManager sets up the controller with the Manager.
func (r *PolarisConnectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisConnection{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, secret client.Object) []reconcile.Request {
			connections := &polarisv1alpha1.PolarisConnectionList{}
			if err := r.List(ctx, connections, client.InNamespace(secret.GetNamespace())); err != nil {
				return nil
			}
			requests := make([]reconcile.Request, 0, len(connections.Items))
			for i := range connections.Items {
				connection := &connections.Items[i]
				if r.referencesSecret(connection, secret) {
					requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
						Namespace: connection.Namespace,
						Name:      connection.Name,
					}})
				}
			}
			return requests
		})).
		Named("polarisconnection").
		Complete(r)
}
