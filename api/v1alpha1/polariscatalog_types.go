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

// +kubebuilder:validation:XValidation:rule="(self.type == 'EXTERNAL') == has(self.externalCatalog)",message="externalCatalog must be set only for EXTERNAL catalogs"
type PolarisCatalogSpec struct {
	// connectionRef names a PolarisConnection in the same namespace.
	// +kubebuilder:validation:Required
	ConnectionRef string `json:"connectionRef"`

	// catalogName is the name used by Polaris; it need not match the Kubernetes name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Required
	CatalogName string `json:"catalogName"`

	// type selects an internal or external Polaris catalog.
	// +kubebuilder:validation:Enum=INTERNAL;EXTERNAL
	// +kubebuilder:default=INTERNAL
	// +optional
	Type string `json:"type,omitempty"`

	// externalCatalog configures an Iceberg REST catalog. Authentication values are read from a Secret.
	// +optional
	ExternalCatalog *PolarisExternalCatalogConfig `json:"externalCatalog,omitempty"`

	// properties are Polaris catalog properties. Do not put credentials here.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="'default-base-location' in self",message="properties must include default-base-location"
	Properties map[string]string `json:"properties"`

	// storageConfigInfo describes the Polaris catalog storage backend.
	// Provider credentials must be managed by Polaris, not stored here.
	// +kubebuilder:validation:Required
	StorageConfigInfo PolarisStorageConfigInfo `json:"storageConfigInfo"`
}

// PolarisExternalCatalogConfig describes the remote Iceberg REST catalog endpoint.
type PolarisExternalCatalogConfig struct {
	// uri is the base URL of the remote Iceberg REST catalog.
	// +kubebuilder:validation:Format=uri
	// +kubebuilder:validation:Required
	URI string `json:"uri"`

	// remoteCatalogName selects a logical catalog served by the endpoint.
	// +optional
	RemoteCatalogName string `json:"remoteCatalogName,omitempty"`

	// properties contains non-secret remote connection properties.
	// +optional
	Properties map[string]string `json:"properties,omitempty"`

	// authentication configures OAuth client credentials or a bearer token.
	// +kubebuilder:validation:Required
	Authentication PolarisExternalCatalogAuthentication `json:"authentication"`
}

// PolarisExternalCatalogAuthentication selects a Secret-backed auth method.
// +kubebuilder:validation:XValidation:rule="(self.type == 'OAUTH') == has(self.clientCredentials)",message="OAUTH requires clientCredentials"
// +kubebuilder:validation:XValidation:rule="(self.type == 'BEARER') == has(self.bearerTokenSecretRef)",message="BEARER requires bearerTokenSecretRef"
// +kubebuilder:validation:XValidation:rule="!has(self.bearerTokenSecretRef) || size(self.bearerTokenSecretRef.name) > 0",message="bearerTokenSecretRef.name is required"
type PolarisExternalCatalogAuthentication struct {
	// type selects OAuth client credentials or a bearer token.
	// +kubebuilder:validation:Enum=OAUTH;BEARER
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// clientCredentials references the OAuth credentials Secret.
	// +optional
	ClientCredentials *PolarisExternalCatalogClientCredentials `json:"clientCredentials,omitempty"`

	// bearerTokenSecretRef references the Secret key containing a bearer token.
	// +optional
	BearerTokenSecretRef *corev1.SecretKeySelector `json:"bearerTokenSecretRef,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="size(self.secretRef.name) > 0",message="secretRef.name is required"
// PolarisExternalCatalogClientCredentials references a Secret containing OAuth credentials.
type PolarisExternalCatalogClientCredentials struct {
	// secretRef references the Secret containing the client ID and secret.
	// +kubebuilder:validation:Required
	SecretRef corev1.LocalObjectReference `json:"secretRef"`

	// tokenURL is the OAuth token endpoint.
	// +kubebuilder:validation:Format=uri
	// +kubebuilder:validation:Required
	TokenURL string `json:"tokenURL"`

	// clientIDKey selects the client ID key in the Secret.
	// +kubebuilder:default=client-id
	// +optional
	ClientIDKey string `json:"clientIDKey,omitempty"`

	// clientSecretKey selects the client secret key in the Secret.
	// +kubebuilder:default=client-secret
	// +optional
	ClientSecretKey string `json:"clientSecretKey,omitempty"`

	// scopes are OAuth scopes sent as a space-delimited value.
	// +optional
	Scopes []string `json:"scopes,omitempty"`
}

// PolarisStorageConfigInfo is the non-secret storage configuration accepted by Polaris.
type PolarisStorageConfigInfo struct {
	// storageType is the backing storage provider.
	// +kubebuilder:validation:Enum=S3;GCS;AZURE;FILE
	StorageType string `json:"storageType"`

	// allowedLocations restricts the locations usable by the catalog.
	// +optional
	AllowedLocations []string `json:"allowedLocations,omitempty"`

	// storageName references a server-side storage configuration.
	// +optional
	StorageName string `json:"storageName,omitempty"`
}

// PolarisCatalogStatus defines the observed state of PolarisCatalog.
type PolarisCatalogStatus struct {
	// conditions represent the current state of the PolarisCatalog resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// entityVersion is Polaris' optimistic-concurrency version.
	// +optional
	EntityVersion int64 `json:"entityVersion,omitempty"`

	// authenticationSecretResourceVersion tracks the Secret revision used to create an external catalog.
	// Polaris does not support updating external connection authentication through UpdateCatalog.
	// +optional
	AuthenticationSecretResourceVersion string `json:"authenticationSecretResourceVersion,omitempty"`

	// authenticationSecretName identifies the Secret used for external catalog creation.
	// +optional
	AuthenticationSecretName string `json:"authenticationSecretName,omitempty"`

	// authenticationConfigurationHash fingerprints the non-secret external auth configuration.
	// +optional
	AuthenticationConfigurationHash string `json:"authenticationConfigurationHash,omitempty"`

	// lastReconciledTime is the last successful remote reconciliation.
	// +optional
	LastReconciledTime *metav1.Time `json:"lastReconciledTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Catalog",type=string,JSONPath=".spec.catalogName"
type PolarisCatalog struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PolarisCatalogSpec   `json:"spec"`
	Status            PolarisCatalogStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PolarisCatalogList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolarisCatalog `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisCatalog{}, &PolarisCatalogList{})
		return nil
	})
}
