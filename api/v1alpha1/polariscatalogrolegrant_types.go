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

// PolarisGrantSpec identifies one privilege on a Polaris securable.
type PolarisGrantSpec struct {
	// type identifies the securable receiving the privilege.
	// +kubebuilder:validation:Enum=catalog;namespace;table;view;policy
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// namespace is the ordered namespace path for namespace-, table-, view-, and policy grants.
	// +kubebuilder:validation:MinItems=1
	// +optional
	Namespace []string `json:"namespace,omitempty"`

	// tableName selects a table when type is table.
	// +optional
	TableName string `json:"tableName,omitempty"`

	// viewName selects a view when type is view.
	// +optional
	ViewName string `json:"viewName,omitempty"`

	// policyName selects a policy when type is policy.
	// +optional
	PolicyName string `json:"policyName,omitempty"`

	// privilege is a Polaris privilege supported by the selected resource type and server version.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Required
	Privilege string `json:"privilege"`
}

// PolarisCatalogRoleGrantSpec defines one privilege grant on a catalog role.
type PolarisCatalogRoleGrantSpec struct {
	// catalogRoleRef names a PolarisCatalogRole in the same namespace.
	// +kubebuilder:validation:Required
	CatalogRoleRef string `json:"catalogRoleRef"`

	// grant identifies the securable and privilege to grant.
	// +kubebuilder:validation:Required
	Grant PolarisGrantSpec `json:"grant"`

	// cascade controls whether revoking this grant also revokes grants on subresources.
	// This is only used when deleting this resource and defaults to false.
	// +optional
	Cascade bool `json:"cascade,omitempty"`
}

// PolarisCatalogRoleGrantStatus defines the observed state of a grant.
type PolarisCatalogRoleGrantStatus struct {
	// conditions represent the current state of the resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// observedCatalogRoleRef records the role resource that owns this grant.
	// +optional
	ObservedCatalogRoleRef string `json:"observedCatalogRoleRef,omitempty"`

	// observedCatalogName records the remote catalog identity.
	// +optional
	ObservedCatalogName string `json:"observedCatalogName,omitempty"`

	// observedCatalogRoleName records the remote catalog role identity.
	// +optional
	ObservedCatalogRoleName string `json:"observedCatalogRoleName,omitempty"`

	// observedConnectionRef records the connection used for this grant.
	// +optional
	ObservedConnectionRef string `json:"observedConnectionRef,omitempty"`

	// observedGrant is the grant identity last managed by this resource.
	// +optional
	ObservedGrant *PolarisGrantSpec `json:"observedGrant,omitempty"`

	// lastReconciledTime is the last successful reconciliation.
	// +optional
	LastReconciledTime *metav1.Time `json:"lastReconciledTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".status.observedCatalogRoleName"
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=".spec.grant.type"
type PolarisCatalogRoleGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PolarisCatalogRoleGrantSpec   `json:"spec"`
	Status            PolarisCatalogRoleGrantStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PolarisCatalogRoleGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolarisCatalogRoleGrant `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisCatalogRoleGrant{}, &PolarisCatalogRoleGrantList{})
		return nil
	})
}
