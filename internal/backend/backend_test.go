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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	promclient "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/snyk/kubernetes-scanner/internal/config"
)

var timeNow time.Time

func init() {
	var err error
	timeNow, err = time.Parse(time.RFC3339, "2023-02-20T16:41:17Z")
	if err != nil {
		panic("could not parse time!")
	}

	now = func() time.Time {
		return timeNow
	}
}

const testToken = "my-super-secret-token"

func TestBackend(t *testing.T) {
	const orgID = "org-123"
	ctx := context.Background()
	tu := testUpstream{t: t, preferredVersion: "v1", orgID: orgID, auth: testToken}
	ts := httptest.NewServer(&tu)
	defer ts.Close()

	b := New("my-pet-cluster", &config.Egress{
		HTTPClientTimeout:       metav1.Duration{Duration: 1 * time.Second},
		SnykAPIBaseURL:          ts.URL,
		SnykServiceAccountToken: testToken,
	}, prometheus.NewPedanticRegistry())
	err := b.Upsert(ctx, "req-id", orgID, []Resource{
		{pod, "v1", metav1.Time{Time: now()}, nil},
	})
	require.NoError(t, err)

	tu.expectDeletion = true
	err = b.Upsert(ctx, "req-id", orgID, []Resource{
		{pod, "v1", metav1.Time{Time: now()}, &metav1.Time{Time: now().Local()}},
	})
	require.NoError(t, err)

}

func TestBackendErrorHandling(t *testing.T) {
	const orgID = "org-123"
	ctx := context.Background()
	tu := testUpstream{t: t, preferredVersion: "v1", orgID: orgID, auth: testToken, statusCodeToReturn: 400}
	ts := httptest.NewServer(&tu)
	defer ts.Close()

	b := New("my-pet-cluster", &config.Egress{
		HTTPClientTimeout:       metav1.Duration{Duration: 1 * time.Second},
		SnykAPIBaseURL:          ts.URL,
		SnykServiceAccountToken: testToken,
	}, prometheus.NewPedanticRegistry())

	err := b.Upsert(ctx, "req-id", orgID, []Resource{{pod, "v1", metav1.Time{Time: now()}, nil}})
	require.Error(t, err)
	var h *HTTPError
	require.ErrorAs(t, err, &h)
	require.Equal(t, 400, h.StatusCode)
}

func TestMetricsFromBackend(t *testing.T) {
	const orgID = "org-123"
	ctx := context.Background()
	tu := testUpstream{t: t, preferredVersion: "v1", orgID: orgID, auth: testToken, statusCodeToReturn: 400}
	ts := httptest.NewServer(&tu)
	defer ts.Close()

	b := New("my-pet-cluster", &config.Egress{
		HTTPClientTimeout:       metav1.Duration{Duration: 1 * time.Second},
		SnykAPIBaseURL:          ts.URL,
		SnykServiceAccountToken: testToken,
	}, prometheus.NewPedanticRegistry())

	err := b.Upsert(ctx, "req-id", orgID, []Resource{{pod, "v1", metav1.Time{Time: now()}, nil}})
	require.Error(t, err)
	require.Equal(t, float64(1), b.failures[newResourceID(pod)].retries)
	require.Equal(t, 400, b.failures[newResourceID(pod)].code)

	tu.statusCodeToReturn = 0
	err = b.Upsert(ctx, "req-id", orgID, []Resource{{pod, "v1", metav1.Time{Time: now()}, nil}})
	require.NoError(t, err)
	_, ok := b.failures[newResourceID(pod)]
	require.False(t, ok)
}

func TestSanityBackend(t *testing.T) {
	const orgID = "org-123"
	ctx := context.Background()
	tu := testUpstream{t: t, preferredVersion: "v1", orgID: orgID, auth: testToken}
	ts := httptest.NewServer(&tu)
	defer ts.Close()

	b := New("my-pet-cluster", &config.Egress{
		HTTPClientTimeout:       metav1.Duration{Duration: 1 * time.Second},
		SnykAPIBaseURL:          ts.URL,
		SnykServiceAccountToken: testToken,
	}, prometheus.NewPedanticRegistry())

	require.NoError(t, b.SanityCheck(ctx, orgID))
	require.Error(t, b.SanityCheck(ctx, "org-456"))
}

