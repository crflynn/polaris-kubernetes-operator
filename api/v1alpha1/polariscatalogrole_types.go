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

// PolarisCatalogRoleSpec defines the desired state of a Polaris catalog role.
type PolarisCatalogRoleSpec struct {
	// catalogRef names a PolarisCatalog in the same namespace.
	// +kubebuilder:validation:Required
	CatalogRef string `json:"catalogRef"`

	// catalogRoleName is the name used by Polaris and may differ from the Kubernetes name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Required
	CatalogRoleName string `json:"catalogRoleName"`

	// properties are non-secret Polaris role properties.
	// +optional
	Properties map[string]string `json:"properties,omitempty"`
}

// PolarisCatalogRoleStatus defines the observed state of PolarisCatalogRole.
type PolarisCatalogRoleStatus struct {
	// conditions represent the current state of the resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// entityVersion is Polaris' optimistic-concurrency version for this role.
	// +optional
	EntityVersion int64 `json:"entityVersion,omitempty"`

	// observedCatalogRef records the PolarisCatalog that identifies the remote catalog scope.
	// +optional
	ObservedCatalogRef string `json:"observedCatalogRef,omitempty"`

	// observedCatalogName records the remote catalog name used for this role.
	// +optional
	ObservedCatalogName string `json:"observedCatalogName,omitempty"`

	// observedConnectionRef records the connection used to manage this role.
	// +optional
	ObservedConnectionRef string `json:"observedConnectionRef,omitempty"`

	// observedRoleName records the role name used in Polaris and prevents identity changes from orphaning roles.
	// +optional
	ObservedRoleName string `json:"observedRoleName,omitempty"`

	// lastReconciledTime is the last successful reconciliation.
	// +optional
	LastReconciledTime *metav1.Time `json:"lastReconciledTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".spec.catalogRoleName"
type PolarisCatalogRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PolarisCatalogRoleSpec   `json:"spec"`
	Status            PolarisCatalogRoleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PolarisCatalogRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolarisCatalogRole `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisCatalogRole{}, &PolarisCatalogRoleList{})
		return nil
	})
}
