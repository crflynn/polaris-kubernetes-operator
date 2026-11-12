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

// PolarisTableSpec describes a registered Iceberg table.
// +kubebuilder:validation:XValidation:rule="!has(self.ownershipMode) || self.ownershipMode != 'Enforce' || (has(self.metadataLocation) && size(self.metadataLocation) > 0)",message="metadataLocation is required when ownershipMode is Enforce"
// +kubebuilder:validation:XValidation:rule="!has(self.deletionPolicy) || self.deletionPolicy == 'Orphan' || (has(self.ownershipMode) && self.ownershipMode == 'Enforce')",message="deletionPolicy Unregister or Delete requires ownershipMode Enforce"
type PolarisTableSpec struct {
	// catalogRef names a PolarisCatalog in the same Kubernetes namespace.
	// +kubebuilder:validation:MinLength=1
	CatalogRef string `json:"catalogRef"`

	// namespace is the ordered Polaris namespace path.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MinLength=1
	Namespace []string `json:"namespace"`

	// tableName is the Polaris table identifier.
	// +kubebuilder:validation:MinLength=1
	TableName string `json:"tableName"`

	// metadataLocation is an existing Iceberg metadata file to register when the table is absent.
	// Enforce mode registers only; it never overwrites an existing registration.
	// +optional
	MetadataLocation string `json:"metadataLocation,omitempty"`

	// ownershipMode controls whether the operator observes, adopts, or registers the remote table.
	// +kubebuilder:validation:Enum=Observe;Adopt;Enforce
	// +kubebuilder:default=Observe
	// +optional
	OwnershipMode PolarisOwnershipMode `json:"ownershipMode,omitempty"`

	// deletionPolicy controls remote cleanup. Unregister detaches the catalog entry without deleting files.
	// +kubebuilder:validation:Enum=Orphan;Unregister;Delete
	// +kubebuilder:default=Orphan
	// +optional
	DeletionPolicy PolarisDeletionPolicy `json:"deletionPolicy,omitempty"`
}

// PolarisTableStatus reports observed remote table identity and registration drift.
type PolarisTableStatus struct {
	// conditions represent the current state of the resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the latest generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// remoteCatalogName is the recorded Polaris catalog identity.
	// +optional
	RemoteCatalogName string `json:"remoteCatalogName,omitempty"`

	// remoteNamespace is the recorded Polaris namespace path.
	// +optional
	RemoteNamespace []string `json:"remoteNamespace,omitempty"`

	// remoteTableName is the recorded table name.
	// +optional
	RemoteTableName string `json:"remoteTableName,omitempty"`

	// remoteMetadataLocation is the observed Iceberg metadata file location.
	// +optional
	RemoteMetadataLocation string `json:"remoteMetadataLocation,omitempty"`

	// driftedFields lists registration fields differing from the desired state.
	// +optional
	DriftedFields []string `json:"driftedFields,omitempty"`

	// lastReconciledTime is the last successful observation.
	// +optional
	LastReconciledTime *metav1.Time `json:"lastReconciledTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// PolarisTable is the Schema for the polaristables API
type PolarisTable struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of PolarisTable
	// +required
	Spec PolarisTableSpec `json:"spec"`

	// status defines the observed state of PolarisTable
	// +optional
	Status PolarisTableStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PolarisTableList contains a list of PolarisTable
type PolarisTableList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PolarisTable `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisTable{}, &PolarisTableList{})
		return nil
	})
}
