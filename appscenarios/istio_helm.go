package appscenarios

import (
	"context"
	"fmt"
	"path/filepath"

	fluxhelmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlClient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nutanix-cloud-native/nkp-catalog-tests/constants"
	"github.com/nutanix-cloud-native/nkp-catalog-tests/environment"
	"github.com/nutanix-cloud-native/nkp-catalog-tests/flux"
	"github.com/nutanix-cloud-native/nkp-catalog-tests/scenarios"
)

const (
	istioHelmSystem         = "istio-helm-system"
	istioHelmGatewayNS      = "istio-helm-gateway-ns"
	istioHelmRevision       = "istio-helm"
	istioHelmOverrideCMName = "istio-helm-config-overrides"
	istioHelmReleaseName    = "app-deployment-name"
	istioHelmAppVersion     = "app-version-istio-helm"
)

// istioHelmSubstitutions are the Flux postBuild variable substitutions for the helmrelease/ kustomization.
var istioHelmSubstitutions = map[string]string{
	"releaseName":              istioHelmReleaseName,
	"appVersion":               istioHelmAppVersion,
	"releaseNamespace":         kommanderNamespace,
	"kubetoolsImageRepository": "docker.io/mesosphere/kubectl",
	"kubetoolsImageTag":        kubetoolsImageTag,
}

type istioHelm struct{}

func (i istioHelm) Name() string {
	return constants.IstioHelm
}

var _ scenarios.AppScenario = (*istioHelm)(nil)

// Install installs istio-helm at the current version with sidecar mode (default).
func (i istioHelm) Install(ctx context.Context, env *environment.Env) error {
	appPath, err := absolutePathTo(i.Name())
	if err != nil {
		return err
	}
	return i.install(ctx, env, appPath)
}

// InstallPreviousVersion installs the previous released version of istio-helm.
// Requires the UPGRADE_KAPPS_REPO_PATH environment variable to point to a checkout
// of the previous kommander-applications version.
func (i istioHelm) InstallPreviousVersion(ctx context.Context, env *environment.Env) error {
	appPath, err := getkAppsUpgradePath(i.Name())
	if err != nil {
		return err
	}
	return i.install(ctx, env, appPath)
}

// Upgrade upgrades from the previously installed version to the current version.
func (i istioHelm) Upgrade(ctx context.Context, env *environment.Env) error {
	appPath, err := absolutePathTo(i.Name())
	if err != nil {
		return err
	}
	return i.install(ctx, env, appPath)
}

// InstallWithAmbientMode installs istio-helm with ztunnel / ambient mode enabled.
func (i istioHelm) InstallWithAmbientMode(ctx context.Context, env *environment.Env) error {
	appPath, err := absolutePathTo(i.Name())
	if err != nil {
		return err
	}

	// Apply the ambient override CM first so Flux picks it up on first reconcile.
	if err := i.applyOverrideCM(ctx, env, "ambient-overrides.yaml"); err != nil {
		return fmt.Errorf("applying ambient override configmap: %w", err)
	}

	return i.install(ctx, env, appPath)
}

// EnableAmbientMode patches the existing istio-helm-config-overrides ConfigMap to
// switch to ambient mode and triggers reconciliation of all component HelmReleases.
func (i istioHelm) EnableAmbientMode(ctx context.Context, env *environment.Env) error {
	if err := i.applyOverrideCM(ctx, env, "ambient-overrides.yaml"); err != nil {
		return fmt.Errorf("applying ambient override configmap: %w", err)
	}
	return i.triggerReconciliation(ctx, env)
}

// InstallWithMultipleGateways installs istio-helm with two additional gateways
// (istio-egressgateway and istio-ingressgateway-internal) pre-configured via the
// multi-gateway override ConfigMap so Flux picks them up on the first reconcile.
func (i istioHelm) InstallWithMultipleGateways(ctx context.Context, env *environment.Env) error {
	appPath, err := absolutePathTo(i.Name())
	if err != nil {
		return err
	}

	if err := i.applyOverrideCM(ctx, env, "multi-gateway-overrides.yaml"); err != nil {
		return fmt.Errorf("applying multi-gateway override configmap: %w", err)
	}

	return i.install(ctx, env, appPath)
}

// EnableMultipleGateways upserts the multi-gateway override ConfigMap onto a
// running sidecar-mode install and forces immediate HelmRelease reconciliation
// so the additional gateways are deployed without waiting for the 15s interval.
func (i istioHelm) EnableMultipleGateways(ctx context.Context, env *environment.Env) error {
	if err := i.applyOverrideCM(ctx, env, "multi-gateway-overrides.yaml"); err != nil {
		return fmt.Errorf("applying multi-gateway override configmap: %w", err)
	}
	return i.triggerReconciliation(ctx, env)
}

