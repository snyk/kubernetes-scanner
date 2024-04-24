/*
 * © 2023 Snyk Limited
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/snyk/kubernetes-scanner/internal/config"
)

type Backend struct {
	apiEndpoint      string
	clusterName      string
	authorizationKey string

	client *http.Client

	*metrics
}

func New(clusterName string, cfg *config.Egress, reg prometheus.Registerer) *Backend {
	return &Backend{
		apiEndpoint:      cfg.SnykAPIBaseURL,
		clusterName:      clusterName,
		authorizationKey: cfg.SnykServiceAccountToken,

		client: &http.Client{
			// the default transport automatically honors HTTP_PROXY settings.
			Transport: http.DefaultTransport,
			Timeout:   cfg.HTTPClientTimeout.Duration,
		},

		metrics: newMetrics(reg),
	}
}

const contentTypeJSON = "application/vnd.api+json"

func (b *Backend) SanityCheck(ctx context.Context, orgID string) error {
	endpoint := fmt.Sprintf("%s/rest/orgs/%s?version=2024-04-11", b.apiEndpoint, orgID)

	req, err := http.NewRequest(http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return fmt.Errorf("could not construct HTTP request: %w", err)
	}

	req.Header.Add("Authorization", "token "+b.authorizationKey)

	resp, err := b.client.Do(req.WithContext(ctx))
	if err != nil {
		return &transportError{err}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newHTTPError(resp)
	}

	return nil
}

func (b *Backend) Upsert(ctx context.Context, requestID string, orgID string, resources []Resource) error {
	body, err := b.newPostBody(resources)
	if err != nil {
		return fmt.Errorf("could not construct request body: %w", err)
	}

	if _, err := b.do(ctx, http.MethodPost, orgID, requestID, body); err != nil {
		var httpErr *HTTPError
		var transportErr *transportError
		switch {
		case errors.As(err, &transportErr):
			for _, resource := range resources {
				b.recordFailure(ctx, 0, resource.ManifestBlob, resource.DeletedAt)
			}
		case errors.As(err, &httpErr):
			for _, resource := range resources {
				b.recordFailure(ctx, httpErr.StatusCode, resource.ManifestBlob, resource.DeletedAt)
			}
		}
		return fmt.Errorf("could not post resource: %w", err)
	}

	for _, resource := range resources {
		b.recordSuccess(ctx, resource.ManifestBlob)
	}

	return nil
}

func (b *Backend) do(ctx context.Context, method, orgID, requestID string, body io.Reader) (responseBody io.ReadCloser, err error) {
	endpoint := fmt.Sprintf("%s/hidden/orgs/%s/kubernetes_resources?version=2023-02-20~experimental",
		b.apiEndpoint, orgID)

	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("could not construct HTTP request: %w", err)
	}

	req.Header.Add("Content-Type", contentTypeJSON)
	req.Header.Add("Authorization", "token "+b.authorizationKey)
	req.Header.Add("snyk-request-id", requestID)

	resp, err := b.client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, &transportError{err}
	}

	if resp.StatusCode >= 300 || resp.StatusCode < 200 {
		return nil, newHTTPError(resp)
	}

	return resp.Body, nil
}

func (b *Backend) List(ctx context.Context, orgID string) ([]ResponseData, error) {
	body, err := b.do(ctx, http.MethodGet, orgID, "", nil) // TODO: add requestID
	if err != nil {
		return nil, fmt.Errorf("error making HTTP request: %w", err)
	}

	defer body.Close()

	respBody, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("could not read HTTP response body: %w", err)
	}

	var responseBody response
	if err := json.Unmarshal(respBody, &responseBody); err != nil {
		return nil, fmt.Errorf("could not unmarshal body: %w", err)
	}

	return responseBody.Data, nil
}

type transportError struct {
	err error
}

func (t *transportError) Error() string {
	return fmt.Sprintf("HTTP transport error: %v", t.err)
}

func newHTTPError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	return &HTTPError{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		body:       body,
		bodyErr:    err,
	}
}

type HTTPError struct {
	StatusCode int
	Header     http.Header
	body       []byte
	bodyErr    error
}

func (h *HTTPError) Error() string {
	msg := fmt.Sprintf("got non-20x HTTP code %v", h.StatusCode)
	switch {
	case h.bodyErr != nil:
		msg += fmt.Sprintf(" but could not read body: %v", h.bodyErr)
	case len(h.body) != 0:
		msg += fmt.Sprintf(" with body %q", h.body)
	}
	return msg
}

// Values returns a map that is suitable for usage with a `logr.Logger.WithValues` in order to log
// properly indexed / structured fields.
func (h *HTTPError) Values() map[string]any {
	v := map[string]any{
		"header": h.Header,
		"code":   h.StatusCode,
	}
	switch {
	case h.bodyErr != nil:
		v["body_read_err"] = h.bodyErr.Error()
	case len(h.body) != 0:
		v["body"] = string(h.body)
	}
	return v
}

// for testing.
var now = time.Now

func (b *Backend) newPostBody(resources []Resource) (io.Reader, error) {
	r := &request{
		Data: requestData{
			Type: "kubernetesresource",
			Attributes: requestAttributes{
				ClusterName: b.clusterName,
				Resources:   resources,
				// ScannedAt:        metav1.Time{Time: now()},
			},
		},
	}
	body, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("could not marshal request body: %w", err)
	}

	return bytes.NewReader(body), nil
}

type request struct {
	Data requestData `json:"data"`
}

type requestData struct {
	Type       string            `json:"type"`
	Attributes requestAttributes `json:"attributes"`
}
type requestAttributes struct {
	ClusterName string     `json:"cluster_name"`
	Resources   []Resource `json:"resources"`
}

type Resource struct {
	ManifestBlob     client.Object `json:"manifest_blob"`
	PreferredVersion string        `json:"preferred_version"`
	ScannedAt        metav1.Time   `json:"scanned_at"`
	DeletedAt        *metav1.Time  `json:"deleted_at,omitempty"`
}

type response struct {
	Data []ResponseData `json:"data,omitempty"`
	// technically there's more fields here for pagination, but the type that defines them is in a
	// private repo & we don't need them (currently)...
}

type ResponseData struct {
	Attributes *ResponseAttributes `json:"attributes,omitempty"`
	ID         string              `json:"id"`
	Type       string              `json:"type"`
}

type ResponseAttributes struct {
	ClusterName      string                 `json:"cluster_name"`
	DeletedAt        string                 `json:"deleted_at,omitempty"`
	ID               string                 `json:"id"`
	ManifestBlob     map[string]interface{} `json:"manifest_blob"`
	PreferredVersion string                 `json:"preferred_version"`
	ScannedAt        string                 `json:"scanned_at,omitempty"`
}

// resourceIdentifier identifies a specific resource. This cannot be done through
// the UID because the UID is absent on delete requests.
type resourceIdentifier struct {
	schema.GroupVersionKind
	types.NamespacedName
}

func newResourceID(from client.Object) resourceIdentifier {
	return resourceIdentifier{
		GroupVersionKind: from.GetObjectKind().GroupVersionKind(),
		NamespacedName: types.NamespacedName{
			Name:      from.GetName(),
			Namespace: from.GetNamespace(),
		},
	}
}

type metrics struct {
	sync.Mutex
	failures map[resourceIdentifier]*upsertFailure
	oldest   *int64

	retries                *prometheus.HistogramVec
	retriesTotal           prometheus.Gauge
	errors                 *prometheus.CounterVec
	oldestFailureTimestamp prometheus.Gauge
	oldestFailureAge       *prometheus.Desc
}

var retriesBuckets = []float64{1, 2, 3, 5, 10, 50}

func newMetrics(registry prometheus.Registerer) *metrics {
	m := &metrics{
		failures: make(map[resourceIdentifier]*upsertFailure),
		oldest:   nil,

		retries: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "kubernetes_scanner",
				Name:      "backend_retries",
				Help:      "Number of retries until resources were upserted successfully, partitioned by their last failure code",
				Buckets:   retriesBuckets,
			},
			[]string{"code"},
		),
		retriesTotal: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: "kubernetes_scanner",
				Name:      "unreconciled_resources_total",
				Help:      "The number of unreconciled resources that are still in backoff-retry loops",
			},
		),
		errors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "kubernetes_scanner",
				Name:      "backend_errors_total",
				Help:      "Number of errors sending resources to the backend",
			},
			[]string{"code"},
		),
		oldestFailureTimestamp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "kubernetes_scanner",
			Name:      "backend_oldest_failure",
			Help:      "A timestamp of when the oldest unreconciled resource first failed reconciliation",
		}),
		oldestFailureAge: prometheus.NewDesc(
			"kubernetes_scanner_backend_oldest_failure_age_seconds",
			"Age of the first failed reconciliation of the oldest unreconciled resource in seconds",
			nil, nil,
		),
	}

	registry.MustRegister(m)
	m.oldestFailureTimestamp.Set(0)

	return m
}

func (m *metrics) recordFailure(ctx context.Context, code int, obj client.Object, deletedAt *metav1.Time) {
	log := log.FromContext(ctx)

	if deletedAt != nil {
		// the resource is being deleted, so do not add it to the failure map as it will not be
		// reconciled anymore and thus could never succeed and would be dangling in our failure map.
		// We're just re-uisng recordSuccess for this as recordSuccess does all the required
		// cleanup.
		m.recordSuccess(ctx, obj)
		log.Info("removed resource from failure metrics as it is being deleted")
		return
	}

	m.errors.With(prometheus.Labels{"code": strconv.Itoa(code)}).Inc()

	m.Lock()
	defer m.Unlock()

	resID := newResourceID(obj)
	if f, ok := m.failures[resID]; ok {
		f.retries++
		f.code = code
		return
	}

	now := now().Unix()
	m.failures[resID] = &upsertFailure{
		code:    code,
		added:   &now,
		retries: 1,
	}
	m.retriesTotal.Inc()
	if m.oldest == nil {
		m.oldestFailureTimestamp.Set(float64(now))
		m.oldest = &now
		log.Info("set oldest failure")
	}
}

func (m *metrics) recordSuccess(ctx context.Context, obj client.Object) {
	m.Lock()
	defer m.Unlock()

	resID := newResourceID(obj)
	fail, ok := m.failures[resID]
	if !ok {
		return
	}

	m.retries.With(prometheus.Labels{"code": strconv.Itoa(fail.code)}).Observe(fail.retries)

	delete(m.failures, resID)
	m.retriesTotal.Dec()

	if fail.added != m.oldest {
		return
	}

	// if we're deleting the oldest element, replace it with the new oldest element.
	m.oldest = nil

	var newID resourceIdentifier
	for id, newFail := range m.failures {
		if m.oldest == nil || *m.oldest > *newFail.added {
			m.oldest = newFail.added
			newID = id
		}
	}

	if m.oldest == nil {
		m.oldestFailureTimestamp.Set(0)
		log.FromContext(ctx).Info("removed oldest failure, no new ones")
	} else {
		m.oldestFailureTimestamp.Set(float64(*m.oldest))
		log.FromContext(ctx).Info("replaced oldest failure", "new_oldest_failure", newID)
	}
}

type upsertFailure struct {
	retries float64
	code    int
	added   *int64
}

func (m *metrics) Collect(ch chan<- prometheus.Metric) {
	age := float64(0)
	if m.oldest != nil {
		age = float64(now().Unix() - *m.oldest)
	}

	ch <- prometheus.MustNewConstMetric(m.oldestFailureAge, prometheus.GaugeValue, age)

	m.oldestFailureTimestamp.Collect(ch)
	m.retriesTotal.Collect(ch)
	m.retries.Collect(ch)
	m.errors.Collect(ch)
}

func (m *metrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- m.oldestFailureAge

	m.oldestFailureTimestamp.Describe(ch)
	m.retriesTotal.Describe(ch)
	m.retries.Describe(ch)
	m.errors.Describe(ch)
}
