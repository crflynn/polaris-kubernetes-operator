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

// PolarisPrincipalRoleBindingSpec defines one principal-to-role assignment.
type PolarisPrincipalRoleBindingSpec struct {
	// principalRef names a PolarisPrincipal in the same namespace.
	// +kubebuilder:validation:Required
	PrincipalRef string `json:"principalRef"`

	// principalRoleRef names a PolarisPrincipalRole in the same namespace.
	// +kubebuilder:validation:Required
	PrincipalRoleRef string `json:"principalRoleRef"`
}

// PolarisPrincipalRoleBindingStatus defines the observed state of the assignment.
type PolarisPrincipalRoleBindingStatus struct {
	// conditions represent the current state of the resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// observedPrincipalName records the Polaris principal identity for safe cleanup.
	// +optional
	ObservedPrincipalName string `json:"observedPrincipalName,omitempty"`

	// observedPrincipalRoleName records the Polaris role identity for safe cleanup.
	// +optional
	ObservedPrincipalRoleName string `json:"observedPrincipalRoleName,omitempty"`

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
// +kubebuilder:printcolumn:name="Principal",type=string,JSONPath=".status.observedPrincipalName"
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".status.observedPrincipalRoleName"
type PolarisPrincipalRoleBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PolarisPrincipalRoleBindingSpec   `json:"spec"`
	Status            PolarisPrincipalRoleBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PolarisPrincipalRoleBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolarisPrincipalRoleBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisPrincipalRoleBinding{}, &PolarisPrincipalRoleBindingList{})
		return nil
	})
}
