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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// PolarisPrincipalSpec defines the desired state of a Polaris principal.
type PolarisPrincipalSpec struct {
	// connectionRef names a PolarisConnection in the same namespace.
	// +kubebuilder:validation:Required
	ConnectionRef string `json:"connectionRef"`

	// principalName is the name used by Polaris and may differ from the Kubernetes name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Required
	PrincipalName string `json:"principalName"`

	// properties are non-secret Polaris principal properties.
	// +optional
	Properties map[string]string `json:"properties,omitempty"`

	// credentialsSecretName is the name of the same-namespace Secret created and maintained by this controller.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	CredentialsSecretName string `json:"credentialsSecretName"`

	// credentialsSecretTemplate configures labels and annotations on the credentials Secret.
	// +optional
	CredentialsSecretTemplate *PolarisPrincipalSecretTemplate `json:"credentialsSecretTemplate,omitempty"`

	// credentialRotationRequestID requests one explicit credential rotation when greater than the last completed request ID.
	// Increase the value for every rotation request. Removing the field does not rotate credentials.
	// +kubebuilder:validation:Minimum=1
	// +optional
	CredentialRotationRequestID *int64 `json:"credentialRotationRequestID,omitempty"`
}

// PolarisPrincipalSecretTemplate configures metadata applied to a principal credentials Secret.
type PolarisPrincipalSecretTemplate struct {
	// metadata contains labels and annotations managed on the credentials Secret.
	// +optional
	Metadata *PolarisPrincipalSecretTemplateMetadata `json:"metadata,omitempty"`
}

// PolarisPrincipalSecretTemplateMetadata contains user-configurable Secret metadata.
type PolarisPrincipalSecretTemplateMetadata struct {
	// labels are merged into the credentials Secret and reconciled by this resource.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// annotations are merged into the credentials Secret and reconciled by this resource.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// PolarisPrincipalStatus defines the observed state of PolarisPrincipal.
type PolarisPrincipalStatus struct {
	// conditions represent the current state of the resource and its generated credentials.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// entityVersion is Polaris' optimistic-concurrency version for the principal.
	// +optional
	EntityVersion int64 `json:"entityVersion,omitempty"`

	// credentialsSecretResourceVersion records the Secret revision containing the latest credentials.
	// +optional
	CredentialsSecretResourceVersion string `json:"credentialsSecretResourceVersion,omitempty"`

	// lastCredentialRotationRequestID records the last explicit rotation request attempted.
	// +optional
	LastCredentialRotationRequestID int64 `json:"lastCredentialRotationRequestID,omitempty"`

	// lastReconciledTime is the last successful reconciliation.
	// +optional
	LastReconciledTime *metav1.Time `json:"lastReconciledTime,omitempty"`

	// credentialsSecretTemplateManagedLabelKeys lists label keys last managed by the Secret template.
	// +optional
	CredentialsSecretTemplateManagedLabelKeys []string `json:"credentialsSecretTemplateManagedLabelKeys,omitempty"`

	// credentialsSecretTemplateManagedAnnotationKeys lists annotation keys last managed by the Secret template.
	// +optional
	CredentialsSecretTemplateManagedAnnotationKeys []string `json:"credentialsSecretTemplateManagedAnnotationKeys,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Principal",type=string,JSONPath=".spec.principalName"
type PolarisPrincipal struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PolarisPrincipalSpec   `json:"spec"`
	Status            PolarisPrincipalStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PolarisPrincipalList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolarisPrincipal `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisPrincipal{}, &PolarisPrincipalList{})
		return nil
	})
}
