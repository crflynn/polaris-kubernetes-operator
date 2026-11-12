//go:build e2e
// +build e2e

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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/apache/polaris-kubernetes-operator/test/utils"
)

// namespace where the project is deployed in.
const namespace = "polaris-kubernetes-operator-system"

// examplesNamespace contains the checked-in example manifests and Helm smoke release.
const examplesNamespace = "polaris-system"

const helmSmokeRelease = "polaris-helm-smoke"

const helmSmokeImageRepository = "example.com/polaris-kubernetes-operator"

const helmSmokeImageTag = "v0.0.1"

// serviceAccountName created for the project
const serviceAccountName = "polaris-kubernetes-operator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "polaris-kubernetes-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "polaris-kubernetes-operator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating namespaces for the controller and examples")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create manager namespace")
		cmd = exec.Command("kubectl", "create", "ns", examplesNamespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create examples namespace")

		By("labeling the manager namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing the published Helm chart into Kind")
		err = installHelmChartSmoke()
		Expect(err).NotTo(HaveOccurred(), "Failed to install and verify Helm chart")

		By("validating checked-in examples against installed CRD schemas")
		cmd = exec.Command("mise", "exec", "-C", ".", "--", "just", "validate-examples")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to validate checked-in examples")

		By("starting the in-cluster Polaris mock")
		cmd = exec.Command("kubectl", "apply", "-f", "test/e2e/testdata/polaris-mock.yaml")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create Polaris mock")
		cmd = exec.Command("kubectl", "rollout", "status", "deployment/polaris-mock",
			"-n", "polaris-mock", "--timeout=2m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Polaris mock did not become ready")

		By("installing CRDs for the Kustomize deployment")
		cmd = exec.Command("mise", "exec", "-C", ".", "--", "just", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("mise", "exec", "-C", ".", "--", "just", "--set", "img", managerImage, "deploy")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("configuring the in-cluster Polaris endpoint allowlist")
		cmd = exec.Command("kubectl", "set", "env", "deployment/controller-manager", "-n", namespace,
			"POLARIS_ALLOWED_HOSTS=polaris-mock.polaris-mock.svc.cluster.local:8080",
			"POLARIS_ALLOW_INSECURE_HTTP=true")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to configure the Polaris endpoint allowlist")
		cmd = exec.Command("kubectl", "rollout", "status", "deployment/controller-manager", "-n", namespace, "--timeout=2m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Controller manager did not restart with endpoint policy")
	})

	// After all tests have executed, clean up the test resources and cluster objects.
	AfterAll(func() {
		By("deleting the reconciliation test resources")
		cmd := exec.Command("kubectl", "delete", "polariscatalog", "polaris-e2e-warehouse",
			"-n", namespace, "--ignore-not-found=true", "--wait=true", "--timeout=2m")
		_, _ = utils.Run(cmd)
		cmd = exec.Command("kubectl", "delete", "polarisconnection/polaris-e2e",
			"secret/polaris-e2e-auth", "-n", namespace, "--ignore-not-found=true", "--wait=true")
		_, _ = utils.Run(cmd)

		By("cleaning up the curl pod for metrics")
		cmd = exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)

		By("removing the metrics reader ClusterRoleBinding")
		cmd = exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName,
			"--ignore-not-found=true")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("mise", "exec", "-C", ".", "--", "just", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("mise", "exec", "-C", ".", "--", "just", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing test namespaces")
		cmd = exec.Command("kubectl", "delete", "ns", namespace, examplesNamespace, "polaris-mock",
			"--ignore-not-found=true", "--wait=true", "--timeout=2m")
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				By("getting the name of the controller-manager pod")
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				By("validating the pod's status")
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=polaris-kubernetes-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		It("reconciles a Polaris connection and catalog against the in-cluster mock", func() {
			By("applying the mock Polaris credentials and custom resources")
			cmd := exec.Command("kubectl", "apply", "-f", "test/e2e/testdata/polaris-resources.yaml")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply reconciliation test resources")

			By("waiting for the Polaris connection check to succeed")
			cmd = exec.Command("kubectl", "wait", "--for=condition=Ready",
				"polarisconnection/polaris-e2e", "-n", namespace, "--timeout=2m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "PolarisConnection did not become Ready")

			By("checking the connection's discovered catalog capability")
			cmd = exec.Command("kubectl", "get", "polarisconnection", "polaris-e2e", "-n", namespace,
				"-o", "jsonpath={.status.capabilities[0]}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("GET /v1/{prefix}/namespaces"))

			By("waiting for the Polaris catalog to reconcile")
			cmd = exec.Command("kubectl", "wait", "--for=condition=Ready",
				"polariscatalog/polaris-e2e-warehouse", "-n", namespace, "--timeout=2m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "PolarisCatalog did not become Ready")

			By("checking the catalog version returned by Polaris")
			cmd = exec.Command("kubectl", "get", "polariscatalog", "polaris-e2e-warehouse", "-n", namespace,
				"-o", "jsonpath={.status.entityVersion}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("23"))

			By("deleting the catalog and waiting for its remote-delete finalizer")
			cmd = exec.Command("kubectl", "delete", "polariscatalog", "polaris-e2e-warehouse",
				"-n", namespace, "--wait=true", "--timeout=2m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "PolarisCatalog finalizer did not complete")
			cmd = exec.Command("kubectl", "delete", "polarisconnection/polaris-e2e",
				"secret/polaris-e2e-auth", "-n", namespace, "--wait=true")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to remove reconciliation test resources")
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})
})

// installHelmChartSmoke installs the chart with the test image and removes it after Helm waits for readiness.
func installHelmChartSmoke() error {
	cmd := exec.Command("mise", "exec", "-C", ".", "--", "helm", "install", helmSmokeRelease,
		"charts/polaris-kubernetes-operator",
		"--namespace", examplesNamespace,
		"--set", fmt.Sprintf("image.repository=%s,image.tag=%s", helmSmokeImageRepository, helmSmokeImageTag),
		"--wait", "--timeout=3m")
	if _, err := utils.Run(cmd); err != nil {
		cleanupHelmChartSmoke()
		return err
	}

	cmd = exec.Command("kubectl", "wait", "--for=condition=Established", "--timeout=1m",
		"crd/polarisconnections.polaris.apache.org")
	if _, err := utils.Run(cmd); err != nil {
		cleanupHelmChartSmoke()
		return err
	}

	return cleanupHelmChartSmoke()
}

func cleanupHelmChartSmoke() error {
	cmd := exec.Command("mise", "exec", "-C", ".", "--", "helm", "uninstall",
		helmSmokeRelease, "--namespace", examplesNamespace, "--ignore-not-found")
	_, err := utils.Run(cmd)
	return err
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	By("creating temporary file to store the token request")
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		By("executing kubectl command to create the token")
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		By("parsing the JSON output to extract the token")
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
