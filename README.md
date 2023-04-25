# Kubernetes-Scanner

The kubernetes-scanner watches the configured types on a Kubernetes API server
and will send resource configurations to Snyk.

This is just a data collection component that is part of a larger system. You
only need to install this if you have been directed here from other
documentation.

## Usage

### Installation

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

The actor running Helm needs to be empowered to create the resources templated
by this chart.

Or using chart dependencies:

```
# Chart.yaml
dependencies:
  - name: kubernetes-scanner
    version: v0.10.0
    repository: https://snyk.github.io/kubernetes-scanner
    alias: kubernetes-scanner

```

Release versions can be found [in GitHub](https://github.com/snyk/kubernetes-scanner/releases).

For further information on how to install and configure kubernetes-scanner,
please familiarize yourself with the commented configuration in
[values.yaml](https://github.com/snyk/kubernetes-scanner/tree/main/helm/kubernetes-scanner/values.yaml).

There are some mandatory fields, each marked with "MANDATORY" in a comment.

### Monitoring

See [monitoring.md](docs/monitoring.md).

### HTTP proxy compatibility

Some users may want the scanner to send its HTTP requests via a proxy, for
example in order to ensure that it is only communicating with an allowlist of
hosts. The scanner supports the standard `HTTPS_PROXY` environment variable in
order to accomplish this. Helm users can set it via the extraEnv value, in your
values file, for example, for example:

```yaml
extraEnv:
  - name: HTTPS_PROXY
    value: "a-proxy:3128"
```

You will need to allowlist both Snyk's API server, and your Kubernetes API
server. Snyk HTTP requests are sent to
`https://$HOST/hidden/orgs/$ORG_ID/kubernetes_resources?version=$API_VERSION`.

`$HOST` will be `api.snyk.io` (unless otherwise communicated). You might use
this to form the basis of a proxy ACL rule. Example for [`squid`
proxy](http://www.squid-cache.org/), that denies all other traffic:

```
acl allowlist dstdomain api.snyk.io <don't forget to add your Kubernetes domain>
http_access allow allowlist
http_access deny all
http_port 3128
```

`$ORG_ID` is one of your configured org IDs, from the "routes" section of your
configuration.

The value of the `$API_VERSION` query parameter should not be depended on, it
may change in subsequent scanner versions.

## Development

You only need to read this section if you are interested in contributing to this
project.

### Running tests

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
eval "$(setup-envtest use -p env)"
```

For more information on `setup-envtest`, we refer to
[their documentation](https://pkg.go.dev/sigs.k8s.io/controller-runtime/tools/setup-envtest#section-readme).

### Architecture

For an overview of the architecture of kubernetes-scanner, please see the
[architecture document](./docs/architecture.md)
