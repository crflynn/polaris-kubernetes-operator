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

// PolarisPolicySpec defines the desired state of one policy stored in a Polaris namespace.
type PolarisPolicySpec struct {
	// catalogRef names a PolarisCatalog in the same Kubernetes namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	CatalogRef string `json:"catalogRef"`

	// namespace is the ordered Polaris namespace path that stores this policy.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:Required
	Namespace []string `json:"namespace"`

	// policyName is the remote policy identifier.
	// +kubebuilder:validation:Pattern="^[A-Za-z0-9_-]+$"
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Required
	PolicyName string `json:"policyName"`

	// type selects the Polaris policy validator.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// description explains the policy purpose.
	// +optional
	Description string `json:"description,omitempty"`

	// content is the policy document validated by Polaris.
	// +optional
	Content string `json:"content,omitempty"`

	// detachAllOnDelete explicitly requests Polaris to remove all mappings when this policy is deleted.
	// Use only when removing mappings not represented by PolarisPolicyBinding resources is intended.
	// +optional
	DetachAllOnDelete bool `json:"detachAllOnDelete,omitempty"`
}

// PolarisPolicyStatus defines the observed state of a policy.
type PolarisPolicyStatus struct {
	// conditions represent the current state of the resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// policyVersion is Polaris' optimistic-concurrency version.
	// +optional
	PolicyVersion int64 `json:"policyVersion,omitempty"`

	// inheritable reports Polaris' policy inheritance behavior.
	// +optional
	Inheritable bool `json:"inheritable,omitempty"`

	// observedCatalogRef records the PolarisCatalog resource that owns the policy scope.
	// +optional
	ObservedCatalogRef string `json:"observedCatalogRef,omitempty"`

	// observedCatalogName records the remote catalog identity.
	// +optional
	ObservedCatalogName string `json:"observedCatalogName,omitempty"`

	// observedConnectionRef records the PolarisConnection used for this policy.
	// +optional
	ObservedConnectionRef string `json:"observedConnectionRef,omitempty"`

	// observedNamespace records the immutable remote namespace path.
	// +optional
	ObservedNamespace []string `json:"observedNamespace,omitempty"`

	// observedPolicyName records the immutable remote policy name.
	// +optional
	ObservedPolicyName string `json:"observedPolicyName,omitempty"`

	// observedType records the immutable remote policy type.
	// +optional
	ObservedType string `json:"observedType,omitempty"`

	// lastReconciledTime is the last successful reconciliation.
	// +optional
	LastReconciledTime *metav1.Time `json:"lastReconciledTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Policy",type=string,JSONPath=".spec.policyName"
// +kubebuilder:printcolumn:name="Version",type=integer,JSONPath=".status.policyVersion"
type PolarisPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PolarisPolicySpec   `json:"spec"`
	Status            PolarisPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PolarisPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolarisPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisPolicy{}, &PolarisPolicyList{})
		return nil
	})
}
