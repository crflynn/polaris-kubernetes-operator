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

// PolarisGenericTableSpec defines the desired state of one Polaris generic table.
// +kubebuilder:validation:XValidation:rule="!has(self.deletionPolicy) || self.deletionPolicy != 'Delete' || (has(self.ownershipMode) && self.ownershipMode == 'Enforce')",message="deletionPolicy Delete requires ownershipMode Enforce"
type PolarisGenericTableSpec struct {
	// catalogRef names a PolarisCatalog in the same Kubernetes namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	CatalogRef string `json:"catalogRef"`

	// namespace is the ordered list of Polaris namespace levels.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:Required
	Namespace []string `json:"namespace"`

	// tableName is the Polaris generic-table name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	TableName string `json:"tableName"`

	// format identifies the generic table format, such as delta or csv.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Format string `json:"format"`

	// baseLocation optionally records the table's backing location.
	// +optional
	BaseLocation string `json:"baseLocation,omitempty"`

	// doc optionally records a description of the table.
	// +optional
	Doc string `json:"doc,omitempty"`

	// properties are user-defined generic-table properties.
	// +optional
	Properties map[string]string `json:"properties,omitempty"`

	// ownershipMode controls whether the controller observes or creates the remote table.
	// +kubebuilder:validation:Enum=Observe;Adopt;Enforce
	// +kubebuilder:default=Observe
	// +optional
	OwnershipMode PolarisOwnershipMode `json:"ownershipMode,omitempty"`

	// deletionPolicy controls remote cleanup. Delete is honored only with Enforce.
	// +kubebuilder:validation:Enum=Orphan;Delete
	// +kubebuilder:default=Orphan
	// +optional
	DeletionPolicy PolarisDeletionPolicy `json:"deletionPolicy,omitempty"`
}

// PolarisGenericTableStatus defines the observed state of PolarisGenericTable.
type PolarisGenericTableStatus struct {
	// conditions represent the current state of the resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// remoteCatalogName records the resolved Polaris catalog identity.
	// +optional
	RemoteCatalogName string `json:"remoteCatalogName,omitempty"`

	// remoteNamespace records the resolved Polaris namespace path.
	// +optional
	RemoteNamespace []string `json:"remoteNamespace,omitempty"`

	// remoteTableName records the observed Polaris generic-table name.
	// +optional
	RemoteTableName string `json:"remoteTableName,omitempty"`

	// remoteFormat records Polaris' generic-table format.
	// +optional
	RemoteFormat string `json:"remoteFormat,omitempty"`

	// remoteBaseLocation records Polaris' optional table location.
	// +optional
	RemoteBaseLocation string `json:"remoteBaseLocation,omitempty"`

	// driftedFields lists desired fields that differ from Polaris.
	// +optional
	DriftedFields []string `json:"driftedFields,omitempty"`

	// lastReconciledTime is the last successful remote observation or mutation.
	// +optional
	LastReconciledTime *metav1.Time `json:"lastReconciledTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Table",type=string,JSONPath=".status.remoteTableName"
// PolarisGenericTable is the Schema for the polarisgenerictables API.
type PolarisGenericTable struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitzero"`
	Spec              PolarisGenericTableSpec   `json:"spec"`
	Status            PolarisGenericTableStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true
// PolarisGenericTableList contains a list of PolarisGenericTable.
type PolarisGenericTableList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PolarisGenericTable `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisGenericTable{}, &PolarisGenericTableList{})
		return nil
	})
}
