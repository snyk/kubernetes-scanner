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

Initially you need to create a kubernetes secret that contains the API token for the 
[service account](https://docs.snyk.io/snyk-admin/service-accounts) 

The service account must have one of the following roles:
* Org Admin
* Group Admin
* Custom Role with "Publish Kubernetes Resources" permission

If organization level service account is used, it must be associated with the organizationID configured to correlate the 
kubernetes data. Group level service accounts can correlate data to any organization under the group.

```shell
kubectl create secret generic <secret containing your auth credentials> \
  --from-literal=snykServiceAccountToken=<your-service-account-token>
```

To install the Helm chart with all default values:

```shell
helm repo add kubernetes-scanner https://snyk.github.io/kubernetes-scanner

# Update repository if it already exists
helm repo update

helm install <release-name> \
	--set "secretName=<secret containing your auth credentials>" \
	--set "config.clusterName=<your human friendly cluster name>" \
	--set "config.routes[0].organizationID=<your Snyk organization ID>" \
	--set "config.routes[0].clusterScopedResources=true" \
	--set "config.routes[0].namespaces[0]=*"  \
	kubernetes-scanner/kubernetes-scanner
```

The actor running Helm needs to be empowered to create the resources templated
by this chart.

Or using chart dependencies:

```yaml
# Chart.yaml
dependencies:
  - name: kubernetes-scanner
    version: v0.21.0 # use the latest available version
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

### Removing resource attributes

While Kubernetes `v1.Secrets` are designed to separate secret fields into their
own types, referenced in other types' fields such as container environment
variable `secretKeyRefs`, some users may wish to prevent certain fields on
certain types from being sent to Snyk, while still scanning other fields on that
type.

The scanner supports this sort of attribute removal, as an optional array of
paths alongside group-version-kind scan config:

```yaml
- apiGroups: ["apps"]
    versions: ["*"]
    resources:
      - replicasets
      - daemonsets
      - deployments
      - statefulsets
    attributeRemovals:
      - "spec.template.spec.containers.env"
      - "metadata.managedFields"
```

These paths are dot-separated address for nested values, in the same format as
arguments to `kubectl explain`. For example, the expression
"spec.containers.env" will cause Kubernetes Pod container environment variables
to be removed. "containers" is an array, and each element of this array is
redacted in this way.

See
[values.yaml](https://github.com/snyk/kubernetes-scanner/tree/main/helm/kubernetes-scanner/values.yaml)
for examples in Helm values.

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
