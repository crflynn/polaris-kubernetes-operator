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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	polarisv1alpha1 "github.com/apache/polaris-kubernetes-operator/api/v1alpha1"
)

const polarisServerReadyCondition = "Ready"

const polarisServerCatalogPortName = "http"

// PolarisServerReconciler reconciles the native Kubernetes workload for a PolarisServer.
type PolarisServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=polaris.apache.org,resources=polarisservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;create;update;patch;delete

// Reconcile creates and updates only the Deployment and Service owned by this resource.
func (r *PolarisServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	server := &polarisv1alpha1.PolarisServer{}
	if err := r.Get(ctx, req.NamespacedName, server); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.reconcileDeployment(ctx, server); err != nil {
		return ctrl.Result{}, r.setPolarisServerFailure(ctx, server, err)
	}
	if err := r.reconcileService(ctx, server); err != nil {
		return ctrl.Result{}, r.setPolarisServerFailure(ctx, server, err)
	}
	if err := r.reconcileManagementService(ctx, server); err != nil {
		return ctrl.Result{}, r.setPolarisServerFailure(ctx, server, err)
	}
	if err := r.reconcileIngress(ctx, server); err != nil {
		return ctrl.Result{}, r.setPolarisServerFailure(ctx, server, err)
	}
	if err := r.reconcileHTTPRoute(ctx, server); err != nil {
		return ctrl.Result{}, r.setPolarisServerFailure(ctx, server, err)
	}

	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: server.Name}, deployment); err != nil {
		return ctrl.Result{}, err
	}
	server.Status.ObservedGeneration = server.Generation
	server.Status.ReadyReplicas = deployment.Status.ReadyReplicas
	server.Status.ServiceName = server.Name
	server.Status.ManagementServiceName = polarisServerManagementServiceName(server)
	ready := deployment.Status.ReadyReplicas >= desiredPolarisReplicas(server)
	status := metav1.ConditionFalse
	reason := "WorkloadNotReady"
	message := fmt.Sprintf("%d of %d Polaris server replicas are ready", deployment.Status.ReadyReplicas, desiredPolarisReplicas(server))
	if ready {
		status = metav1.ConditionTrue
		reason = "WorkloadReady"
		message = "Polaris server Deployment has the desired number of ready replicas"
	}
	meta.SetStatusCondition(&server.Status.Conditions, metav1.Condition{
		Type:               polarisServerReadyCondition,
		Status:             status,
		ObservedGeneration: server.Generation,
		Reason:             reason,
		Message:            message,
	})
	if err := r.Status().Update(ctx, server); err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	if server.Spec.HTTPRoute != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}
	return ctrl.Result{}, nil
}

func (r *PolarisServerReconciler) reconcileDeployment(ctx context.Context, server *polarisv1alpha1.PolarisServer) error {
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: server.Name, Namespace: server.Namespace}}
	if err := r.ensurePolarisServerObjectAvailable(ctx, server, deployment); err != nil {
		return fmt.Errorf("check Polaris server Deployment ownership: %w", err)
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		if err := r.setPolarisServerControllerReference(server, deployment); err != nil {
			return err
		}
		labels := polarisServerLabels(server)
		replicas := desiredPolarisReplicas(server)
		port := desiredPolarisPort(server)
		managementPort := desiredPolarisManagementPort(server)
		deployment.Labels = labels
		deployment.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: server.Spec.PodAnnotations},
				Spec: corev1.PodSpec{
					ServiceAccountName:            server.Spec.ServiceAccountName,
					ImagePullSecrets:              server.Spec.ImagePullSecrets,
					SecurityContext:               server.Spec.PodSecurityContext,
					NodeSelector:                  server.Spec.NodeSelector,
					Affinity:                      server.Spec.Affinity,
					Tolerations:                   server.Spec.Tolerations,
					TopologySpreadConstraints:     server.Spec.TopologySpreadConstraints,
					PriorityClassName:             server.Spec.PriorityClassName,
					TerminationGracePeriodSeconds: server.Spec.TerminationGracePeriodSeconds,
					Volumes:                       server.Spec.Volumes,
					InitContainers:                server.Spec.InitContainers,
					Containers: []corev1.Container{{
						Name:            "polaris",
						Image:           server.Spec.Image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						SecurityContext: server.Spec.ContainerSecurityContext,
						Ports:           []corev1.ContainerPort{{Name: polarisServerCatalogPortName, ContainerPort: port, Protocol: corev1.ProtocolTCP}, {Name: "management", ContainerPort: managementPort, Protocol: corev1.ProtocolTCP}},
						Env:             server.Spec.Env,
						EnvFrom:         server.Spec.EnvFrom,
						Resources:       server.Spec.Resources,
						VolumeMounts:    server.Spec.VolumeMounts,
						StartupProbe:    httpProbe("/q/health/started", managementPort, 30),
						ReadinessProbe:  httpProbe("/q/health/ready", managementPort, 3),
						LivenessProbe:   httpProbe("/q/health/live", managementPort, 3),
					}},
				},
			},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconcile Polaris server Deployment: %w", err)
	}
	return nil
}

