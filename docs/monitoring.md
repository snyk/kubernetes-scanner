# Monitoring the Snyk Kubernetes scanner

The kubernetes-scanner is built with standard Kubernetes controller libraries
and tools: controller-runtime and Kubebuilder. As such, it exposes the
[standard controller metrics](https://book.kubebuilder.io/reference/metrics-reference.html)
on a Prometheus endpoint.

If the scanner fails to push resources to Snyk's backend, the reconciliation
will fail. The controller-runtime will ensure a continuous retry (with backoff).
The metric `controller_runtime_reconcile_errors_total` is incremented for each
failure. We recommend collecting these metrics and setting alerts on elevated
reconcile error ratio. An example prometheus alerting query, that would return
data when the error ratio exceeds 1%:

```
sum(rate(controller_runtime_reconcile_errors_total[1m])) / sum(rate(controller_runtime_reconcile_total[1m])) > 0.01
```

Occasional failures are expected, and the backoff-retry mechanism may often
resolve them. As such it's recommended to set a sufficiently long `for` rule on
any Prometheus alerts to balance signal with noise.

In response to alerts, see the scanner's logs for details on what might be going
wrong. Please report persistent errors to
[Snyk support](https://support.snyk.io/).
