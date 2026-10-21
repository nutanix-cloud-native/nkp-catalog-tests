package appscenarios

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nutanix-cloud-native/nkp-catalog-tests/constants"
	"github.com/nutanix-cloud-native/nkp-catalog-tests/environment"
	"github.com/nutanix-cloud-native/nkp-catalog-tests/scenarios"
)

type headlamp struct {
	appPathCurrentVersion  string
	appPathPreviousVersion string
}

func (h headlamp) Name() string {
	return constants.Headlamp
}

var _ scenarios.AppScenario = (*headlamp)(nil)

func NewHeadlamp() *headlamp {
	appPath, _ := absolutePathTo(constants.Headlamp)
	appPrevVerPath, _ := getkAppsUpgradePath(constants.Headlamp)
	return &headlamp{
		appPathCurrentVersion:  appPath,
		appPathPreviousVersion: appPrevVerPath,
	}
}

func (h headlamp) Install(ctx context.Context, env *environment.Env) error {
	return h.install(ctx, env, h.appPathCurrentVersion)
}

func (h headlamp) InstallPreviousVersion(ctx context.Context, env *environment.Env) error {
	return h.install(ctx, env, h.appPathPreviousVersion)
}

func (h headlamp) install(ctx context.Context, env *environment.Env, appPath string) error {
	defaultKustomization := filepath.Join(appPath, "/defaults")
	if _, err := os.Stat(defaultKustomization); err == nil {
		err := env.ApplyKustomizations(ctx, defaultKustomization, map[string]string{
			"appVersion":         "app-version" + filepath.Base(appPath),
			"releaseNamespace":   kommanderNamespace,
			"workspaceNamespace": kommanderNamespace,
		})
		if err != nil {
			return err
		}
	}

	// Apply the helmrelease subdirectory directly so that the actual K8s
	// resources (HelmRelease, OCIRepository, ConfigMap, RBAC) are created.
	// The top-level kustomization.yaml only contains a Flux Kustomization
	// CRD that references a GitRepository source unavailable in test.
	helmreleasePath := filepath.Join(appPath, "helmrelease")
	return env.ApplyKustomizations(ctx, helmreleasePath, map[string]string{
		"appVersion":         "app-version" + filepath.Base(appPath),
		"releaseName":        constants.Headlamp,
		"releaseNamespace":   kommanderNamespace,
		"workspaceNamespace": kommanderNamespace,
	})
}

func (h headlamp) Upgrade(ctx context.Context, env *environment.Env) error {
	return fmt.Errorf("upgrade is not yet implemented")
}
