package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/snyk/kubernetes-scanner/internal/config"
)

type Backend struct {
	apiEndpoint string
	clusterName string

	client *http.Client
}

func New(clusterName string, cfg *config.Egress) *Backend {
	return &Backend{
		apiEndpoint: cfg.SnykAPIBaseURL,
		clusterName: clusterName,

		client: &http.Client{
			// the default transport automatically honors HTTP_PROXY settings.
			Transport: http.DefaultTransport,
			Timeout:   cfg.HTTPClientTimeout.Duration,
		},
	}
}

const contentTypeJSON = "application/vnd.api+json"

func (b *Backend) Upsert(ctx context.Context, obj client.Object, orgID string, deletedAt *metav1.Time) error {
	body, err := b.newPostBody(obj, deletedAt)
	if err != nil {
		return fmt.Errorf("could not construct request body: %w", err)
	}

	endpoint := fmt.Sprintf("%s/rest/orgs/%s/kubernetesresources?version=2023-02-20~experimental",
		b.apiEndpoint, orgID)

	req, err := http.NewRequest(http.MethodPost, endpoint, body)
	if err != nil {
		return fmt.Errorf("could not construct request: %w", err)
	}
	req.Header.Add("Content-Type", contentTypeJSON)

	resp, err := b.client.Do(req.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("could not post resource: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 || resp.StatusCode < 200 {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("got non-zero exit code %v and could not read body: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("got non-zero exit code %v with body %s", resp.StatusCode, body)
	}

	return nil
}

// for testing.
var now = time.Now

func (b *Backend) newPostBody(obj client.Object, deletedAt *metav1.Time) (io.Reader, error) {
	r := &request{
		Data: requestData{
			Type: "kubernetesresource",
			Attributes: requestAttributes{
				ClusterName: b.clusterName,
				Resources: []resource{{
					ScannedAt:    metav1.Time{Time: now()},
					ManifestBlob: obj,
					DeletedAt:    deletedAt,
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
	ManifestBlob client.Object `json:"manifest_blob"`
	ScannedAt    metav1.Time   `json:"scanned_at"`
	DeletedAt    *metav1.Time  `json:"deleted_at,omitempty"`
}
