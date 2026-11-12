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
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// PolarisServerSpec defines the desired state of a Polaris server workload.
// +kubebuilder:validation:XValidation:rule="!has(self.port) || !has(self.managementPort) || self.port != self.managementPort",message="port and managementPort must be different"
type PolarisServerSpec struct {
	// image is the Polaris server container image, including its version tag or digest.
	// The controller does not select or upgrade server versions implicitly.
	// +kubebuilder:validation:MinLength=1
	// +required
	Image string `json:"image"`

	// replicas is the desired number of server replicas.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// port is the container and Service port for the Polaris Catalog API.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8181
	// +optional
	Port *int32 `json:"port,omitempty"`

	// managementPort is the container and Service port for the Polaris Management API.
	// Health probes use this port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8182
	// +optional
	ManagementPort *int32 `json:"managementPort,omitempty"`

	// envFrom imports configuration from existing ConfigMaps and Secrets in this namespace.
	// Database, object-storage, and identity-provider resources remain externally managed.
	// +optional
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`

	// resources configures container resource requests and limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// env adds individual environment variables to the Polaris container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// imagePullSecrets references image pull credentials in this namespace.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// serviceAccountName selects an existing ServiceAccount for the server pods.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// podAnnotations are added to server pod templates.
	// +optional
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`

	// podSecurityContext configures pod-level security settings.
	// +optional
	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext,omitempty"`

	// containerSecurityContext configures the Polaris container security settings.
	// +optional
	ContainerSecurityContext *corev1.SecurityContext `json:"containerSecurityContext,omitempty"`

	// nodeSelector constrains which nodes may run the server pods.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// affinity configures pod scheduling affinity and anti-affinity.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// tolerations allow scheduling onto tainted nodes.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// topologySpreadConstraints controls how pods are spread across topology domains.
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// priorityClassName sets the priority class for server pods.
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// terminationGracePeriodSeconds controls pod shutdown grace time.
	// +kubebuilder:validation:Minimum=0
	// +optional
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`

	// volumes adds caller-managed volumes to server pods.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// volumeMounts adds caller-managed mounts to the Polaris container.
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`

	// initContainers adds caller-managed initialization containers.
	// +optional
	InitContainers []corev1.Container `json:"initContainers,omitempty"`

	// service configures the Service exposing the Polaris Catalog API.
	// The Management API Service remains internal.
	// +optional
	Service PolarisServerServiceSpec `json:"service,omitempty"`

	// ingress optionally creates a networking.k8s.io/v1 Ingress for the Catalog API.
	// The Management API remains available only through the in-cluster Service.
	// +optional
	Ingress *PolarisServerIngressSpec `json:"ingress,omitempty"`
	// httpRoute optionally creates a Gateway API HTTPRoute for the Catalog API.
	// Gateway API CRDs and a compatible Gateway controller must be installed separately.
	// +optional
	HTTPRoute *PolarisServerHTTPRouteSpec `json:"httpRoute,omitempty"`
}

// PolarisServerServiceSpec configures the Kubernetes Service.
type PolarisServerServiceSpec struct {
	// type controls how the Polaris API Service is exposed.
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	// +kubebuilder:default=ClusterIP
	// +optional
	Type corev1.ServiceType `json:"type,omitempty"`

	// annotations are added to the Service metadata.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// PolarisServerIngressSpec configures optional external access to the Catalog API.
type PolarisServerIngressSpec struct {
	// host is the hostname routed to the Polaris Catalog API.
	// +kubebuilder:validation:MinLength=1
	// +required
	Host string `json:"host"`

	// ingressClassName selects the Ingress controller.
	// +optional
	IngressClassName *string `json:"ingressClassName,omitempty"`

	// path is the URL path routed to Polaris. Defaults to /.
	// +kubebuilder:default="/"
	// +optional
	Path string `json:"path,omitempty"`

	// tlsSecretName references a TLS Secret in this namespace.
	// +optional
	TLSSecretName string `json:"tlsSecretName,omitempty"`

	// annotations are added to the Ingress metadata.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// PolarisServerHTTPRouteSpec configures optional Gateway API HTTPRoute access.
type PolarisServerHTTPRouteSpec struct {
	// parentRefs identify Gateway or Service parents that accept this route.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +required
	ParentRefs []gatewayv1.ParentReference `json:"parentRefs"`

	// hostnames restrict requests routed by the parent listeners.
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Hostnames []gatewayv1.Hostname `json:"hostnames,omitempty"`

	// path is the path prefix routed to the Polaris Catalog API. Defaults to /.
	// +kubebuilder:default="/"
	// +optional
	Path string `json:"path,omitempty"`

	// annotations are added to the HTTPRoute metadata.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// PolarisServerStatus defines the observed state of PolarisServer.
type PolarisServerStatus struct {
	// conditions represent the current state of the PolarisServer resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the generation most recently reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// readyReplicas is the number of ready Polaris server pods.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// serviceName is the in-namespace Service exposing the Catalog API.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// managementServiceName is the internal Service exposing the Management API.
	// +optional
	ManagementServiceName string `json:"managementServiceName,omitempty"`
	// httpRouteName is the name of the managed Gateway API HTTPRoute, when configured.
	// +optional
	HTTPRouteName string `json:"httpRouteName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Ready Replicas",type=integer,JSONPath=".status.readyReplicas"

// PolarisServer manages an isolated Polaris server Deployment and separate Catalog and Management Services.
// It does not provision a database, object store, identity provider, or Polaris API entities.
type PolarisServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PolarisServerSpec   `json:"spec"`
	Status PolarisServerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PolarisServerList contains a list of PolarisServer.
type PolarisServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolarisServer `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PolarisServer{}, &PolarisServerList{})
		return nil
	})
}
