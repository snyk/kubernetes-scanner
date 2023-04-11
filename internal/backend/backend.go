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
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func (b *Backend) Upsert(ctx context.Context, obj client.Object, preferredVersion string, orgID string, deletedAt *metav1.Time) error {
	body, err := b.newPostBody(obj, preferredVersion, deletedAt)
	if err != nil {
		return fmt.Errorf("could not construct request body: %w", err)
	}

	endpoint := fmt.Sprintf("%s/hidden/orgs/%s/kubernetes_resources?version=2023-02-20~experimental",
		b.apiEndpoint, orgID)

	req, err := http.NewRequest(http.MethodPost, endpoint, body)
	if err != nil {
		return fmt.Errorf("could not construct request: %w", err)
	}
	req.Header.Add("Content-Type", contentTypeJSON)
	req.Header.Add("Authorization", "token "+b.authorizationKey)

	resp, err := b.client.Do(req.WithContext(ctx))
	if err != nil {
		b.recordFailure(ctx, 0, obj.GetUID())
		return fmt.Errorf("could not post resource: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 || resp.StatusCode < 200 {
		body, err := io.ReadAll(resp.Body)
		b.recordFailure(ctx, resp.StatusCode, obj.GetUID())
		if err != nil {
			return fmt.Errorf("got non-20x HTTP code %v and could not read body: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("got non-20x exit code %v with body %s", resp.StatusCode, body)
	}

	b.recordSuccess(ctx, obj.GetUID())
	return nil
}

// for testing.
var now = time.Now

func (b *Backend) newPostBody(obj client.Object, preferredVersion string, deletedAt *metav1.Time) (io.Reader, error) {
	r := &request{
		Data: requestData{
			Type: "kubernetesresource",
			Attributes: requestAttributes{
				ClusterName: b.clusterName,
				Resources: []resource{{
					ScannedAt:        metav1.Time{Time: now()},
					ManifestBlob:     obj,
					PreferredVersion: preferredVersion,
					DeletedAt:        deletedAt,
				}},
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
	Resources   []resource `json:"resources"`
}

type resource struct {
	ManifestBlob     client.Object `json:"manifest_blob"`
	PreferredVersion string        `json:"preferred_version"`
	ScannedAt        metav1.Time   `json:"scanned_at"`
	DeletedAt        *metav1.Time  `json:"deleted_at,omitempty"`
}

type metrics struct {
	sync.Mutex
	failures map[types.UID]*upsertFailure
	oldest   *int64

	retries                *prometheus.HistogramVec
	oldestFailureTimestamp prometheus.Gauge
}

var retriesBuckets = []float64{1, 2, 3, 5, 10, 50}

func newMetrics(registry prometheus.Registerer) *metrics {
	m := &metrics{
		failures: make(map[types.UID]*upsertFailure),
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
		oldestFailureTimestamp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "kubernetes_scanner",
			Name:      "backend_oldest_failure",
			Help:      "A timestamp of when the oldest unreconciled resource first failed reconciliation",
		}),
	}
	registry.MustRegister(m.oldestFailureTimestamp, m.retries)

	// we need to set an initial value so that it is not 0.
	m.oldestFailureTimestamp.Set(math.Inf(0))

	return m
}

func (m *metrics) recordFailure(ctx context.Context, code int, uid types.UID) {
	m.Lock()
	defer m.Unlock()

	if f, ok := m.failures[uid]; ok {
		f.retries++
		f.code = code
	} else {
		now := time.Now().Unix()
		fail := &upsertFailure{
			code:    code,
			added:   &now,
			retries: 1,
		}
		m.failures[uid] = fail
		if m.oldest == nil {
			m.oldestFailureTimestamp.Set(float64(now))
			m.oldest = &now
			log.FromContext(ctx).Info("set oldest failure", "new", uid)
		}
	}
}

func (m *metrics) recordSuccess(ctx context.Context, uid types.UID) {
	m.Lock()
	defer m.Unlock()

	fail, ok := m.failures[uid]
	if !ok {
		return
	}

	m.retries.With(prometheus.Labels{
		"code": strconv.Itoa(fail.code),
	}).Observe(float64(fail.retries))

	delete(m.failures, uid)

	// if we're deleting the oldest element, replace it with the new oldest element.
	if m.oldest == fail.added {
		m.oldest = nil

		var newUID types.UID
		for uid, newFail := range m.failures {
			if m.oldest == nil || *m.oldest > *newFail.added {
				m.oldest = newFail.added
				newUID = uid
			}
		}

		if m.oldest == nil {
			m.oldestFailureTimestamp.Set(math.Inf(0))
			log.FromContext(ctx).Info("removed oldest failure, no new ones", "old", uid)
		} else {
			m.oldestFailureTimestamp.Set(float64(*m.oldest))
			log.FromContext(ctx).Info("replaced oldest failure", "old", uid, "new", newUID)
		}
	}
}

type upsertFailure struct {
	retries uint8
	code    int
	added   *int64
}
