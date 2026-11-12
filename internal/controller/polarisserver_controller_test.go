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

package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
)

const (
	polarisServerTestName          = "polaris"
	polarisServerTestCustomValue   = "custom"
	polarisServerTestPath          = "/polaris"
	polarisServerTestParentGateway = "shared-gateway"
	polarisServerTestHostname      = "polaris.example.test"
)

func TestPolarisServerReconcilesDeploymentAndService(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := polarisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := gatewayv1.Install(scheme); err != nil {
		t.Fatal(err)
	}
	parentNamespace := gatewayv1.Namespace("gateway-system")
	replicas := int32(2)
	port := int32(9000)
	serviceType := corev1.ServiceTypeNodePort
	server := &polarisv1alpha1.PolarisServer{
		ObjectMeta: metav1.ObjectMeta{Name: polarisServerTestName, Namespace: testDefaultNamespace, UID: types.UID("server-uid")},
		Spec: polarisv1alpha1.PolarisServerSpec{
			Image: "apache/polaris:test", Replicas: &replicas, Port: &port,
			Service:            polarisv1alpha1.PolarisServerServiceSpec{Type: serviceType, Annotations: map[string]string{"service": polarisServerTestCustomValue}},
			PodAnnotations:     map[string]string{"pod": polarisServerTestCustomValue},
			ServiceAccountName: "polaris-runtime",
			ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "registry-auth"}},
			NodeSelector:       map[string]string{"zone": "west"},
			Volumes:            []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "polaris-config"}}}}},
			VolumeMounts:       []corev1.VolumeMount{{Name: "config", MountPath: "/etc/polaris"}},
			Ingress:            &polarisv1alpha1.PolarisServerIngressSpec{Host: polarisServerTestHostname, Path: polarisServerTestPath, TLSSecretName: "polaris-tls"},
			HTTPRoute: &polarisv1alpha1.PolarisServerHTTPRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: gatewayv1.ObjectName(polarisServerTestParentGateway), Namespace: &parentNamespace}},
				Hostnames:  []gatewayv1.Hostname{gatewayv1.Hostname(polarisServerTestHostname)},
				Path:       polarisServerTestPath,
			},
		},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisServer{}).
		WithObjects(server).Build()
	reconciler := &PolarisServerReconciler{Client: kubeClient, Scheme: scheme}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: server.Namespace, Name: server.Name}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("Reconcile() did not requeue while replicas were not ready")
	}

	assertPolarisServerDeployment(t, ctx, kubeClient, server)
	assertPolarisServerServices(t, ctx, kubeClient, server, serviceType, port)
	assertPolarisServerIngress(t, ctx, kubeClient, server)
	assertPolarisServerHTTPRoute(t, ctx, kubeClient, server, port)

	ingress := &networkingv1.Ingress{}
	httpRoute := &gatewayv1.HTTPRoute{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: server.Name}, server); err != nil {
		t.Fatalf("get PolarisServer before disabling exposure: %v", err)
	}
	server.Spec.Ingress = nil
	server.Spec.HTTPRoute = nil
	if err := kubeClient.Update(ctx, server); err != nil {
		t.Fatalf("disable PolarisServer exposure: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: server.Namespace, Name: server.Name}}); err != nil {
		t.Fatalf("reconcile after disabling exposure: %v", err)
	}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: server.Name}, ingress); !apierrors.IsNotFound(err) {
		t.Fatalf("Ingress after disabling spec = %v, want NotFound", err)
	}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: server.Name}, httpRoute); !apierrors.IsNotFound(err) {
		t.Fatalf("HTTPRoute after disabling spec = %v, want NotFound", err)
	}
}

func assertPolarisServerDeployment(t *testing.T, ctx context.Context, kubeClient client.Client, server *polarisv1alpha1.PolarisServer) {
	t.Helper()
	deployment := &appsv1.Deployment{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: server.Name}, deployment); err != nil {
		t.Fatalf("get Deployment: %v", err)
	}
	if *deployment.Spec.Replicas != 2 || deployment.Spec.Template.Spec.Containers[0].Image != server.Spec.Image {
		t.Fatalf("Deployment spec = replicas %d, image %q", *deployment.Spec.Replicas, deployment.Spec.Template.Spec.Containers[0].Image)
	}
	container := deployment.Spec.Template.Spec.Containers[0]
	if container.StartupProbe == nil || container.ReadinessProbe == nil || container.LivenessProbe == nil {
		t.Fatal("Deployment is missing one or more health probes")
	}
	if container.ReadinessProbe.HTTPGet.Port.IntValue() != 8182 {
		t.Fatalf("ReadinessProbe port = %d, want management port 8182", container.ReadinessProbe.HTTPGet.Port.IntValue())
	}
	if controller := metav1.GetControllerOf(deployment); controller == nil || controller.UID != server.UID {
		t.Fatalf("Deployment controller owner = %#v, want PolarisServer UID %q", controller, server.UID)
	}
	podSpec := deployment.Spec.Template.Spec
	if podSpec.ServiceAccountName != "polaris-runtime" || podSpec.NodeSelector["zone"] != "west" ||
		len(podSpec.ImagePullSecrets) != 1 || podSpec.ImagePullSecrets[0].Name != "registry-auth" ||
		len(podSpec.Volumes) != 1 || podSpec.Containers[0].VolumeMounts[0].MountPath != "/etc/polaris" ||
		deployment.Spec.Template.Annotations["pod"] != polarisServerTestCustomValue {
		t.Fatalf("Deployment pod configuration = %#v", podSpec)
	}
}

