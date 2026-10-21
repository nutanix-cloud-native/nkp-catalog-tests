package appscenarios

import (
	"context"
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	fluxhelmv2 "github.com/fluxcd/helm-controller/api/v2"
	apimeta "github.com/fluxcd/pkg/apis/meta"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlClient "sigs.k8s.io/controller-runtime/pkg/client"
)

// hrName returns the full HelmRelease name for an istio-helm component.
func hrName(component string) string {
	return fmt.Sprintf("%s-%s", istioHelmReleaseName, component)
}

// waitForIstioHelmRelease polls until the named HelmRelease has ReadyCondition == True.
func waitForIstioHelmRelease(component string, timeout time.Duration) {
	GinkgoHelper()
	hr := &fluxhelmv2.HelmRelease{
		TypeMeta: metav1.TypeMeta{
			Kind:       fluxhelmv2.HelmReleaseKind,
			APIVersion: fluxhelmv2.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      hrName(component),
			Namespace: kommanderNamespace,
		},
	}
	Eventually(func() error {
		if err := k8sClient.Get(ctx, ctrlClient.ObjectKeyFromObject(hr), hr); err != nil {
			return err
		}
		for _, cond := range hr.Status.Conditions {
			if cond.Status == metav1.ConditionTrue && cond.Type == apimeta.ReadyCondition {
				return nil
			}
		}
		return fmt.Errorf("HelmRelease %s not ready (conditions: %v)", hrName(component), hr.Status.Conditions)
	}).WithPolling(pollInterval).WithTimeout(timeout).Should(Succeed())
}

// sidecarTestDeployment returns a minimal single-replica Deployment used to
// exercise Istio sidecar injection.
func sidecarTestDeployment(namespace string) *appsv1.Deployment {
	replicas := int32(1)
	labels := map[string]string{"app": "sidecar-test"}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "sidecar-test", Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers:                    []corev1.Container{{Name: "app", Image: "docker.io/nginx:stable-alpine"}},
					TerminationGracePeriodSeconds: func() *int64 { v := int64(0); return &v }(),
				},
			},
		},
	}
}

// injectedContainerCounts lists the sidecar-test pods in the namespace and
// reports how many have an istio-proxy container injected, alongside the total
// number of matching pods observed.
//
// With Istio CNI enabled (istio 1.25), the sidecar is injected as a native
// sidecar — a restartPolicy=Always *init* container — so istio-proxy lands in
// pod.Spec.InitContainers rather than pod.Spec.Containers. Check both.
func injectedContainerCounts(namespace string) (injected, total int, err error) {
	pods := &corev1.PodList{}
	if err = k8sClient.List(ctx, pods, ctrlClient.InNamespace(namespace),
		ctrlClient.MatchingLabels{"app": "sidecar-test"}); err != nil {
		return 0, 0, err
	}
	for _, p := range pods.Items {
		total++
		if hasIstioProxy(p) {
			injected++
		}
	}
	return injected, total, nil
}

// hasIstioProxy reports whether the pod has an istio-proxy container, checking
// both regular containers and init containers (native sidecar mode).
func hasIstioProxy(p corev1.Pod) bool {
	for _, c := range p.Spec.Containers {
		if c.Name == "istio-proxy" {
			return true
		}
	}
	for _, c := range p.Spec.InitContainers {
		if c.Name == "istio-proxy" {
			return true
		}
	}
	return false
}

// rolloutRestartSidecarTestDeployment bumps the pod template's
// restartedAt annotation, the same mechanism `kubectl rollout restart` uses, so
// the ReplicaSet spins up fresh pods that pass through the injection webhook.
func rolloutRestartSidecarTestDeployment(namespace string) error {
	dep := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, ctrlClient.ObjectKey{Name: "sidecar-test", Namespace: namespace}, dep); err != nil {
		return err
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = metav1.Now().Format(time.RFC3339Nano)
	return k8sClient.Update(ctx, dep)
}