type testUpstream struct {
	t                  *testing.T
	preferredVersion   string
	orgID              string
	expectDeletion     bool
	auth               string
	statusCodeToReturn int
}

func (tu *testUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "token "+tu.auth {
		http.Error(w, fmt.Sprintf("invalid authorization header provided: %v", r.Header.Get("Authorization")), 403)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/hidden/orgs/%s/kubernetes_resources", tu.orgID), tu.handleKubernetesResources)
	mux.HandleFunc(fmt.Sprintf("/rest/orgs/%s", tu.orgID), tu.handleOrg)
	mux.ServeHTTP(w, r)
}

func (tu *testUpstream) handleKubernetesResources(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		http.Error(w, fmt.Sprintf("could not read body: %v", err), 400)
		return
	}

	req := request{}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, fmt.Sprintf("could not unmarshal body to JSON: %v", err), 400)
		return
	}

	if tu.statusCodeToReturn != 0 {
		http.Error(w, "an error occurred", tu.statusCodeToReturn)
		return
	}

	expected := []Resource{{
		ManifestBlob:     pod,
		PreferredVersion: tu.preferredVersion,
		// when unmarshaling from JSON, metav1.Time also calls Local(), so we need to do too.
		ScannedAt: metav1.Time{Time: now().Local()},
	}}

	if tu.expectDeletion {
		expected[0].DeletedAt = &metav1.Time{Time: now().Local()}
	}

	require.Equal(tu.t, expected, req.Data.Attributes.Resources)
}

func (tu *testUpstream) handleOrg(w http.ResponseWriter, r *http.Request) {
}

var pod = &corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "normal-pod",
		Namespace: "default",
		UID:       types.UID("21E430E3-FA43-45B9-B5EC-AE27FFA16D82"),
	},
	TypeMeta: metav1.TypeMeta{
		Kind:       "Pod",
		APIVersion: "v1",
	},
	Spec: corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "bla",
			Image: "bla:latest",
		}},
	},
}

// UnmarshalJSON unmarshals the resource type. This needs a custom unmarshal function because the
// resource type contains a `client.Object` interface, which cannot be unmarshalled automatically.
func (r *Resource) UnmarshalJSON(data []byte) error {
	// create a temporary type to avoid recursive calls to this method.
	type s Resource
	tmp := s{
		// we simply set the manifestBlob to an unstructured object, which satisfies the
		// `client.Object` interface and allows us to extract the apiVersion & kind from it.
		ManifestBlob: &unstructured.Unstructured{},
	}
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}

	// figure out the actual type of the manifestBlob and add it to tmp so that we can do another
	// round of json unmarshaling.
	apiVersion, kind := tmp.ManifestBlob.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
	switch {
	case kind == "Pod" && apiVersion == "v1":
		tmp.ManifestBlob = &corev1.Pod{}
	default:
		return fmt.Errorf("unknown APIVersion / Kind %v in %s", tmp, data)
	}

	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}

	// the GVK is lost when unmarshalling / decoding objects, so we need to add it back.
	// https://github.com/kubernetes/client-go/issues/541#issuecomment-452312901
	tmp.ManifestBlob.GetObjectKind().SetGroupVersionKind(tmp.ManifestBlob.GetObjectKind().GroupVersionKind())
	*r = Resource(tmp)
	return nil
}