func assertPolarisServerServices(t *testing.T, ctx context.Context, kubeClient client.Client, server *polarisv1alpha1.PolarisServer, serviceType corev1.ServiceType, port int32) {
	t.Helper()
	service := &corev1.Service{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: server.Name}, service); err != nil {
		t.Fatalf("get Service: %v", err)
	}
	if service.Spec.Type != serviceType || len(service.Spec.Ports) != 1 ||
		service.Spec.Ports[0].Port != port || service.Annotations["service"] != polarisServerTestCustomValue {
		t.Fatalf("Catalog Service spec = type %q, ports %#v, annotations %#v", service.Spec.Type, service.Spec.Ports, service.Annotations)
	}
	if controller := metav1.GetControllerOf(service); controller == nil || controller.UID != server.UID {
		t.Fatalf("Service controller owner = %#v, want PolarisServer UID %q", controller, server.UID)
	}
	managementService := &corev1.Service{}
	managementName := polarisServerManagementServiceName(server)
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: managementName}, managementService); err != nil {
		t.Fatalf("get Management Service: %v", err)
	}
	if managementService.Spec.Type != corev1.ServiceTypeClusterIP || len(managementService.Spec.Ports) != 1 ||
		managementService.Spec.Ports[0].Port != 8182 {
		t.Fatalf("Management Service spec = type %q, ports %#v", managementService.Spec.Type, managementService.Spec.Ports)
	}
}

func assertPolarisServerIngress(t *testing.T, ctx context.Context, kubeClient client.Client, server *polarisv1alpha1.PolarisServer) {
	t.Helper()
	ingress := &networkingv1.Ingress{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: server.Name}, ingress); err != nil {
		t.Fatalf("get Ingress: %v", err)
	}
	if ingress.Spec.Rules[0].Host != polarisServerTestHostname ||
		ingress.Spec.Rules[0].HTTP.Paths[0].Path != polarisServerTestPath ||
		ingress.Spec.TLS[0].SecretName != "polaris-tls" {
		t.Fatalf("Ingress spec = %#v", ingress.Spec)
	}
	if ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name != server.Name ||
		ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Name != polarisServerCatalogPortName {
		t.Fatalf("Ingress backend = %#v, want Catalog Service %q", ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service, server.Name)
	}
	if controller := metav1.GetControllerOf(ingress); controller == nil || controller.UID != server.UID {
		t.Fatalf("Ingress controller owner = %#v, want PolarisServer UID %q", controller, server.UID)
	}
}

func assertPolarisServerHTTPRoute(t *testing.T, ctx context.Context, kubeClient client.Client, server *polarisv1alpha1.PolarisServer, port int32) {
	t.Helper()
	httpRoute := &gatewayv1.HTTPRoute{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: server.Name}, httpRoute); err != nil {
		t.Fatalf("get HTTPRoute: %v", err)
	}
	if len(httpRoute.Spec.ParentRefs) != 1 ||
		httpRoute.Spec.ParentRefs[0].Name != gatewayv1.ObjectName(polarisServerTestParentGateway) ||
		len(httpRoute.Spec.Hostnames) != 1 ||
		httpRoute.Spec.Hostnames[0] != gatewayv1.Hostname(polarisServerTestHostname) {
		t.Fatalf("HTTPRoute attachment = %#v", httpRoute.Spec)
	}
	rule := httpRoute.Spec.Rules[0]
	if len(rule.Matches) != 1 || rule.Matches[0].Path == nil ||
		rule.Matches[0].Path.Value == nil || rule.Matches[0].Path.Type == nil ||
		*rule.Matches[0].Path.Value != polarisServerTestPath ||
		*rule.Matches[0].Path.Type != gatewayv1.PathMatchPathPrefix {
		t.Fatalf("HTTPRoute match = %#v", rule.Matches)
	}
	if len(rule.BackendRefs) != 1 || rule.BackendRefs[0].Name != gatewayv1.ObjectName(server.Name) ||
		rule.BackendRefs[0].Port == nil || *rule.BackendRefs[0].Port != port {
		t.Fatalf("HTTPRoute backend = %#v, want Catalog Service %q:%d", rule.BackendRefs, server.Name, port)
	}
	if controller := metav1.GetControllerOf(httpRoute); controller == nil || controller.UID != server.UID {
		t.Fatalf("HTTPRoute controller owner = %#v, want PolarisServer UID %q", controller, server.UID)
	}
}

func TestPolarisServerDoesNotAdoptExistingDeployment(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := polarisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	server := &polarisv1alpha1.PolarisServer{
		ObjectMeta: metav1.ObjectMeta{Name: polarisServerTestName, Namespace: testDefaultNamespace, UID: types.UID("server-uid")},
		Spec:       polarisv1alpha1.PolarisServerSpec{Image: "apache/polaris:test"},
	}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: server.Name, Namespace: server.Namespace}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&polarisv1alpha1.PolarisServer{}).
		WithObjects(server, deployment).Build()
	reconciler := &PolarisServerReconciler{Client: kubeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: server.Namespace, Name: server.Name}})
	if err == nil {
		t.Fatal("Reconcile() error = nil, want refusal to adopt existing Deployment")
	}
	current := &appsv1.Deployment{}
	if getErr := kubeClient.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: server.Name}, current); getErr != nil {
		t.Fatalf("get existing Deployment: %v", getErr)
	}
	if len(current.OwnerReferences) != 0 {
		t.Fatalf("existing Deployment owner references = %#v, want none", current.OwnerReferences)
	}
}
