# Kubernetes-Scanner

## Running tests

- Install Kubebuilder from your package manager of choice
- Install setup-envtest with Go: `go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest`
- Install the required tools: `setup-envtest use`
- Setup the environment: `setup-envtest use -p | source`

(yes this is not optimal)

See [this](https://pkg.go.dev/sigs.k8s.io/controller-runtime/tools/setup-envtest#section-readme)
for more info.