func (i istioHelm) install(ctx context.Context, env *environment.Env, appPath string) error {
	if err := i.ensureOverrideCM(ctx, env); err != nil {
		return fmt.Errorf("ensuring override configmap: %w", err)
	}

	for _, ns := range []string{istioHelmSystem, istioHelmGatewayNS} {
		if err := i.ensureNamespace(ctx, env, ns); err != nil {
			return fmt.Errorf("creating namespace %s: %w", ns, err)
		}
	}

	gatewayNSPath := filepath.Join(appPath, "istio-helm-gateway-namespace")
	if err := env.ApplyYAML(ctx, gatewayNSPath, istioHelmSubstitutions); err != nil {
		return fmt.Errorf("applying gateway namespace: %w", err)
	}

	// The istiod chart renders ServiceMonitor resources (monitoring.coreos.com/v1).
	// Those CRDs are only present when kube-prometheus-stack is installed, which we
	// skip in apptests Kind clusters. Install a minimal stub CRD so Helm can
	// discover the API group and render the manifests without failing.
	if err := installServiceMonitorCRD(ctx, env); err != nil {
		return fmt.Errorf("installing ServiceMonitor CRD stub: %w", err)
	}

	releasePath := filepath.Join(appPath, "helmrelease")
	if err := env.ApplyKustomizations(ctx, releasePath, istioHelmSubstitutions); err != nil {
		return fmt.Errorf("applying helmrelease kustomization: %w", err)
	}

	return i.removeBaseHRDependsOn(ctx, env)
}

// ensureOverrideCM applies the single-replica override ConfigMap only if no override
// CM already exists in the cluster. This allows tests that pre-apply the ambient
// overrides CM to skip the single-replica override without clobbering it.
func (i istioHelm) ensureOverrideCM(ctx context.Context, env *environment.Env) error {
	client, err := ctrlClient.New(env.K8sClient.Config(), ctrlClient.Options{Scheme: flux.NewScheme()})
	if err != nil {
		return err
	}

	existing := &corev1.ConfigMap{}
	err = client.Get(ctx, ctrlClient.ObjectKey{Name: istioHelmOverrideCMName, Namespace: kommanderNamespace}, existing)
	if err == nil {
		return nil
	}
	if !k8serrors.IsNotFound(err) {
		return err
	}

	return i.applyOverrideCM(ctx, env, "single-replica-overrides.yaml")
}

func (i istioHelm) applyOverrideCM(ctx context.Context, env *environment.Env, filename string) error {
	testDataPath, err := getTestDataDir()
	if err != nil {
		return err
	}
	cmPath := filepath.Join(testDataPath, "istio-helm", filename)
	return env.ApplyYAML(ctx, cmPath, map[string]string{
		"releaseNamespace": kommanderNamespace,
	})
}

func (i istioHelm) ensureNamespace(ctx context.Context, env *environment.Env, name string) error {
	client, err := ctrlClient.New(env.K8sClient.Config(), ctrlClient.Options{Scheme: flux.NewScheme()})
	if err != nil {
		return err
	}
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	err = client.Create(ctx, ns)
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// removeBaseHRDependsOn patches the `<releaseName>-base` HelmRelease to clear its
// spec.dependsOn slice. In production, kube-prometheus-stack is required; in the
// apptests Kind cluster it is absent, so Flux would block indefinitely.
func (i istioHelm) removeBaseHRDependsOn(ctx context.Context, env *environment.Env) error {
	client, err := ctrlClient.New(env.K8sClient.Config(), ctrlClient.Options{Scheme: flux.NewScheme()})
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	hr := &fluxhelmv2.HelmRelease{
		TypeMeta: metav1.TypeMeta{
			Kind:       fluxhelmv2.HelmReleaseKind,
			APIVersion: fluxhelmv2.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      istioHelmReleaseName + "-base",
			Namespace: kommanderNamespace,
		},
	}

	if err := client.Get(ctx, ctrlClient.ObjectKeyFromObject(hr), hr); err != nil {
		return fmt.Errorf("getting base HelmRelease: %w", err)
	}

	hr.Spec.DependsOn = nil
	if err := client.Update(ctx, hr); err != nil {
		return fmt.Errorf("removing dependsOn from base HelmRelease: %w", err)
	}

	return nil
}

// triggerReconciliation annotates all istio-helm HelmReleases with
// reconcile.fluxcd.io/requestedAt to force immediate re-evaluation of valuesFrom.
func (i istioHelm) triggerReconciliation(ctx context.Context, env *environment.Env) error {
	client, err := ctrlClient.New(env.K8sClient.Config(), ctrlClient.Options{Scheme: flux.NewScheme()})
	if err != nil {
		return err
	}

	components := []string{"base", "cni", "istiod", "ztunnel", "gateway"}
	for _, component := range components {
		hr := &fluxhelmv2.HelmRelease{}
		key := ctrlClient.ObjectKey{
			Name:      fmt.Sprintf("%s-%s", istioHelmReleaseName, component),
			Namespace: kommanderNamespace,
		}
		if err := client.Get(ctx, key, hr); err != nil {
			if k8serrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("getting %s HelmRelease: %w", component, err)
		}

		if hr.Annotations == nil {
			hr.Annotations = make(map[string]string)
		}
		hr.Annotations["reconcile.fluxcd.io/requestedAt"] = metav1.Now().String()
		if err := client.Update(ctx, hr); err != nil {
			return fmt.Errorf("annotating %s HelmRelease: %w", component, err)
		}
	}

	return nil
}
