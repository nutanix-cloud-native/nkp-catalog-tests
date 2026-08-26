# nkp-catalog-tests

**nkp-catalog-tests** is a standalone, reusable application testing framework for the Nutanix Kubernetes Platform (NKP).

## Usage

This project is structured as a standard Go module. To consume it in your Go project:

```bash
go get github.com/nutanix-cloud-native/nkp-catalog-tests@v0.1.0
```

### `NKP_CATALOG_DIR`

This module does not ship catalog application manifests. Application tests and environment helpers read them from an external catalog checkout (for example `nkp-ai-applications-catalog` or `kommander-applications`).

Set `NKP_CATALOG_DIR` to that checkout's root — the directory that contains `applications/`:

```bash
export NKP_CATALOG_DIR=/path/to/nkp-catalog
```

The library then resolves application manifests under `$NKP_CATALOG_DIR/applications/<app>/<version>/`.

The value may be an absolute path, or a path relative to the process working directory. Prefer an absolute path when running `ginkgo` or `go test` from a package subdirectory (`ginkgo` sets the working directory to the test package).

If `NKP_CATALOG_DIR` is unset, the library falls back to this repository's root. That tree has no `applications/` or `common/` directories, so Ginkgo application tests fail unless the variable is set.

`NKP_CATALOG_DIR` does not affect fixtures in this repository's `testdata/` directory.

## Local Development

This repository uses [Devbox](https://www.jetpack.io/devbox) to ensure reproducible and consistent toolchains.

1. Install Devbox if you haven't already.
2. Run `devbox shell` to enter the environment, which provisions Go 1.22 and other required tools.
3. Export `NKP_CATALOG_DIR` to a catalog checkout before running Ginkgo application tests (see above). See [tests-doc.md](tests-doc.md) for test labels and run commands.

## Contributing

1. Ensure you have installed and enabled the pre-commit hooks (`pre-commit install`) to run `golangci-lint` and `shfmt` automatically.
2. Push your branch and open a Pull Request.
3. Once approved, merge to the `main` branch. 

Releases are published automatically via GitHub Actions upon tagging the `main` branch with a semantic version (e.g. `vX.Y.Z`).
