# Monitoring the Snyk Kubernetes scanner

The kubernetes-scanner is built with standard Kubernetes controller libraries
and tools: controller-runtime and Kubebuilder. As such, it exposes the
[standard controller metrics](https://book.kubebuilder.io/reference/metrics-reference.html)
on a Prometheus endpoint.

## Collection

If you are using [the Prometheus Kubernetes operator](https://github.com/prometheus-operator/prometheus-operator)
(or another stack that makes use of its CRDs), you can enable metrics collection
enabling the helm chart's PodMonitor in your values overrides:

```yaml
prometheus:
  podMonitor:
    enabled: true
```

## Alerting

We recommend that kubernetes-scanner monitor the scanner for failures, possibly
with (presumably non-paging) alerts, in order to inform Snyk of potential issues
with your installation. Please report persistent errors to [Snyk
support](https://support.snyk.io/).

When the scanner fails to push resources to Snyk's backend, the reconciliation
will fail. The controller-runtime will ensure a continuous retry (with backoff).
The metric `controller_runtime_reconcile_errors_total` is incremented for each
failure. While we recommend collecting these metrics, we recommend setting
alerts on the age of the oldest reconcilation failure that was not subsequently
successfully retried, rather than on an elevated reconcile error ratio. An
example prometheus query that will alert when a resource has not reconciled for
1 hour:

```
kubernetes_scanner_backend_oldest_failure_age_seconds > 3600
```

If you do wish to alert on the error ratio, this example query will alert when
the error ratio exceeds 5%:

```
sum(rate(controller_runtime_reconcile_errors_total[1m])) / sum(rate(controller_runtime_reconcile_total[1m])) > 0.05
```

The example threshold of 5% may seem high, but occasional failures are
expected, and the backoff-retry mechanism may often resolve them. As such, if
you do wish to alert on error ratio, it's recommended to set a sufficiently long
`for` rule on any such Prometheus alerts to balance signal with noise.

### Investigating errors

In response to alerts, see the scanner's logs for details on what might be going
wrong. Structured log lines with `level:"error"` can be correlated by a
`reconcileID` attribute. Log lines should also contain some metadata about the
resource in question: group-version, kind, name, and UID.

When contacting support, please send all this information along with your
configured Snyk organization ID and cluster name, so that the maintaining team
can investigate persistent errors.