func TestJSONMatches(t *testing.T) {
	b := New("my pet cluster", &config.Egress{}, prometheus.NewPedanticRegistry())
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "a-pod",
			Namespace: "default",
			UID:       types.UID("ADD9E4F4-5154-4261-81FD-F14A4358F772"),
		},
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
	}
	r, err := b.newPostBody([]Resource{{pod, "v1", metav1.Time{Time: now()}, nil}})
	require.NoError(t, err)

	body, err := io.ReadAll(r)
	require.NoError(t, err)

	var prettyJSON bytes.Buffer
	err = json.Indent(&prettyJSON, body, "", "\t")
	require.NoError(t, err)

	const expectedJSON = `{
	"data": {
		"type": "kubernetesresource",
		"attributes": {
			"cluster_name": "my pet cluster",
			"resources": [
				{
					"manifest_blob": {
						"kind": "Pod",
						"apiVersion": "v1",
						"metadata": {
							"name": "a-pod",
							"namespace": "default",
							"uid": "ADD9E4F4-5154-4261-81FD-F14A4358F772",
							"creationTimestamp": null
						},
						"spec": {
							"containers": null
						},
						"status": {}
					},
					"preferred_version": "v1",
					"scanned_at": "2023-02-20T16:41:17Z"
				}
			]
		}
	}
}`
	require.Equal(t, expectedJSON, prettyJSON.String())
}

func newResource(id string) client.Object {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      id,
			Namespace: id,
			UID:       types.UID(id),
		},
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
	}
}

func TestMetricsRetries(t *testing.T) {
	var (
		ctx          = context.Background()
		testFailures = map[client.Object]int{
			newResource("a"): 1, newResource("b"): 6, newResource("c"): 5,
			newResource("d"): 2, newResource("e"): 1, newResource("f"): 4,
			newResource("g"): 3, newResource("h"): 3, newResource("i"): 1,
			newResource("j"): 40, newResource("k"): 0, newResource("l"): 8,
		}
		// see the retriesBuckets var for the bucket values.
		// the actual values have been "calculated" manually.
		expectedBucketSizes = []uint64{3, 4, 6, 8, 10, 11}
		m, registry         = newMetricsTest()
		failures            = make(chan client.Object)
		wg                  sync.WaitGroup
	)
	const metricName = "kubernetes_scanner_backend_retries"

	// we spawn some goroutines to recordFailures so that we can
	// make sure they're thread-safe.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				obj, ok := <-failures
				if !ok {
					return
				}
				m.recordFailure(ctx, 404, obj, nil)
			}
		}()
	}

	// send all the required failures into the channels
	var allFailuresDone bool
	for !allFailuresDone {
		allFailuresDone = true
		for obj, numFailures := range testFailures {
			if numFailures == 0 {
				continue
			}
			failures <- obj
			testFailures[obj]--
			allFailuresDone = false

		}
	}

	close(failures)
	wg.Wait()

	requireGauge(t, registry,
		"kubernetes_scanner_unreconciled_resources_total",
		// we have one resource that has 0 failures, meaning it should
		// never show up in that unreconciled_resources_total metric.
		float64(len(testFailures)-1),
	)

	// now mark all requests as successful. We can't do that above
	// because we need to make sure that these calls come strictly
	// after m.recordFailure calls to get the right count. However
	// because we still want to test that calls to m.recordSuccess
	// are also thread-safe, we spawn a goroutine for each call as
	// well.
	wg.Add(len(testFailures))
	for obj := range testFailures {
		go func(obj client.Object) {
			defer wg.Done()
			m.recordSuccess(ctx, obj)
		}(obj)
	}
	wg.Wait()

	requireHistogram(t, registry, metricName, expectedBucketSizes)
}

