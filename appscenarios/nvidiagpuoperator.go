package appscenarios

import (
	"context"
	"os"
	"path/filepath"

	"github.com/nutanix-cloud-native/nkp-catalog-tests/constants"
	"github.com/nutanix-cloud-native/nkp-catalog-tests/environment"
	"github.com/nutanix-cloud-native/nkp-catalog-tests/scenarios"
)

type nvidiaGpuOperator struct {
	appPathCurrentVersion  string
	appPathPreviousVersion string
}

func (n nvidiaGpuOperator) Name() string {
	return constants.NvidiaGpuOperator
}

var _ scenarios.AppScenario = (*nvidiaGpuOperator)(nil)

func NewNvidiaGpuOperator() *nvidiaGpuOperator {
	appPath, _ := absolutePathTo(constants.NvidiaGpuOperator)
	// The previous-version path is only available when the upgrade
	// kommander-applications checkout contains this app. It is intentionally
	// tolerant of the error here; the upgrade spec guards on it (a from-version
	// predating the DRA-based nvidia-gpu-operator will not have the directory).
	appPrevVerPath, _ := getkAppsUpgradePath(constants.NvidiaGpuOperator)
	return &nvidiaGpuOperator{
		appPathCurrentVersion:  appPath,
		appPathPreviousVersion: appPrevVerPath,
	}
}

func (n nvidiaGpuOperator) Install(ctx context.Context, env *environment.Env) error {
	return n.install(ctx, env, n.appPathCurrentVersion)
}

func (n nvidiaGpuOperator) InstallPreviousVersion(ctx context.Context, env *environment.Env) error {
	return n.install(ctx, env, n.appPathPreviousVersion)
}

func (n nvidiaGpuOperator) Upgrade(ctx context.Context, env *environment.Env) error {
	return n.install(ctx, env, n.appPathCurrentVersion)
}

// install applies the nvidia-gpu-operator manifests for the given version
// directory. The app's top-level kustomization produces Flux Kustomization
// wrappers that reference a GitRepository named "management" which does not
// exist in test clusters, so (like knative) we apply the pre-install/,
// helmrelease/ and dra-driver/ subdirectories directly.
//
// The subdirectories are applied conditionally with os.Stat because older
// (pre-DRA) versions only ship helmrelease/ (and grafana-dashboards/) and have
// neither pre-install/ nor dra-driver/. helmrelease/ is always present.
func (n nvidiaGpuOperator) install(ctx context.Context, env *environment.Env, appPath string) error {
	// appVersion is embedded in generated resource names (the config-defaults
	// ConfigMap and the chart OCIRepository) and must be internally consistent
	// across the manifests of a single version directory. Deriving it from the
	// directory base name keeps it correct regardless of which version is
	// applied (current vs. previous).
	subst := map[string]string{
		"releaseName":              constants.NvidiaGpuOperator,
		"appVersion":               filepath.Base(appPath),
		"releaseNamespace":         kommanderNamespace,
		"workspaceNamespace":       kommanderNamespace,
		"kubetoolsImageRepository": "docker.io/mesosphere/kubectl",
		"kubetoolsImageTag":        "v1.37.0-alpine",
	}

	// The pre-install Job decides between DevicePlugin and DRA. It only exists
	// in the DRA-based (26.3.3+) versions.
	preInstallPath := filepath.Join(appPath, "pre-install")
	if _, err := os.Stat(preInstallPath); err == nil {
		if err := env.ApplyKustomizations(ctx, preInstallPath, subst); err != nil {
			return err
		}
	}

	// helmrelease/ carries the operator OCIRepository, HelmRelease and the
	// default values ConfigMap. It is present in every version.
	helmreleasePath := filepath.Join(appPath, "helmrelease")
	if err := env.ApplyKustomizations(ctx, helmreleasePath, subst); err != nil {
		return err
	}

	// dra-driver/ carries the separate NVIDIA DRA driver HelmRelease and only
	// exists in the DRA-based versions.
	draPath := filepath.Join(appPath, "dra-driver")
	if _, err := os.Stat(draPath); err == nil {
		if err := env.ApplyKustomizations(ctx, draPath, subst); err != nil {
			return err
		}
	}

	return nil
}
