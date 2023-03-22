# Kubernetes-Scanner

The kubernetes-scanner watches the configured types on a Kubernetes API server
and will send the received resources to Snyk.

## Installation

There is a [Helm chart](https://helm.sh) within this repo in
[helm/kubernetes-scanner](https://github.com/snyk/kubernetes-scanner/tree/main/helm/kubernetes-scanner),
that is hosted through Github pages in
`https://snyk.github.io/kubernetes-scanner`.

To install the Helm chart with all default values:

```shell
helm repo add kubernetes-scanner https://snyk.github.io/kubernetes-scanner
helm install <release-name> \
    -f organizationID=<your Snyk organization ID> \
    -f secretName=<secret containing your auth credentials> \
    kubernetes-scanner/kubernetes-scanner
```

For further information on how to install and configure kubernetes-scanner, have
a look at the
[values.yaml](https://github.com/snyk/kubernetes-scanner/tree/main/helm/kubernetes-scanner/values.yaml)

## Running tests

kubernetes-scanner is built on top of `controller-runtime` and uses
`controller-runtime`'s `envtest` to run tests against a real API Server. To run
the tests, you will need to have the `kube-apiserver` and `etcd` binaries
installed in `/usr/local/kubebuilder/bin` or set the `KUBEBUILDER_ASSETS`
environment variable to where your binaries are located in.

To help with this, you can install `setup-envtest`, which can be installed
through the `go install` command:

```shell
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
```

After the installation is completed, the following command will download the
`kube-apiserver` and `etcd` binaries and populate the `KUBEBUILDER_ASSETS`
environment variable:

```shell
setup-envtest use -p env | source
```

For more information on `setup-envtest`, we refer to
[their documentation](https://pkg.go.dev/sigs.k8s.io/controller-runtime/tools/setup-envtest#section-readme).

## Architecture

For an overview of the architecture of kubernetes-scanner, please see the
[architecture document](./docs/architecture.md)
