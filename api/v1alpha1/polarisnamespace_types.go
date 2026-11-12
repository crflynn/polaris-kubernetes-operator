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

// PolarisOwnershipMode describes how a resource reconciles remote Polaris state.
type PolarisOwnershipMode string

const (
	PolarisOwnershipObserve PolarisOwnershipMode = "Observe"
	PolarisOwnershipAdopt   PolarisOwnershipMode = "Adopt"
	PolarisOwnershipEnforce PolarisOwnershipMode = "Enforce"
)

// PolarisDeletionPolicy controls remote cleanup when a managed resource is deleted.
type PolarisDeletionPolicy string

const (
	PolarisDeletionOrphan     PolarisDeletionPolicy = "Orphan"
	PolarisDeletionUnregister PolarisDeletionPolicy = "Unregister"
	PolarisDeletionDelete     PolarisDeletionPolicy = "Delete"
)

// PolarisNamespaceSpec defines one nested Polaris namespace.
// +kubebuilder:validation:XValidation:rule="!has(self.deletionPolicy) || self.deletionPolicy != 'Delete' || (has(self.ownershipMode) && self.ownershipMode == 'Enforce')",message="deletionPolicy Delete requires ownershipMode Enforce"
type PolarisNamespaceSpec struct {
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

	// ownershipMode controls whether the operator observes, adopts, or enforces the remote namespace.
	// +kubebuilder:validation:Enum=Observe;Adopt;Enforce
	// +kubebuilder:default=Observe
	// +optional
	OwnershipMode PolarisOwnershipMode `json:"ownershipMode,omitempty"`

	// deletionPolicy controls remote namespace cleanup. Delete is honored only with Enforce.
	// +kubebuilder:validation:Enum=Orphan;Delete
	// +kubebuilder:default=Orphan
	// +optional
	DeletionPolicy PolarisDeletionPolicy `json:"deletionPolicy,omitempty"`

	// properties are Polaris namespace properties managed by this resource.
	// The controller manages only keys present in this map or previously recorded as managed.
	// +optional
	Properties map[string]string `json:"properties,omitempty"`
}

// PolarisNamespaceStatus defines the observed state of a PolarisNamespace.
type PolarisNamespaceStatus struct {
	// conditions represent the current state of the PolarisNamespace resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// remoteCatalogName records the immutable Polaris catalog identity.
	// +optional
	RemoteCatalogName string `json:"remoteCatalogName,omitempty"`

	// remoteNamespace records the immutable Polaris namespace identity.
	// +optional
	RemoteNamespace []string `json:"remoteNamespace,omitempty"`

	// managedPropertyKeys tracks the property keys owned by this resource.
	// +optional
	ManagedPropertyKeys []string `json:"managedPropertyKeys,omitempty"`

	// driftedFields lists spec fields that differ from Polaris in Observe mode.
	// +optional
	DriftedFields []string `json:"driftedFields,omitempty"`

	// lastReconciledTime is the last successful remote observation or mutation.
	// +optional
	LastReconciledTime *metav1.Time `json:"lastReconciledTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// PolarisNamespace is the Schema for the polarisnamespaces API
type PolarisNamespace struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of PolarisNamespace
	// +required
	Spec PolarisNamespaceSpec `json:"spec"`

	// status defines the observed state of PolarisNamespace
	// +optional
	Status PolarisNamespaceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PolarisNamespaceList contains a list of PolarisNamespace
type PolarisNamespaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PolarisNamespace `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisNamespace{}, &PolarisNamespaceList{})
		return nil
	})
}
