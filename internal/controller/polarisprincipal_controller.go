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
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
	polarisclient "github.com/apache/polaris-kubernetes-operator/internal/polaris"
)

const (
	principalFinalizer                = "polaris.apache.org/principal-cleanup"
	principalReadyConditionType       = "Ready"
	principalCredentialsConditionType = "CredentialsReady"
)

// PolarisPrincipalReconciler reconciles a PolarisPrincipal object.
type PolarisPrincipalReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisprincipals,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisprincipals/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisprincipals/finalizers,verbs=update
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisconnections,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
func (r *PolarisPrincipalReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)
	principal := &polarisv1alpha1.PolarisPrincipal{}
	if err := r.Get(ctx, req.NamespacedName, principal); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	connection := &polarisv1alpha1.PolarisConnection{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: principal.Namespace, Name: principal.Spec.ConnectionRef}, connection); err != nil {
		if apierrors.IsNotFound(err) {
			return r.principalFailure(ctx, principal, "ConnectionNotFound", errors.New("referenced PolarisConnection was not found"))
		}
		return ctrl.Result{}, err
	}
	apiClient, err := (&PolarisConnectionReconciler{Client: r.Client}).newClient(ctx, connection)
	if err != nil {
		return r.principalFailure(ctx, principal, "ConnectionUnavailable", err)
	}

	if !principal.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(principal, principalFinalizer) {
			return ctrl.Result{}, nil
		}
		err := apiClient.DeletePrincipal(ctx, principal.Spec.PrincipalName)
		if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil {
			if !ok || httpErr.StatusCode != http.StatusNotFound {
				return r.principalFailure(ctx, principal, "RemoteDeleteFailed", err)
			}
		}
		controllerutil.RemoveFinalizer(principal, principalFinalizer)
		return ctrl.Result{}, r.Update(ctx, principal)
	}

	if !controllerutil.ContainsFinalizer(principal, principalFinalizer) {
		controllerutil.AddFinalizer(principal, principalFinalizer)
		if err := r.Update(ctx, principal); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	secret, err := r.ensurePrincipalSecret(ctx, principal)
	if err != nil {
		return r.principalFailure(ctx, principal, "CredentialsSecretUnavailable", err)
	}

	if err := r.reconcilePrincipalSecretTemplate(ctx, principal, secret); err != nil {
		return r.principalReadyFailure(ctx, principal, "CredentialsSecretTemplateFailed", err)
	}
	remote, err := apiClient.GetPrincipal(ctx, principal.Spec.PrincipalName)
	if httpErr, ok := errors.AsType[*polarisclient.HTTPError](err); err != nil && ok && httpErr.StatusCode == http.StatusNotFound {
		created, createErr := apiClient.CreatePrincipal(ctx, polarisclient.Principal{
			Name: principal.Spec.PrincipalName, Properties: principal.Spec.Properties,
		})
		if createErr != nil {
			return r.principalFailure(ctx, principal, "CreateFailed", createErr)
		}
		remote = created.Principal
		if err := r.writePrincipalCredentials(ctx, secret, created.Credentials); err != nil {
			return r.principalFailure(ctx, principal, "CredentialsPersistenceFailed", err)
		}
	} else if err != nil {
		return r.principalFailure(ctx, principal, "ReadFailed", err)
	} else {
		if !reflect.DeepEqual(remote.Properties, principal.Spec.Properties) {
			remote.Properties = principal.Spec.Properties
			remote, err = apiClient.UpdatePrincipal(ctx, remote)
			if err != nil {
				return r.principalFailure(ctx, principal, "UpdateFailed", err)
			}
		}
	}

	if requested := principal.Spec.CredentialRotationRequestID; requested != nil {
		if *requested < principal.Status.LastCredentialRotationRequestID {
			return r.principalFailure(ctx, principal, "InvalidRotationRequest", errors.New("credentialRotationRequestID cannot decrease"))
		}
		if *requested > principal.Status.LastCredentialRotationRequestID {
			principal.Status.LastCredentialRotationRequestID = *requested
			if err := r.Status().Update(ctx, principal); err != nil {
				return ctrl.Result{}, err
			}
			rotated, rotateErr := apiClient.RotatePrincipal(ctx, principal.Spec.PrincipalName)
			if rotateErr != nil {
				return r.principalFailure(ctx, principal, "CredentialRotationFailed", rotateErr)
			}
			remote = rotated.Principal
			if err := r.writePrincipalCredentials(ctx, secret, rotated.Credentials); err != nil {
				return r.principalFailure(ctx, principal, "CredentialsPersistenceFailed", err)
			}
		}
	}

	if len(secret.Data["client-id"]) == 0 || len(secret.Data["client-secret"]) == 0 {
		return r.principalFailure(ctx, principal, "CredentialsMissing",
			errors.New("credentials Secret has no credentials; set a new credentialRotationRequestID to rotate credentials"))
	}
	now := metav1.Now()
	principal.Status.ObservedGeneration = principal.Generation
	principal.Status.EntityVersion = remote.EntityVersion
	principal.Status.CredentialsSecretResourceVersion = secret.ResourceVersion
	principal.Status.LastReconciledTime = &now
	meta.SetStatusCondition(&principal.Status.Conditions, metav1.Condition{
		Type: principalReadyConditionType, Status: metav1.ConditionTrue, ObservedGeneration: principal.Generation,
		LastTransitionTime: now, Reason: "PrincipalReady", Message: "Polaris principal matches the desired state",
	})
	meta.SetStatusCondition(&principal.Status.Conditions, metav1.Condition{
		Type: principalCredentialsConditionType, Status: metav1.ConditionTrue, ObservedGeneration: principal.Generation,
		LastTransitionTime: now, Reason: "CredentialsAvailable", Message: "Generated credentials are stored in the referenced Secret",
	})
	if err := r.Status().Update(ctx, principal); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("Reconciled Polaris principal", "name", principal.Name)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *PolarisPrincipalReconciler) ensurePrincipalSecret(ctx context.Context, principal *polarisv1alpha1.PolarisPrincipal) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: principal.Namespace, Name: principal.Spec.CredentialsSecretName}
	if err := r.Get(ctx, key, secret); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}, Data: map[string][]byte{}}
		if err := controllerutil.SetControllerReference(principal, secret, r.Scheme); err != nil {
			return nil, err
		}
		if err := r.Create(ctx, secret); err != nil {
			return nil, err
		}
		return secret, nil
	}
	owner := metav1.GetControllerOf(secret)
	if owner == nil || owner.UID != principal.UID {
		return nil, fmt.Errorf("credentials Secret %s is not controlled by this PolarisPrincipal", key.Name)
	}
	return secret, nil
}

