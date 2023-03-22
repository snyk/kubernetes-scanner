# Architecture

`kubernetes-scanner` is based on `controller-runtime`, which is configured to
dynamically watch resources based on the `unstructured.Unstructured` type
configured with dynamic GroupVersionKinds (GVK).

The controller's core logic can be found in the `main.go` file in the root of
the repository. It handles the setup of the controller, as well as implementing
the main `Reconcile` function. That `Reconcile` function is responsible for
populating the `unstructured.Unstructured` object, and passed that to the
backend store, located in `internal/backend`.

The backend constructs a REST request to push the resource manifests to Snyk.

In the future, we're expecting the backend to start batching upserts, and
potentially making the batching-parameters user-configurable as well.