func TestMetricsOldest(t *testing.T) {
	const metricName = "kubernetes_scanner_backend_oldest_failure"

	ctx := context.Background()
	t.Run("cleanup with no other failures", func(t *testing.T) {
		m, registry := newMetricsTest()

		res := newResource("x")

		m.recordFailure(ctx, 403, res, nil)
		requireGauge(t, registry, metricName, float64(*m.failures[newResourceID(res)].added))

		m.recordSuccess(ctx, res)
		requireGauge(t, registry, metricName, 0)
	})

	t.Run("cleanup with replacement", func(t *testing.T) {
		m, registry := newMetricsTest()
		resA := newResource("a")
		resB := newResource("b")

		m.recordFailure(ctx, 403, resA, nil)
		requireGauge(t, registry, metricName, float64(*m.failures[newResourceID(resA)].added))

		m.recordFailure(ctx, 403, resB, nil)
		requireGauge(t, registry, metricName, float64(*m.failures[newResourceID(resA)].added))

		m.recordSuccess(ctx, resA)
		requireGauge(t, registry, metricName, float64(*m.failures[newResourceID(resB)].added))
		m.recordSuccess(ctx, resB)
	})

	const ageMetricName = "kubernetes_scanner_backend_oldest_failure_age_seconds"
	t.Run("test age", func(t *testing.T) {
		m, registry := newMetricsTest()

		res := newResource("a")

		m.recordFailure(ctx, 403, res, nil)
		requireGauge(t, registry, ageMetricName, 0)
		// fast-forward time.
		now = func() time.Time {
			return timeNow.Add(50 * time.Second)
		}
		requireGauge(t, registry, ageMetricName, 50)

		m.recordSuccess(ctx, res)
		requireGauge(t, registry, ageMetricName, 0)
	})
}

func TestMetricsErrors(t *testing.T) {
	m, registry := newMetricsTest()
	m.recordFailure(context.Background(), 500, newResource("a-uid"), nil)
	requireCounter(t, registry, "kubernetes_scanner_backend_errors_total", 1.0)
}

func TestMetricsOnDeletedResource(t *testing.T) {
	ctx := context.Background()
	m, registry := newMetricsTest()
	res := newResource("some-pod")
	m.recordFailure(ctx, 404, res, nil)
	requireGauge(t, registry, "kubernetes_scanner_unreconciled_resources_total", 1)
	requireCounter(t, registry, "kubernetes_scanner_backend_errors_total", 1)
	// we don't check for the backend_retries metric because without any values, it will be absent.

	m.recordFailure(ctx, 404, res, nil)
	requireGauge(t, registry, "kubernetes_scanner_unreconciled_resources_total", 1)
	requireCounter(t, registry, "kubernetes_scanner_backend_errors_total", 2)

	m.recordFailure(ctx, 404, res, &metav1.Time{Time: time.Now()})
	requireGauge(t, registry, "kubernetes_scanner_unreconciled_resources_total", 0)
	requireCounter(t, registry, "kubernetes_scanner_backend_errors_total", 2)
	requireHistogram(t, registry, "kubernetes_scanner_backend_retries", []uint64{0, 1, 1, 1, 1, 1})
}

func newMetricsTest() (*metrics, *prometheus.Registry) {
	registry := prometheus.NewPedanticRegistry()
	m := newMetrics(registry)
	return m, registry
}

// requireMetrics requires the given metric to be present in the registry and pass the requireFn.
func requireMetric(t *testing.T, registry prometheus.Gatherer, metricName string,
	requireFn func(*testing.T, *promclient.Metric),
) {
	t.Helper()
	metrics, err := registry.Gather()
	require.NoError(t, err)

	for _, m := range metrics {
		if *m.Name == metricName {
			requireFn(t, m.GetMetric()[0])
			return
		}
	}
	t.Fatalf("metric %v is not present in registry", metricName)
}

// requireGauge requires the given metric to be present in the registry with the expected value.
func requireGauge(t *testing.T, registry prometheus.Gatherer, metricName string, expected float64) {
	t.Helper()
	requireMetric(t, registry, metricName, func(t *testing.T, metric *promclient.Metric) {
		require.Equal(t, expected, metric.Gauge.GetValue())
	})
}

func requireCounter(t *testing.T, registry prometheus.Gatherer, metricName string, expected float64) {
	t.Helper()
	requireMetric(t, registry, metricName, func(t *testing.T, metric *promclient.Metric) {
		require.Equal(t, expected, metric.Counter.GetValue())
	})
}

func requireHistogram(t *testing.T, registry prometheus.Gatherer, metricName string, expectedValues []uint64) {
	t.Helper()
	requireMetric(t, registry, metricName, func(t *testing.T, metric *promclient.Metric) {
		for i, bucket := range metric.Histogram.Bucket {
			require.Equal(t, expectedValues[i], *bucket.CumulativeCount)
		}
	})
}