func (r *PolarisPrincipalReconciler) reconcilePrincipalSecretTemplate(ctx context.Context, principal *polarisv1alpha1.PolarisPrincipal, secret *corev1.Secret) error {
	var desiredLabels, desiredAnnotations map[string]string
	if template := principal.Spec.CredentialsSecretTemplate; template != nil && template.Metadata != nil {
		desiredLabels = template.Metadata.Labels
		desiredAnnotations = template.Metadata.Annotations
	}

	labels, labelsChanged := reconcileTemplateMetadataMap(
		secret.Labels, principal.Status.CredentialsSecretTemplateManagedLabelKeys, desiredLabels,
	)
	annotations, annotationsChanged := reconcileTemplateMetadataMap(
		secret.Annotations, principal.Status.CredentialsSecretTemplateManagedAnnotationKeys, desiredAnnotations,
	)
	if labelsChanged || annotationsChanged {
		secret.Labels = labels
		secret.Annotations = annotations
		if err := r.Update(ctx, secret); err != nil {
			return fmt.Errorf("update credentials Secret metadata: %w", err)
		}
	}

	labelKeys := sortedMetadataKeys(desiredLabels)
	annotationKeys := sortedMetadataKeys(desiredAnnotations)
	if reflect.DeepEqual(principal.Status.CredentialsSecretTemplateManagedLabelKeys, labelKeys) &&
		reflect.DeepEqual(principal.Status.CredentialsSecretTemplateManagedAnnotationKeys, annotationKeys) {
		return nil
	}

	principal.Status.CredentialsSecretTemplateManagedLabelKeys = labelKeys
	principal.Status.CredentialsSecretTemplateManagedAnnotationKeys = annotationKeys
	if err := r.Status().Update(ctx, principal); err != nil {
		return fmt.Errorf("record credentials Secret template metadata: %w", err)
	}
	return nil
}

func reconcileTemplateMetadataMap(current map[string]string, previouslyManagedKeys []string, desired map[string]string) (map[string]string, bool) {
	changed := false
	for _, key := range previouslyManagedKeys {
		if _, managed := desired[key]; managed {
			continue
		}
		if _, exists := current[key]; exists {
			delete(current, key)
			changed = true
		}
	}
	for key, value := range desired {
		if current[key] == value {
			continue
		}
		if current == nil {
			current = make(map[string]string, len(desired))
		}
		current[key] = value
		changed = true
	}
	if changed && len(current) == 0 {
		current = nil
	}
	return current, changed
}

func sortedMetadataKeys(metadata map[string]string) []string {
	if len(metadata) == 0 {
		return nil
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (r *PolarisPrincipalReconciler) principalReadyFailure(ctx context.Context, principal *polarisv1alpha1.PolarisPrincipal, reason string, cause error) (ctrl.Result, error) {
	meta.SetStatusCondition(&principal.Status.Conditions, metav1.Condition{
		Type: principalReadyConditionType, Status: metav1.ConditionFalse, ObservedGeneration: principal.Generation,
		LastTransitionTime: metav1.Now(), Reason: reason, Message: cause.Error(),
	})
	if err := r.Status().Update(ctx, principal); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *PolarisPrincipalReconciler) writePrincipalCredentials(ctx context.Context, secret *corev1.Secret, credentials polarisclient.PrincipalCredentials) error {
	if credentials.ClientID == "" || credentials.ClientSecret == "" {
		return errors.New("Polaris returned empty principal credentials")
	}
	secret.Data = map[string][]byte{
		"client-id":     []byte(credentials.ClientID),
		"client-secret": []byte(credentials.ClientSecret),
	}
	return r.Update(ctx, secret)
}

func (r *PolarisPrincipalReconciler) principalFailure(ctx context.Context, principal *polarisv1alpha1.PolarisPrincipal, reason string, cause error) (ctrl.Result, error) {
	now := metav1.Now()
	message := cause.Error()
	meta.SetStatusCondition(&principal.Status.Conditions, metav1.Condition{
		Type: principalReadyConditionType, Status: metav1.ConditionFalse, ObservedGeneration: principal.Generation,
		LastTransitionTime: now, Reason: reason, Message: message,
	})
	meta.SetStatusCondition(&principal.Status.Conditions, metav1.Condition{
		Type: principalCredentialsConditionType, Status: metav1.ConditionFalse, ObservedGeneration: principal.Generation,
		LastTransitionTime: now, Reason: reason, Message: message,
	})
	if err := r.Status().Update(ctx, principal); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PolarisPrincipalReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisPrincipal{}).
		Owns(&corev1.Secret{}).
		Named("polarisprincipal").
		Complete(r)
}