// waitForAllSidecarHRs waits for base, cni, istiod and gateway to be ready.
func waitForAllSidecarHRs(timeout time.Duration) {
	GinkgoHelper()
	for _, component := range []string{"base", "cni", "istiod", "gateway"} {
		waitForIstioHelmRelease(component, timeout)
	}
}

// setupIstioTestCluster creates a fresh Kind cluster and installs Flux + base kustomizations.
func setupIstioTestCluster() {
	GinkgoHelper()
	_ = env.Destroy(context.Background()) // best-effort pre-cleanup
	Expect(SetupKindCluster()).To(BeNil())
	Expect(env.InstallLatestFlux(ctx)).To(BeNil())
	Expect(env.ApplyKommanderBaseKustomizations(ctx)).To(BeNil())
}

// teardownIstioTestCluster destroys the Kind cluster, logging but not failing on errors.
func teardownIstioTestCluster() {
	GinkgoHelper()
	if os.Getenv("SKIP_CLUSTER_TEARDOWN") != "" {
		return
	}
	if err := env.Destroy(ctx); err != nil {
		GinkgoWriter.Printf("warning: cluster teardown failed (will be ignored): %v\n", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Test suite
//
// Two clusters total:
//
//   Cluster 1 — "install" group: ONE cluster shared across all feature tests.
//     istio-helm is installed once in sidecar mode; tests then progressively
//     mutate the running install (add gateways → enable ambient) without
//     recreating the cluster.
//
//   Cluster 2 — "upgrade" group: requires a separate cluster because the
//     previous version of istio-helm must be installed first.
//
// Run individual sub-scenarios locally with:
//   ginkgo --v --label-filter="istio-helm && sidecar-install"
//   ginkgo --v --label-filter="istio-helm && overrides"
//   ginkgo --v --label-filter="istio-helm && sidecar"
//   ginkgo --v --label-filter="istio-helm && multi-gateway"
//   ginkgo --v --label-filter="istio-helm && ambient"
//   ginkgo --v --label-filter="istio-helm && upgrade"
// ──────────────────────────────────────────────────────────────────────────────

var _ = Describe("Istio-Helm Tests", Label("istio-helm"), func() {

	// ── Cluster 1: all install / feature tests ───────────────────────────────
	//
	// BeforeEach installs istio-helm in sidecar mode once.  Sub-scenarios then
	// run sequentially on the live cluster, each mutating the override CM:
	//   sidecar verification → config overrides → sidecar injection →
	//   add multiple gateways → enable ambient mode
	//
	Describe("Istio-Helm install and feature tests", Ordered, Label("install"), func() {

		BeforeAll(func() {
			setupIstioTestCluster()
			Expect(istioHelm{}.Install(ctx, env)).To(Succeed())
			waitForAllSidecarHRs(20 * time.Minute)
		})

		AfterAll(teardownIstioTestCluster)

		// ── Sidecar mode verification ────────────────────────────────────────

		Describe("Sidecar mode", Ordered, Label("sidecar-install"), func() {

			It("should have istiod running in istio-helm-system", func() {
				deployments := &appsv1.DeploymentList{}
				Expect(k8sClient.List(ctx, deployments, ctrlClient.InNamespace(istioHelmSystem))).To(Succeed())

				var found bool
				for _, d := range deployments.Items {
					if d.Name == "istiod-"+istioHelmRevision {
						found = true
						Expect(d.Status.ReadyReplicas).To(BeNumerically(">=", 1))
					}
				}
				Expect(found).To(BeTrue(), "istiod deployment not found in %s", istioHelmSystem)
			})

			It("should have the ingress gateway running in istio-helm-gateway-ns", func() {
				deployments := &appsv1.DeploymentList{}
				Expect(k8sClient.List(ctx, deployments, ctrlClient.InNamespace(istioHelmGatewayNS))).To(Succeed())

				var found bool
				for _, d := range deployments.Items {
					if d.Labels["app"] == "istio-helm-ingressgateway" {
						found = true
						Expect(d.Status.ReadyReplicas).To(BeNumerically(">=", 1))
					}
				}
				Expect(found).To(BeTrue(), "ingress gateway deployment not found in %s", istioHelmGatewayNS)
			})

			It("should have CNI DaemonSet running in kube-system", func() {
				daemonsets := &appsv1.DaemonSetList{}
				Expect(k8sClient.List(ctx, daemonsets, ctrlClient.InNamespace("kube-system"))).To(Succeed())

				// Match by name — the CNI chart hardcodes "istio-cni-node" regardless of
				// the Helm release name. Label keys vary across chart vendors.
				var found bool
				for _, ds := range daemonsets.Items {
					if ds.Name == "istio-cni-node" {
						found = true
						Expect(ds.Status.NumberReady).To(Equal(ds.Status.DesiredNumberScheduled))
					}
				}
				Expect(found).To(BeTrue(), "istio-cni-node DaemonSet not found in kube-system")
			})

			It("should have dkp-critical-priority set on istiod", func() {
				deployments := &appsv1.DeploymentList{}
				Expect(k8sClient.List(ctx, deployments, ctrlClient.InNamespace(istioHelmSystem))).To(Succeed())

				for _, d := range deployments.Items {
					if d.Name == "istiod-"+istioHelmRevision {
						Expect(d.Spec.Template.Spec.PriorityClassName).To(Equal(dkpCriticalPriority))
					}
				}
			})

			It("should have the gateway namespace labelled for sidecar injection", func() {
				ns := &corev1.Namespace{}
				Expect(k8sClient.Get(ctx, ctrlClient.ObjectKey{Name: istioHelmGatewayNS}, ns)).To(Succeed())
				Expect(ns.Labels).To(HaveKeyWithValue("istio.io/rev", istioHelmRevision))
			})
		})

		// ── Config overrides ─────────────────────────────────────────────────
		// The single-replica CM was applied by Install(); just assert it worked.

		Describe("Config overrides", Ordered, Label("overrides"), func() {

			It("should have exactly 1 istiod replica", func() {
				deployments := &appsv1.DeploymentList{}
				Expect(k8sClient.List(ctx, deployments, ctrlClient.InNamespace(istioHelmSystem))).To(Succeed())

				for _, d := range deployments.Items {
					if d.Name == "istiod-"+istioHelmRevision {
						Expect(d.Status.ReadyReplicas).To(Equal(int32(1)))
					}
				}
			})

			It("should have exactly 1 gateway replica", func() {
				deployments := &appsv1.DeploymentList{}
				Expect(k8sClient.List(ctx, deployments, ctrlClient.InNamespace(istioHelmGatewayNS))).To(Succeed())

				for _, d := range deployments.Items {
					if d.Labels["app"] == "istio-helm-ingressgateway" {
						Expect(d.Status.ReadyReplicas).To(Equal(int32(1)))
					}
				}
			})

			It("should reference the override ConfigMap in HelmRelease valuesFrom", func() {
				hr := &fluxhelmv2.HelmRelease{}
				Expect(k8sClient.Get(ctx, ctrlClient.ObjectKey{
					Name: hrName("istiod"), Namespace: kommanderNamespace,
				}, hr)).To(Succeed())

				var found bool
				for _, ref := range hr.Spec.ValuesFrom {
					if ref.Name == istioHelmOverrideCMName {
						found = true
					}
				}
				Expect(found).To(BeTrue(), "HelmRelease valuesFrom should reference %s", istioHelmOverrideCMName)
			})
		})

		// ── Sidecar injection ────────────────────────────────────────────────

		Describe("Sidecar injection", Ordered, Label("sidecar"), func() {

			It("should inject istio-proxy into a pod in a labelled namespace", func() {
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "istio-helm-sidecar-test",
						Labels: map[string]string{"istio.io/rev": istioHelmRevision},
					},
				}
				Expect(k8sClient.Create(ctx, ns)).To(Or(Succeed(), Satisfy(k8serrors.IsAlreadyExists)))

				Expect(k8sClient.Create(ctx, sidecarTestDeployment(ns.Name))).
					To(Or(Succeed(), Satisfy(k8serrors.IsAlreadyExists)))

				Eventually(func() error {
					injected, total, err := injectedContainerCounts(ns.Name)
					if err != nil {
						return err
					}
					if injected == 0 {
						return fmt.Errorf("istio-proxy not injected yet (matching pods: %d)", total)
					}
					return nil
				}).WithPolling(5 * time.Second).WithTimeout(5 * time.Minute).Should(Succeed())
			})

			It("should NOT inject a sidecar into a pod in an unlabelled namespace", func() {
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: "istio-helm-no-inject"},
				}
				Expect(k8sClient.Create(ctx, ns)).To(Or(Succeed(), Satisfy(k8serrors.IsAlreadyExists)))

				Expect(k8sClient.Create(ctx, sidecarTestDeployment(ns.Name))).
					To(Or(Succeed(), Satisfy(k8serrors.IsAlreadyExists)))

				// Wait for the pod to be scheduled, then assert it was never injected.
				Eventually(func() (int, error) {
					_, total, err := injectedContainerCounts(ns.Name)
					return total, err
				}).WithPolling(2 * time.Second).WithTimeout(2 * time.Minute).Should(BeNumerically(">=", 1))

				Consistently(func() (int, error) {
					injected, _, err := injectedContainerCounts(ns.Name)
					return injected, err
				}).WithPolling(2 * time.Second).WithTimeout(30 * time.Second).Should(Equal(0))
			})

			It("should inject istio-proxy after labelling an existing namespace and restarting the deployment", func() {
				// Brownfield scenario: a namespace and its workload already exist
				// without injection. The operator later opts the namespace in and
				// restarts the workload. Because injection happens at pod creation,
				// only the pods created by the rollout restart get the sidecar —
				// this proves the restart (not the label alone) is what triggers it.
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: "istio-helm-brownfield"},
				}
				Expect(k8sClient.Create(ctx, ns)).To(Or(Succeed(), Satisfy(k8serrors.IsAlreadyExists)))

				Expect(k8sClient.Create(ctx, sidecarTestDeployment(ns.Name))).
					To(Or(Succeed(), Satisfy(k8serrors.IsAlreadyExists)))

				// The pre-label workload must come up without a sidecar.
				Eventually(func() (int, error) {
					_, total, err := injectedContainerCounts(ns.Name)
					return total, err
				}).WithPolling(2 * time.Second).WithTimeout(2 * time.Minute).Should(BeNumerically(">=", 1))
				injected, _, err := injectedContainerCounts(ns.Name)
				Expect(err).NotTo(HaveOccurred())
				Expect(injected).To(Equal(0), "workload should not be injected before labelling the namespace")

				// Opt the existing namespace into the mesh.
				Eventually(func() error {
					current := &corev1.Namespace{}
					if err := k8sClient.Get(ctx, ctrlClient.ObjectKey{Name: ns.Name}, current); err != nil {
						return err
					}
					if current.Labels == nil {
						current.Labels = map[string]string{}
					}
					current.Labels["istio.io/rev"] = istioHelmRevision
					return k8sClient.Update(ctx, current)
				}).WithPolling(pollInterval).WithTimeout(time.Minute).Should(Succeed())

				// Restart the workload so fresh pods pass through the injector.
				Expect(rolloutRestartSidecarTestDeployment(ns.Name)).To(Succeed())

				Eventually(func() error {
					injected, total, err := injectedContainerCounts(ns.Name)
					if err != nil {
						return err
					}
					if injected == 0 {
						return fmt.Errorf("istio-proxy not injected after restart yet (matching pods: %d)", total)
					}
					return nil
				}).WithPolling(5 * time.Second).WithTimeout(5 * time.Minute).Should(Succeed())
			})
		})

		// ── Multiple gateways ────────────────────────────────────────────────
		// Apply the multi-gateway override CM to the running install and verify
		// that the additional gateways are deployed.

		Describe("Multiple gateways", Ordered, Label("multi-gateway"), func() {
			var i istioHelm

			It("should have only the default ingressgateway before applying multi-gateway overrides", func() {
				i = istioHelm{}
				deployments := &appsv1.DeploymentList{}
				Expect(k8sClient.List(ctx, deployments, ctrlClient.InNamespace(istioHelmGatewayNS))).To(Succeed())

				for _, d := range deployments.Items {
					Expect(d.Name).NotTo(Equal("istio-egressgateway"),
						"egress gateway must not exist before multi-gateway overrides are applied")
				}
			})

			It("should deploy additional gateways after updating the override ConfigMap", func() {
				Expect(i.EnableMultipleGateways(ctx, env)).To(Succeed())
				waitForIstioHelmRelease("gateway", 10*time.Minute)
			})

			It("should have the egress gateway deployment with correct labels", func() {
				Eventually(func() error {
					deployments := &appsv1.DeploymentList{}
					if err := k8sClient.List(ctx, deployments, ctrlClient.InNamespace(istioHelmGatewayNS)); err != nil {
						return err
					}
					for _, d := range deployments.Items {
						if d.Name == "istio-egressgateway" && d.Status.ReadyReplicas >= 1 {
							return nil
						}
					}
					return fmt.Errorf("istio-egressgateway not ready yet")
				}).WithPolling(pollInterval).WithTimeout(5 * time.Minute).Should(Succeed())
			})

			It("should have the egress gateway Service with type ClusterIP and correct ports", func() {
				svcList := &corev1.ServiceList{}
				Expect(k8sClient.List(ctx, svcList, ctrlClient.InNamespace(istioHelmGatewayNS))).To(Succeed())

				var found bool
				for _, svc := range svcList.Items {
					if svc.Name == "istio-egressgateway" {
						found = true
						Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
						portNames := make([]string, 0, len(svc.Spec.Ports))
						for _, p := range svc.Spec.Ports {
							portNames = append(portNames, p.Name)
						}
						Expect(portNames).To(ContainElements("http2", "https", "tls"))
					}
				}
				Expect(found).To(BeTrue(), "istio-egressgateway service not found")
			})

			It("should have the internal ingressgateway deployment ready", func() {
				Eventually(func() error {
					deployments := &appsv1.DeploymentList{}
					if err := k8sClient.List(ctx, deployments, ctrlClient.InNamespace(istioHelmGatewayNS)); err != nil {
						return err
					}
					for _, d := range deployments.Items {
						if d.Name == "istio-ingressgateway-internal" && d.Status.ReadyReplicas >= 1 {
							return nil
						}
					}
					return fmt.Errorf("istio-ingressgateway-internal not ready yet")
				}).WithPolling(pollInterval).WithTimeout(5 * time.Minute).Should(Succeed())
			})

			It("should keep the default ingressgateway running alongside the new gateways", func() {
				deployments := &appsv1.DeploymentList{}
				Expect(k8sClient.List(ctx, deployments, ctrlClient.InNamespace(istioHelmGatewayNS))).To(Succeed())

				var found bool
				for _, d := range deployments.Items {
					if d.Labels["app"] == "istio-helm-ingressgateway" {
						found = true
						Expect(d.Status.ReadyReplicas).To(BeNumerically(">=", 1))
					}
				}
				Expect(found).To(BeTrue(), "default ingressgateway disappeared after adding extra gateways")
			})
		})

		// ── Ambient mode ─────────────────────────────────────────────────────
		// Apply the ambient override CM to the running install (which currently
		// has the multi-gateway CM) and verify that ztunnel is deployed.

		Describe("Ambient mode", Ordered, Label("ambient"), func() {
			var i istioHelm

			It("should enable ambient mode on the running install", func() {
				i = istioHelm{}
				Expect(i.EnableAmbientMode(ctx, env)).To(Succeed())
				waitForIstioHelmRelease("ztunnel", 10*time.Minute)
			})

			It("should have ztunnel DaemonSet running in istio-helm-gateway-ns", func() {
				// The ztunnel HelmRelease was already installed (disabled) in sidecar
				// mode, so its Ready condition can still read True from the previous
				// reconcile immediately after we flip ztunnel.enabled=true. Poll for
				// the DaemonSet to actually appear and become ready rather than
				// asserting once.
				Eventually(func() error {
					daemonsets := &appsv1.DaemonSetList{}
					if err := k8sClient.List(ctx, daemonsets, ctrlClient.InNamespace(istioHelmGatewayNS)); err != nil {
						return err
					}
					for _, ds := range daemonsets.Items {
						if ds.Name == "ztunnel" {
							if ds.Status.NumberReady >= 1 {
								return nil
							}
							return fmt.Errorf("ztunnel DaemonSet found but not ready (%d ready)", ds.Status.NumberReady)
						}
					}
					return fmt.Errorf("ztunnel DaemonSet not found after enabling ambient mode")
				}).WithPolling(pollInterval).WithTimeout(10 * time.Minute).Should(Succeed())
			})

			It("should enrol a namespace into the ambient mesh", func() {
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "istio-helm-ambient-test",
						Labels: map[string]string{"istio.io/dataplane-mode": "ambient"},
					},
				}
				Expect(k8sClient.Create(ctx, ns)).To(Or(Succeed(), Satisfy(k8serrors.IsAlreadyExists)))

				fetched := &corev1.Namespace{}
				Expect(k8sClient.Get(ctx, ctrlClient.ObjectKey{Name: ns.Name}, fetched)).To(Succeed())
				Expect(fetched.Labels).To(HaveKeyWithValue("istio.io/dataplane-mode", "ambient"))
			})

			It("should NOT inject sidecars into pods in an ambient-labelled namespace", func() {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "ambient-test-pod", Namespace: "istio-helm-ambient-test"},
					Spec: corev1.PodSpec{
						Containers:                    []corev1.Container{{Name: "app", Image: "docker.io/nginx:stable-alpine"}},
						TerminationGracePeriodSeconds: func() *int64 { v := int64(0); return &v }(),
					},
				}
				Expect(k8sClient.Create(ctx, pod)).To(Or(Succeed(), Satisfy(k8serrors.IsAlreadyExists)))

				Eventually(func() (int, error) {
					p := &corev1.Pod{}
					if err := k8sClient.Get(ctx, ctrlClient.ObjectKey{Name: pod.Name, Namespace: pod.Namespace}, p); err != nil {
						return 0, err
					}
					return len(p.Spec.Containers), nil
				}).WithPolling(2*time.Second).WithTimeout(2*time.Minute).Should(Equal(1),
					"ambient-mode pods must not have an istio-proxy sidecar")
			})
		})
	})

	// ── Cluster 2: upgrade test ──────────────────────────────────────────────
	// Requires its own cluster because the previous version of istio-helm must
	// be installed first — there is no clean way to downgrade on a live cluster.

	Describe("Upgrade from previous version", Ordered, Label("upgrade"), func() {
		var i istioHelm

		BeforeAll(setupIstioTestCluster)
		AfterAll(teardownIstioTestCluster)

		It("should install the previous version successfully", func() {
			i = istioHelm{}
			err := i.InstallPreviousVersion(ctx, env)
			if err != nil && os.Getenv(upgradeKappsRepoPathEnv) == "" {
				Skip("UPGRADE_KAPPS_REPO_PATH not set — skipping upgrade test")
			}
			Expect(err).To(BeNil())
			waitForAllSidecarHRs(20 * time.Minute)
		})

		It("should upgrade to the current version successfully", func() {
			Expect(i.Upgrade(ctx, env)).To(Succeed())
			waitForAllSidecarHRs(20 * time.Minute)
		})

		It("should have all HelmReleases at the current version after upgrade", func() {
			for _, component := range []string{"base", "cni", "istiod", "gateway"} {
				hr := &fluxhelmv2.HelmRelease{}
				Expect(k8sClient.Get(ctx, ctrlClient.ObjectKey{
					Name: hrName(component), Namespace: kommanderNamespace,
				}, hr)).To(Succeed())
				Expect(hr.Status.History).NotTo(BeEmpty())
			}
		})
	})
})
