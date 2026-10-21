package appscenarios

import (
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	fluxhelmv2 "github.com/fluxcd/helm-controller/api/v2"
	apimeta "github.com/fluxcd/pkg/apis/meta"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlClient "sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Headlamp Tests", Label("headlamp"), func() {
	var (
		h *headlamp
	)

	BeforeEach(OncePerOrdered, func() {
		err := SetupKindCluster()
		Expect(err).To(BeNil())

		err = env.InstallLatestFlux(ctx)
		Expect(err).To(BeNil())

		err = env.ApplyKommanderBaseKustomizations(ctx)
		Expect(err).To(BeNil())

		h = NewHeadlamp()
	})

	AfterEach(OncePerOrdered, func() {
		if os.Getenv("SKIP_CLUSTER_TEARDOWN") != "" {
			return
		}

		err := env.Destroy(ctx)
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("Headlamp Install Test", Ordered, Label("install"), func() {
		var (
			headlampHr         *fluxhelmv2.HelmRelease
			deploymentList     *appsv1.DeploymentList
			headlampDeployment appsv1.Deployment
			headlampContainer  corev1.Container
		)

		It("should install successfully with default config", func() {
			err := h.Install(ctx, env)
			Expect(err).To(BeNil())

			headlampHr = &fluxhelmv2.HelmRelease{
				TypeMeta: metav1.TypeMeta{
					Kind:       fluxhelmv2.HelmReleaseKind,
					APIVersion: fluxhelmv2.GroupVersion.Version,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      h.Name(),
					Namespace: kommanderNamespace,
				},
			}

			Eventually(func() error {
				err := k8sClient.Get(ctx, ctrlClient.ObjectKeyFromObject(headlampHr), headlampHr)
				if err != nil {
					return err
				}

				for _, cond := range headlampHr.Status.Conditions {
					if cond.Status == metav1.ConditionTrue &&
						cond.Type == apimeta.ReadyCondition {
						return nil
					}
				}
				return fmt.Errorf("helm release not ready yet")
			}).WithPolling(pollInterval).WithTimeout(5 * time.Minute).Should(Succeed())
		})

		Context("Headlamp Deployment", func() {
			It("should have a ready deployment with correct configuration", func() {
				selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
					MatchLabels: map[string]string{
						"helm.toolkit.fluxcd.io/name": h.Name(),
					},
				})
				Expect(err).To(BeNil())

				deploymentList = &appsv1.DeploymentList{}
				err = k8sClient.List(ctx, deploymentList, &ctrlClient.ListOptions{
					LabelSelector: selector,
				})
				Expect(err).To(BeNil())
				Expect(deploymentList.Items).To(HaveLen(1))

				headlampDeployment = deploymentList.Items[0]
				Expect(headlampDeployment.Status.ReadyReplicas).To(BeNumerically(">=", 1))
			})

			It("should have the correct priority class", func() {
				Expect(headlampDeployment.Spec.Template.Spec.PriorityClassName).To(Equal(dkpHighPriority))
			})

			It("should have resource limits set", func() {
				headlampContainer = headlampDeployment.Spec.Template.Spec.Containers[0]
				Expect(headlampContainer.Resources.Requests).ToNot(BeEmpty())
				Expect(headlampContainer.Resources.Limits).ToNot(BeEmpty())
			})

			It("should mount the kubeconfig volume", func() {
				found := false
				for _, vm := range headlampContainer.VolumeMounts {
					if vm.Name == "kubeconfig" && vm.MountPath == "/etc/headlamp/kubeconfig" {
						found = true
					}
				}
				Expect(found).To(BeTrue())
			})
		})

		Context("Headlamp ConfigMaps", func() {
			It("should have the kubeconfig configmap", func() {
				kubeconfigCM := &corev1.ConfigMap{}
				err := k8sClient.Get(ctx, ctrlClient.ObjectKey{
					Namespace: kommanderNamespace,
					Name:      "headlamp-kubeconfig",
				}, kubeconfigCM)
				Expect(err).To(BeNil())
				Expect(kubeconfigCM.Data).To(HaveKey("kubeconfig"))
				Expect(kubeconfigCM.Data["kubeconfig"]).To(ContainSubstring("https://kubernetes.default.svc"))
			})
		})

		Context("Headlamp RBAC", func() {
			It("should have the impersonation ClusterRole", func() {
				cr := &rbacv1.ClusterRole{}
				err := k8sClient.Get(ctx, ctrlClient.ObjectKey{
					Name: "headlamp-impersonation-minimal",
				}, cr)
				Expect(err).To(BeNil())

				hasImpersonate := false
				for _, rule := range cr.Rules {
					for _, verb := range rule.Verbs {
						if verb == "impersonate" {
							hasImpersonate = true
						}
					}
				}
				Expect(hasImpersonate).To(BeTrue())
			})
		})

		Context("Headlamp Ingress", func() {
			It("should have traefik ingress annotations", func() {
				headlampIngress := &networking.Ingress{}
				err := k8sClient.Get(ctx, ctrlClient.ObjectKeyFromObject(headlampHr), headlampIngress)
				Expect(err).To(BeNil())
				Expect(headlampIngress.Annotations).To(HaveKeyWithValue("kubernetes.io/ingress.class", "kommander-traefik"))
				Expect(headlampIngress.Annotations).To(HaveKeyWithValue("traefik.ingress.kubernetes.io/router.tls", "true"))
				Expect(headlampIngress.Annotations).To(HaveKey("traefik.ingress.kubernetes.io/router.middlewares"))
			})

			It("should set the correct path", func() {
				headlampIngress := &networking.Ingress{}
				err := k8sClient.Get(ctx, ctrlClient.ObjectKeyFromObject(headlampHr), headlampIngress)
				Expect(err).To(BeNil())
				Expect(headlampIngress.Spec.Rules).ToNot(BeEmpty())
				Expect(headlampIngress.Spec.Rules[0].HTTP.Paths[0].Path).To(Equal("/dkp/headlamp"))
			})
		})

		Context("Headlamp Availability", func() {
			It("should have a running and ready pod", func() {
				selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app.kubernetes.io/name": "headlamp",
					},
				})
				Expect(err).To(BeNil())

				podList := &corev1.PodList{}
				Eventually(func() ([]corev1.Pod, error) {
					err := k8sClient.List(ctx, podList, &ctrlClient.ListOptions{
						LabelSelector: selector,
					})
					return podList.Items, err
				}).WithPolling(5 * time.Second).WithTimeout(time.Minute).Should(HaveLen(1))

				pod := podList.Items[0]
				Expect(pod.Status.Phase).To(Equal(corev1.PodRunning))
				ready := false
				for _, cond := range pod.Status.Conditions {
					if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
						ready = true
					}
				}
				Expect(ready).To(BeTrue())
			})
		})
	})
})
