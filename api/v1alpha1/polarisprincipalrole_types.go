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

// PolarisPrincipalRoleSpec defines the desired state of a Polaris principal role.
type PolarisPrincipalRoleSpec struct {
	// connectionRef names a PolarisConnection in the same namespace.
	// +kubebuilder:validation:Required
	ConnectionRef string `json:"connectionRef"`

	// roleName is the name used by Polaris and may differ from the Kubernetes name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Required
	RoleName string `json:"roleName"`

	// federated indicates the principal role is managed by an external identity provider.
	// Polaris does not support changing this value after creation.
	// +optional
	Federated bool `json:"federated,omitempty"`

	// properties are non-secret Polaris role properties.
	// +optional
	Properties map[string]string `json:"properties,omitempty"`
}

// PolarisPrincipalRoleStatus defines the observed state of a PolarisPrincipalRole.
type PolarisPrincipalRoleStatus struct {
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

	// lastReconciledTime is the last successful reconciliation.
	// +optional
	LastReconciledTime *metav1.Time `json:"lastReconciledTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".spec.roleName"
type PolarisPrincipalRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PolarisPrincipalRoleSpec   `json:"spec"`
	Status            PolarisPrincipalRoleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PolarisPrincipalRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolarisPrincipalRole `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisPrincipalRole{}, &PolarisPrincipalRoleList{})
		return nil
	})
}
