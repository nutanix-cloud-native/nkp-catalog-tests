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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8smeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrlClient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nutanix-cloud-native/nkp-catalog-tests/constants"
)

const (
	// nvidiaOperatorHRName is the operator HelmRelease name (see helmrelease/nvidia.yaml).
	nvidiaOperatorHRName = "nvidia-gpu-operator"
	// nvidiaDRAHRName is the DRA driver HelmRelease name (see dra-driver/dra-driver.yaml).
	nvidiaDRAHRName = "nvidia-dra-driver"
	// nvidiaPreInstallJobName is the pre-install Job name (see pre-install/pre-install-job.yaml).
	nvidiaPreInstallJobName = "nvidia-gpu-operator-pre-install"
	// nvidiaDevicePluginConfigMapName is the ConfigMap the pre-install Job creates
	// to preserve an existing DevicePlugin configuration ("<releaseName>-config-deviceplugin-defaults").
	nvidiaDevicePluginConfigMapName = "nvidia-gpu-operator-config-deviceplugin-defaults"
)

var _ = Describe("NVIDIA GPU Operator Tests", Label(constants.NvidiaGpuOperator), func() {
	var n *nvidiaGpuOperator

	BeforeEach(OncePerOrdered, func() {
		err := SetupKindCluster()
		Expect(err).To(BeNil())

		err = env.InstallLatestFlux(ctx)
		Expect(err).To(BeNil())

		// ApplyKommanderBaseKustomizations installs the dkp priority classes,
		// including dkp-critical-priority which the pre-install Job requires.
		err = env.ApplyKommanderBaseKustomizations(ctx)
		Expect(err).To(BeNil())

		n = NewNvidiaGpuOperator()
	})

	AfterEach(OncePerOrdered, func() {
		if os.Getenv("SKIP_CLUSTER_TEARDOWN") != "" {
			return
		}

		err := env.Destroy(ctx)
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("NVIDIA GPU Operator Install Test", Ordered, Label("install"), func() {
		It("should install NVIDIA GPU Operator dependencies", func() {
			installNVIDIAGPUOperatorDependencies()
		})

		It("should install nvidia-gpu-operator successfully with the default (DRA) config", func() {
			Expect(n.Install(ctx, env)).To(Succeed())

			// The pre-install Job decides DevicePlugin vs DRA. On a fresh
			// install there is no ClusterPolicy, so it enables DRA by ensuring
			// the deviceplugin-defaults override ConfigMap does not exist.
			waitForNvidiaPreInstallJob(ctx)
		})

		It("should not create the deviceplugin-defaults ConfigMap on a fresh install (DRA path)", func() {
			cm := &corev1.ConfigMap{}
			err := k8sClient.Get(ctx, ctrlClient.ObjectKey{
				Name:      nvidiaDevicePluginConfigMapName,
				Namespace: kommanderNamespace,
			}, cm)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(),
				"deviceplugin-defaults ConfigMap must be absent on a fresh install so DRA stays enabled")
		})

		It("should reconcile the nvidia-gpu-operator HelmRelease to Ready", func() {
			waitForNvidiaHelmReleaseReady(ctx, nvidiaOperatorHRName, 10*time.Minute)
		})

		It("should reconcile the nvidia-dra-driver HelmRelease to Ready (DRA enabled)", func() {
			waitForNvidiaHelmReleaseReady(ctx, nvidiaDRAHRName, 10*time.Minute)
		})

		It("should reconcile a ClusterPolicy with DevicePlugin disabled", func() {
			Eventually(func() (bool, error) {
				enabled, exists, err := nvidiaClusterPolicyDevicePluginEnabled(ctx)
				if err != nil {
					return false, err
				}
				if !exists {
					return false, fmt.Errorf("ClusterPolicy not created by the operator yet")
				}
				// Poll until the operator has reconciled a ClusterPolicy; then
				// assert DevicePlugin is disabled (DRA is the active path).
				return enabled, nil
			}).WithPolling(pollInterval).WithTimeout(10*time.Minute).Should(BeFalse(),
				"fresh install must leave ClusterPolicy spec.devicePlugin.enabled=false (DRA active)")
		})
	})

	// The upgrade block relies on UPGRADE_KAPPS_REPO_PATH pointing at a
	// kommander-applications checkout that contains nvidia-gpu-operator. The
	// DRA-based nvidia-gpu-operator is new, so from-versions predating it do
	// not ship the app directory; getkAppsUpgradePath then fails and we Skip so
	// the matrix upgrade job does not hard-fail for those from-versions.
	Describe("NVIDIA GPU Operator Upgrade Test", Ordered, Label("upgrade"), func() {
		var (
			preDevicePluginEnabled bool
			preClusterPolicyExists bool
		)

		It("should install NVIDIA GPU Operator dependencies", func() {
			installNVIDIAGPUOperatorDependencies()
		})

		It("should skip when the previous version does not contain nvidia-gpu-operator", func() {
			if _, err := getkAppsUpgradePath(constants.NvidiaGpuOperator); err != nil {
				Skip(fmt.Sprintf("previous version does not contain %s (%v); skipping upgrade",
					constants.NvidiaGpuOperator, err))
			}
			Expect(n.appPathPreviousVersion).ToNot(BeEmpty())
		})

		It("should install the previous version successfully", func() {
			Expect(n.InstallPreviousVersion(ctx, env)).To(Succeed())
			waitForNvidiaHelmReleaseReady(ctx, nvidiaOperatorHRName, 10*time.Minute)
		})

		It("should capture the pre-upgrade DevicePlugin/DRA state", func() {
			// The operator reconciles a ClusterPolicy shortly after the
			// HelmRelease is Ready. Poll for it, then record the DevicePlugin
			// state so we can assert the upgrade preserves it. A missing
			// ClusterPolicy is treated as DevicePlugin-off.
			Eventually(func() (bool, error) {
				_, exists, err := nvidiaClusterPolicyDevicePluginEnabled(ctx)
				if err != nil {
					return false, err
				}
				return exists, nil
			}).WithPolling(pollInterval).WithTimeout(10*time.Minute).Should(BeTrue(),
				"expected the operator to reconcile a ClusterPolicy before upgrade")

			var err error
			preDevicePluginEnabled, preClusterPolicyExists, err = nvidiaClusterPolicyDevicePluginEnabled(ctx)
			Expect(err).To(BeNil())
			GinkgoWriter.Printf("pre-upgrade state: clusterPolicyExists=%t devicePluginEnabled=%t\n",
				preClusterPolicyExists, preDevicePluginEnabled)
		})

		It("should upgrade to the current version successfully", func() {
			Expect(n.Upgrade(ctx, env)).To(Succeed())
			waitForNvidiaPreInstallJob(ctx)
			waitForNvidiaHelmReleaseReady(ctx, nvidiaOperatorHRName, 10*time.Minute)
		})

		It("should preserve the pre-upgrade DevicePlugin/DRA configuration", func() {
			cmKey := ctrlClient.ObjectKey{
				Name:      nvidiaDevicePluginConfigMapName,
				Namespace: kommanderNamespace,
			}

			if preClusterPolicyExists && preDevicePluginEnabled {
				// DevicePlugin was active: the pre-install Job must create the
				// override ConfigMap so DevicePlugin stays enabled and DRA off.
				cm := &corev1.ConfigMap{}
				Eventually(func() error {
					return k8sClient.Get(ctx, cmKey, cm)
				}).WithPolling(pollInterval).WithTimeout(5*time.Minute).Should(Succeed(),
					"deviceplugin-defaults ConfigMap must exist to preserve DevicePlugin")
				Expect(cm.Data).To(HaveKey("operator-deviceplugin-enabled-values.yaml"))
				Expect(cm.Data).To(HaveKey("dra-driver-disabled-values.yaml"))

				Eventually(func() (bool, error) {
					enabled, exists, err := nvidiaClusterPolicyDevicePluginEnabled(ctx)
					if err != nil {
						return false, err
					}
					if !exists {
						return false, fmt.Errorf("ClusterPolicy not present after upgrade")
					}
					return enabled, nil
				}).WithPolling(pollInterval).WithTimeout(10*time.Minute).Should(BeTrue(),
					"DevicePlugin must remain enabled after upgrade")
			} else {
				// DevicePlugin was off (or there was no ClusterPolicy): DRA
				// stays active, so the override ConfigMap must be absent.
				Consistently(func() bool {
					cm := &corev1.ConfigMap{}
					err := k8sClient.Get(ctx, cmKey, cm)
					return apierrors.IsNotFound(err)
				}).WithPolling(pollInterval).WithTimeout(30*time.Second).Should(BeTrue(),
					"deviceplugin-defaults ConfigMap must stay absent so DRA remains enabled")

				waitForNvidiaHelmReleaseReady(ctx, nvidiaDRAHRName, 10*time.Minute)

				Eventually(func() (bool, error) {
					enabled, exists, err := nvidiaClusterPolicyDevicePluginEnabled(ctx)
					if err != nil {
						return false, err
					}
					if !exists {
						return false, fmt.Errorf("ClusterPolicy not present after upgrade")
					}
					return enabled, nil
				}).WithPolling(pollInterval).WithTimeout(10*time.Minute).Should(BeFalse(),
					"DevicePlugin must remain disabled after upgrade (DRA active)")
			}
		})
	})
})

// waitForNvidiaPreInstallJob blocks until the nvidia-gpu-operator-pre-install
// Job reports at least one successful completion.
func waitForNvidiaPreInstallJob(ctx context.Context) {
	GinkgoHelper()

	job := &batchv1.Job{}
	Eventually(func() error {
		if err := k8sClient.Get(ctx, ctrlClient.ObjectKey{
			Name:      nvidiaPreInstallJobName,
			Namespace: kommanderNamespace,
		}, job); err != nil {
			return err
		}
		if job.Status.Succeeded < 1 {
			return fmt.Errorf("pre-install job has not succeeded yet (succeeded=%d)", job.Status.Succeeded)
		}
		return nil
	}).WithPolling(pollInterval).WithTimeout(5 * time.Minute).Should(Succeed())
}

// waitForNvidiaHelmReleaseReady blocks until the named HelmRelease in the
// kommander namespace reports Ready=True.
func waitForNvidiaHelmReleaseReady(ctx context.Context, name string, timeout time.Duration) {
	GinkgoHelper()

	hr := &fluxhelmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: kommanderNamespace,
		},
	}

	Eventually(func() error {
		if err := k8sClient.Get(ctx, ctrlClient.ObjectKeyFromObject(hr), hr); err != nil {
			return err
		}
		for _, cond := range hr.Status.Conditions {
			if cond.Type == apimeta.ReadyCondition && cond.Status == metav1.ConditionTrue {
				return nil
			}
		}
		return fmt.Errorf("%s helm release not ready yet", name)
	}).WithPolling(pollInterval).WithTimeout(timeout).Should(Succeed())
}

