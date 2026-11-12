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

// PolarisPolicyBindingSpec defines one policy-to-resource attachment.
type PolarisPolicyBindingSpec struct {
	// policyRef names a PolarisPolicy in the same Kubernetes namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	PolicyRef string `json:"policyRef"`

	// targetType selects the catalog, namespace, or table-like target.
	// Polaris represents both tables and views as table-like targets.
	// +kubebuilder:validation:Enum=catalog;namespace;table-like
	// +kubebuilder:validation:Required
	TargetType string `json:"targetType"`

	// namespace is the ordered path for namespace and table-like targets.
	// It must be empty for a catalog target.
	// +optional
	Namespace []string `json:"namespace,omitempty"`

	// targetName is the table or view name for a table-like target.
	// +optional
	TargetName string `json:"targetName,omitempty"`

	// parameters are Polaris mapping parameters.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// PolarisPolicyBindingStatus defines the observed state of a policy attachment.
type PolarisPolicyBindingStatus struct {
	// conditions represent the current state of the resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// observedPolicyRef records the policy resource used for this mapping.
	// +optional
	ObservedPolicyRef string `json:"observedPolicyRef,omitempty"`

	// observedCatalogRef records the PolarisCatalog resource used for this mapping.
	// +optional
	ObservedCatalogRef string `json:"observedCatalogRef,omitempty"`

	// observedCatalogName records the remote catalog identity.
	// +optional
	ObservedCatalogName string `json:"observedCatalogName,omitempty"`

	// observedConnectionRef records the PolarisConnection used for this mapping.
	// +optional
	ObservedConnectionRef string `json:"observedConnectionRef,omitempty"`

	// observedNamespace records the remote namespace path containing the policy.
	// +optional
	ObservedNamespace []string `json:"observedNamespace,omitempty"`

	// observedPolicyName records the remote policy identity.
	// +optional
	ObservedPolicyName string `json:"observedPolicyName,omitempty"`

	// observedTargetType records the immutable Polaris attachment target type.
	// +optional
	ObservedTargetType string `json:"observedTargetType,omitempty"`

	// observedTargetNamespace records the immutable target namespace path.
	// +optional
	ObservedTargetNamespace []string `json:"observedTargetNamespace,omitempty"`

	// observedTargetName records the immutable table or view target name.
	// +optional
	ObservedTargetName string `json:"observedTargetName,omitempty"`

	// lastReconciledTime is the last successful reconciliation.
	// +optional
	LastReconciledTime *metav1.Time `json:"lastReconciledTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Policy",type=string,JSONPath=".status.observedPolicyName"
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=".spec.targetType"
type PolarisPolicyBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PolarisPolicyBindingSpec   `json:"spec"`
	Status            PolarisPolicyBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PolarisPolicyBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolarisPolicyBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisPolicyBinding{}, &PolarisPolicyBindingList{})
		return nil
	})
}
