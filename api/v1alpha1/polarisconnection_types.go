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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	PolarisConnectionReadyConditionType = "Ready"
)

// PolarisConnectionSpec defines the desired state of a Polaris API connection.
type PolarisConnectionSpec struct {
	// managementURL is the base URL for the Polaris Management Service API.
	// +kubebuilder:validation:Format=uri
	// +required
	ManagementURL string `json:"managementURL"`

	// catalogURL is the base URL for the Polaris Catalog Service API.
	// It should end before the service's /v1 path.
	// +optional
	// +kubebuilder:validation:Format=uri
	CatalogURL string `json:"catalogURL,omitempty"`

	// tokenURL is the OAuth2 token endpoint used with client credentials.
	// It is not needed when bearerTokenSecretRef is configured.
	// +optional
	// +kubebuilder:validation:Format=uri
	TokenURL string `json:"tokenURL,omitempty"`

	// realm selects the Polaris realm sent in the configured realm header.
	// +optional
	Realm string `json:"realm,omitempty"`

	// realmHeader is the HTTP header used to carry realm context.
	// +kubebuilder:default="Polaris-Realm"
	// +optional
	RealmHeader string `json:"realmHeader,omitempty"`

	// auth contains exactly one supported authentication method.
	// +required
	// +kubebuilder:validation:XValidation:rule="has(self.clientCredentials) != has(self.bearerTokenSecretRef)",message="exactly one of clientCredentials or bearerTokenSecretRef must be configured"
	Auth PolarisConnectionAuth `json:"auth"`

	// tls configures trust for private or self-managed Polaris endpoints.
	// +optional
	TLS *PolarisTLSConfig `json:"tls,omitempty"`

	// timeoutSeconds is the per-request timeout used by connection checks.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=300
	// +kubebuilder:default=10
	// +optional
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`

	// enableBetaGenericTables explicitly opts into the Polaris 1.8 beta Generic Table API.
	// Polaris /v1/config does not advertise this native API, so support must be verified for the target server.
	// +optional
	EnableBetaGenericTables bool `json:"enableBetaGenericTables,omitempty"`
}

// PolarisConnectionAuth selects credentials used by the operator.
type PolarisConnectionAuth struct {
	// clientCredentials references a Secret containing OAuth client credentials.
	// +optional
	ClientCredentials *PolarisClientCredentials `json:"clientCredentials,omitempty"`

	// bearerTokenSecretRef references a Secret key containing a bearer token.
	// The reference is resolved in the PolarisConnection namespace.
	// +optional
	BearerTokenSecretRef *corev1.SecretKeySelector `json:"bearerTokenSecretRef,omitempty"`
}

// PolarisClientCredentials references OAuth client credentials in a Secret.
type PolarisClientCredentials struct {
	// secretRef references the Secret containing the client ID and secret.
	// +required
	SecretRef corev1.LocalObjectReference `json:"secretRef"`

	// clientIDKey is the Secret key containing the OAuth client ID.
	// +kubebuilder:default="client-id"
	// +optional
	ClientIDKey string `json:"clientIDKey,omitempty"`

	// clientSecretKey is the Secret key containing the OAuth client secret.
	// +kubebuilder:default="client-secret"
	// +optional
	ClientSecretKey string `json:"clientSecretKey,omitempty"`

	// scopes are sent as a space-delimited OAuth scope value.
	// +optional
	Scopes []string `json:"scopes,omitempty"`
}

// PolarisTLSConfig configures custom trust for the Polaris endpoint.
type PolarisTLSConfig struct {
	// caSecretRef references a Secret key containing PEM-encoded CA certificates.
	// The reference is resolved in the PolarisConnection namespace.
	// +required
	CASecretRef corev1.SecretKeySelector `json:"caSecretRef"`
}

// PolarisConnectionStatus defines the observed state of PolarisConnection.
type PolarisConnectionStatus struct {
	// conditions represent the current state of the PolarisConnection resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// managementURL is the endpoint successfully checked by the controller.
	// +optional
	ManagementURL string `json:"managementURL,omitempty"`

	// catalogURL is the catalog endpoint successfully checked by the controller.
	// +optional
	CatalogURL string `json:"catalogURL,omitempty"`

	// capabilities lists endpoint paths advertised by the catalog service.
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`
	// namespaceSeparator is the URL-decoded separator advertised by Polaris /v1/config.
	// +optional
	NamespaceSeparator string `json:"namespaceSeparator,omitempty"`

	// lastCheckedTime is the last time the remote endpoints were checked.
	// +optional
	LastCheckedTime *metav1.Time `json:"lastCheckedTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Management URL",type=string,JSONPath=".status.managementURL"

// PolarisConnection is the Schema for the polarisconnections API.
type PolarisConnection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PolarisConnectionSpec   `json:"spec"`
	Status PolarisConnectionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PolarisConnectionList contains a list of PolarisConnection.
type PolarisConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolarisConnection `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisConnection{}, &PolarisConnectionList{})
		return nil
	})
}
