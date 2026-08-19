# nkp-catalog-tests

**nkp-catalog-tests** is a standalone, reusable application testing framework for the Nutanix Kubernetes Platform (NKP).

## Usage

This project is structured as a standard Go module. To consume it in your Go project:

```bash
go get github.com/nutanix-cloud-native/nkp-catalog-tests@v0.1.0
```

## Local Development

This repository uses [Devbox](https://www.jetpack.io/devbox) to ensure reproducible and consistent toolchains.

1. Install Devbox if you haven't already.
2. Run `devbox shell` to enter the environment, which provisions Go 1.22 and other required tools.

## Contributing

1. Ensure you have installed and enabled the pre-commit hooks (`pre-commit install`) to run `golangci-lint` and `shfmt` automatically.
2. Push your branch and open a Pull Request.
3. Once approved, merge to the `main` branch. 

Releases are published automatically via GitHub Actions upon tagging the `main` branch with a semantic version (e.g. `vX.Y.Z`).
