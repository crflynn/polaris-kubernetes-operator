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

// PolarisCatalogRoleBindingSpec defines one principal-role to catalog-role assignment.
type PolarisCatalogRoleBindingSpec struct {
	// principalRoleRef names a PolarisPrincipalRole in the same namespace.
	// +kubebuilder:validation:Required
	PrincipalRoleRef string `json:"principalRoleRef"`

	// catalogRoleRef names a PolarisCatalogRole in the same namespace.
	// +kubebuilder:validation:Required
	CatalogRoleRef string `json:"catalogRoleRef"`
}

// PolarisCatalogRoleBindingStatus defines the observed state of the assignment.
type PolarisCatalogRoleBindingStatus struct {
	// conditions represent the current state of the resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// observedPrincipalRoleRef records the role resource used for this assignment.
	// +optional
	ObservedPrincipalRoleRef string `json:"observedPrincipalRoleRef,omitempty"`

	// observedPrincipalRoleName records the Polaris principal role identity.
	// +optional
	ObservedPrincipalRoleName string `json:"observedPrincipalRoleName,omitempty"`

	// observedCatalogRoleRef records the catalog role resource used for this assignment.
	// +optional
	ObservedCatalogRoleRef string `json:"observedCatalogRoleRef,omitempty"`

	// observedCatalogName records the remote catalog identity.
	// +optional
	ObservedCatalogName string `json:"observedCatalogName,omitempty"`

	// observedCatalogRoleName records the Polaris catalog role identity.
	// +optional
	ObservedCatalogRoleName string `json:"observedCatalogRoleName,omitempty"`

	// observedConnectionRef records the connection used for this assignment.
	// +optional
	ObservedConnectionRef string `json:"observedConnectionRef,omitempty"`

	// lastReconciledTime is the last successful reconciliation.
	// +optional
	LastReconciledTime *metav1.Time `json:"lastReconciledTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="PrincipalRole",type=string,JSONPath=".status.observedPrincipalRoleName"
// +kubebuilder:printcolumn:name="CatalogRole",type=string,JSONPath=".status.observedCatalogRoleName"
type PolarisCatalogRoleBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PolarisCatalogRoleBindingSpec   `json:"spec"`
	Status            PolarisCatalogRoleBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PolarisCatalogRoleBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolarisCatalogRoleBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisCatalogRoleBinding{}, &PolarisCatalogRoleBindingList{})
		return nil
	})
}