func (r *PolarisServerReconciler) reconcileService(ctx context.Context, server *polarisv1alpha1.PolarisServer) error {
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: server.Name, Namespace: server.Namespace}}
	if err := r.ensurePolarisServerObjectAvailable(ctx, server, service); err != nil {
		return fmt.Errorf("check Polaris server Service ownership: %w", err)
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		if err := r.setPolarisServerControllerReference(server, service); err != nil {
			return err
		}
		labels := polarisServerLabels(server)
		port := desiredPolarisPort(server)
		service.Labels = labels
		service.Annotations = server.Spec.Service.Annotations
		service.Spec.Type = desiredPolarisServiceType(server)
		service.Spec.Selector = labels
		service.Spec.Ports = []corev1.ServicePort{
			{Name: polarisServerCatalogPortName, Port: port, TargetPort: intstr.FromInt32(port), Protocol: corev1.ProtocolTCP},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconcile Polaris server Service: %w", err)
	}
	return nil
}

func (r *PolarisServerReconciler) reconcileManagementService(ctx context.Context, server *polarisv1alpha1.PolarisServer) error {
	name := polarisServerManagementServiceName(server)
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: server.Namespace}}
	if err := r.ensurePolarisServerObjectAvailable(ctx, server, service); err != nil {
		return fmt.Errorf("check Polaris server Management Service ownership: %w", err)
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		if err := r.setPolarisServerControllerReference(server, service); err != nil {
			return err
		}
		labels := polarisServerLabels(server)
		managementPort := desiredPolarisManagementPort(server)
		service.Labels = labels
		service.Spec.Type = corev1.ServiceTypeClusterIP
		service.Spec.Selector = labels
		service.Spec.Ports = []corev1.ServicePort{{
			Name: "management", Port: managementPort,
			TargetPort: intstr.FromInt32(managementPort), Protocol: corev1.ProtocolTCP,
		}}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconcile Polaris server Management Service: %w", err)
	}
	return nil
}

func (r *PolarisServerReconciler) reconcileIngress(ctx context.Context, server *polarisv1alpha1.PolarisServer) error {
	ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: server.Name, Namespace: server.Namespace}}
	if server.Spec.Ingress == nil {
		if err := r.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: server.Name}, ingress); apierrors.IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}
		if owner := metav1.GetControllerOf(ingress); owner != nil && owner.UID == server.UID {
			if err := r.Delete(ctx, ingress); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete Polaris server Ingress: %w", err)
			}
		}
		return nil
	}
	if err := r.ensurePolarisServerObjectAvailable(ctx, server, ingress); err != nil {
		return fmt.Errorf("check Polaris server Ingress ownership: %w", err)
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ingress, func() error {
		if err := r.setPolarisServerControllerReference(server, ingress); err != nil {
			return err
		}
		config := server.Spec.Ingress
		path := config.Path
		if path == "" {
			path = "/"
		}
		pathType := networkingv1.PathTypePrefix
		ingress.Labels = polarisServerLabels(server)
		ingress.Annotations = config.Annotations
		ingress.Spec = networkingv1.IngressSpec{
			IngressClassName: config.IngressClassName,
			Rules: []networkingv1.IngressRule{{
				Host: config.Host,
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{
						Path:     path,
						PathType: &pathType,
						Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
							Name: server.Name,
							Port: networkingv1.ServiceBackendPort{Name: polarisServerCatalogPortName},
						}},
					}},
				}},
			}},
		}
		if config.TLSSecretName != "" {
			ingress.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{config.Host}, SecretName: config.TLSSecretName}}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconcile Polaris server Ingress: %w", err)
	}
	return nil
}