// nvidiaClusterPolicyDevicePluginEnabled reads the (single) nvidia.com/v1
// ClusterPolicy and reports spec.devicePlugin.enabled. The second return value
// indicates whether any ClusterPolicy exists. A missing ClusterPolicy CRD is
// treated as "no ClusterPolicy" rather than an error, mirroring the pre-install
// script's handling.
func nvidiaClusterPolicyDevicePluginEnabled(ctx context.Context) (enabled bool, exists bool, err error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "nvidia.com",
		Version: "v1",
		Kind:    "ClusterPolicyList",
	})

	if err := k8sClient.List(ctx, list); err != nil {
		// Before the operator installs the ClusterPolicy CRD, listing fails
		// with a discovery no-match error. Treat that as "no ClusterPolicy".
		if k8smeta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return false, false, nil
		}
		return false, false, err
	}

	if len(list.Items) == 0 {
		return false, false, nil
	}

	enabled, _, err = unstructured.NestedBool(list.Items[0].Object, "spec", "devicePlugin", "enabled")
	if err != nil {
		return false, true, err
	}
	return enabled, true, nil
}

func installNVIDIAGPUOperatorDependencies() {
	// cm.yaml enables dcgmExporter.serviceMonitor, so the chart renders
	// a ServiceMonitor. Install the CRD so Helm can apply it in clusters
	// that lack kube-prometheus-stack.
	By("Installing ServiceMonitor CRD")
	err := installServiceMonitorCRD(ctx, env)
	Expect(err).To(BeNil())

	// The NVIDIA DRA Driver webhook requires cert-manager to be installed.
	By("Installing cert-manager")
	cm := certManager{}
	err = cm.Install(ctx, env)
	Expect(err).To(BeNil())

	hr := &fluxhelmv2.HelmRelease{
		TypeMeta: metav1.TypeMeta{
			Kind:       fluxhelmv2.HelmReleaseKind,
			APIVersion: fluxhelmv2.GroupVersion.Version,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.CertManager,
			Namespace: kommanderNamespace,
		},
	}

	Eventually(func() error {
		err = k8sClient.Get(ctx, ctrlClient.ObjectKeyFromObject(hr), hr)
		if err != nil {
			return err
		}

		for _, cond := range hr.Status.Conditions {
			if cond.Status == metav1.ConditionTrue &&
				cond.Type == apimeta.ReadyCondition {
				return nil
			}
		}
		return fmt.Errorf("helm release not ready yet")
	}).WithPolling(pollInterval).WithTimeout(5 * time.Minute).Should(Succeed())

	By("Verifying that cert-manager CRDs are installed")
	certManagerCrds := &fluxhelmv2.HelmRelease{
		TypeMeta: metav1.TypeMeta{
			Kind:       fluxhelmv2.HelmReleaseKind,
			APIVersion: fluxhelmv2.GroupVersion.Version,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cert-manager-crds",
			Namespace: kommanderNamespace,
		},
	}

	Eventually(func() error {
		err := k8sClient.Get(ctx, ctrlClient.ObjectKeyFromObject(certManagerCrds), certManagerCrds)
		if err != nil {
			return err
		}

		for _, cond := range certManagerCrds.Status.Conditions {
			if cond.Status == metav1.ConditionTrue &&
				cond.Type == apimeta.ReadyCondition {
				return nil
			}
		}
		return fmt.Errorf("helm release not ready yet")
	}).WithPolling(pollInterval).WithTimeout(5 * time.Minute).Should(Succeed())
}