func (r *PolarisServerReconciler) reconcileHTTPRoute(ctx context.Context, server *polarisv1alpha1.PolarisServer) error {
	route := &gatewayv1.HTTPRoute{}
	if server.Spec.HTTPRoute == nil {
		if server.Status.HTTPRouteName == "" {
			return nil
		}
		if err := r.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: server.Status.HTTPRouteName}, route); apierrors.IsNotFound(err) {
			server.Status.HTTPRouteName = ""
			return nil
		} else if err != nil {
			return err
		}
		if owner := metav1.GetControllerOf(route); owner != nil && owner.UID == server.UID {
			if err := r.Delete(ctx, route); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete Polaris server HTTPRoute: %w", err)
			}
		}
		server.Status.HTTPRouteName = ""
		return nil
	}

	route.Name = server.Name
	route.Namespace = server.Namespace
	if err := r.ensurePolarisServerObjectAvailable(ctx, server, route); err != nil {
		return fmt.Errorf("check Polaris server HTTPRoute ownership: %w", err)
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, route, func() error {
		if err := r.setPolarisServerControllerReference(server, route); err != nil {
			return err
		}
		config := server.Spec.HTTPRoute
		path := config.Path
		if path == "" {
			path = "/"
		}
		pathMatchType := gatewayv1.PathMatchPathPrefix
		backendPort := desiredPolarisPort(server)
		route.Labels = polarisServerLabels(server)
		route.Annotations = config.Annotations
		route.Spec = gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: config.ParentRefs},
			Hostnames:       config.Hostnames,
			Rules: []gatewayv1.HTTPRouteRule{{
				Matches: []gatewayv1.HTTPRouteMatch{{
					Path: &gatewayv1.HTTPPathMatch{Type: &pathMatchType, Value: &path},
				}},
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: gatewayv1.ObjectName(server.Name),
						Port: &backendPort,
					}},
				}},
			}},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconcile Polaris server HTTPRoute: %w", err)
	}
	server.Status.HTTPRouteName = server.Name
	return nil
}

func (r *PolarisServerReconciler) ensurePolarisServerObjectAvailable(ctx context.Context, server *polarisv1alpha1.PolarisServer, object client.Object) error {
	err := r.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: object.GetName()}, object)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	controller := metav1.GetControllerOf(object)
	if controller == nil {
		return fmt.Errorf("%s/%s already exists without a PolarisServer owner", object.GetNamespace(), object.GetName())
	}
	if controller.UID != server.UID {
		return fmt.Errorf("%s/%s is already controlled by %s %s", object.GetNamespace(), object.GetName(), controller.Kind, controller.Name)
	}
	return nil
}

func (r *PolarisServerReconciler) setPolarisServerControllerReference(server *polarisv1alpha1.PolarisServer, object client.Object) error {
	return controllerutil.SetControllerReference(server, object, r.Scheme)
}

func (r *PolarisServerReconciler) setPolarisServerFailure(ctx context.Context, server *polarisv1alpha1.PolarisServer, err error) error {
	logger := logf.FromContext(ctx)
	logger.Error(err, "Failed to reconcile Polaris server", "name", server.Name)
	meta.SetStatusCondition(&server.Status.Conditions, metav1.Condition{
		Type:               polarisServerReadyCondition,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: server.Generation,
		Reason:             "ReconcileFailed",
		Message:            err.Error(),
	})
	if statusErr := r.Status().Update(ctx, server); statusErr != nil && !apierrors.IsNotFound(statusErr) {
		return fmt.Errorf("reconcile Polaris server: %w; update status: %v", err, statusErr)
	}
	return err
}

func desiredPolarisReplicas(server *polarisv1alpha1.PolarisServer) int32 {
	if server.Spec.Replicas == nil {
		return 1
	}
	return *server.Spec.Replicas
}

func desiredPolarisPort(server *polarisv1alpha1.PolarisServer) int32 {
	if server.Spec.Port == nil {
		return 8181
	}
	return *server.Spec.Port
}

func desiredPolarisServiceType(server *polarisv1alpha1.PolarisServer) corev1.ServiceType {
	if server.Spec.Service.Type == "" {
		return corev1.ServiceTypeClusterIP
	}
	return server.Spec.Service.Type
}
func polarisServerLabels(server *polarisv1alpha1.PolarisServer) map[string]string {
	name := server.Name
	if len(name) > 63 {
		hash := sha256.Sum256([]byte(name))
		name = name[:54] + "-" + hex.EncodeToString(hash[:4])
	}
	return map[string]string{"app.kubernetes.io/name": "polaris", "app.kubernetes.io/instance": name}
}

func polarisServerManagementServiceName(server *polarisv1alpha1.PolarisServer) string {
	hash := sha256.Sum256([]byte(server.Namespace + "/" + server.Name))
	return "polaris-management-" + hex.EncodeToString(hash[:])
}

func desiredPolarisManagementPort(server *polarisv1alpha1.PolarisServer) int32 {
	if server.Spec.ManagementPort == nil {
		return 8182
	}
	return *server.Spec.ManagementPort
}

func httpProbe(path string, port int32, failureThreshold int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:     corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromInt32(port), Scheme: corev1.URISchemeHTTP}},
		PeriodSeconds:    10,
		TimeoutSeconds:   5,
		FailureThreshold: failureThreshold,
	}
}

// SetupWithManager sets up the PolarisServer controller and watches owned workload resources.
func (r *PolarisServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&polarisv1alpha1.PolarisServer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		Named("polarisserver").
		Complete(r)
}
