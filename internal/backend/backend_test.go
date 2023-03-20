package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

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
	tu := testUpstream{orgID: orgID, auth: testToken}
	ts := httptest.NewServer(http.HandlerFunc(tu.Handle))
	defer ts.Close()

	b := New("my-pet-cluster", &config.Egress{
		HTTPClientTimeout:       metav1.Duration{Duration: 1 * time.Second},
		SnykAPIBaseURL:          ts.URL,
		SnykServiceAccountToken: testToken,
	})
	err := b.Upsert(ctx, pod, orgID, nil)
	require.NoError(t, err)

	tu.deletion = true
	err = b.Upsert(ctx, pod, orgID, &metav1.Time{Time: now().Local()})
	require.NoError(t, err)

	// make sure that our error handling also works; our upstream currently expects a
	// delete-request, but we're sending a zero-time, so it will return an error. That error should
	// be passed through to the caller of Upsert.
	err = b.Upsert(ctx, pod, orgID, nil)
	require.Error(t, err)
}

type testUpstream struct {
	orgID    string
	deletion bool
	auth     string
}

func (tu *testUpstream) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "token "+tu.auth {
		http.Error(w, fmt.Sprintf("invalid authorization header provided: %v", r.Header.Get("Authorization")), 403)
		return
	}
	matches, err := path.Match(fmt.Sprintf("*/rest/orgs/%s/kubernetesresources", tu.orgID), r.URL.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid path, could not match: %v", err), 400)
		return
	}
	if !matches {
		http.Error(w, fmt.Sprintf("path does not match expectations: %v", r.URL.Path), 400)
		return
	}

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

	expected := []resource{{
		ManifestBlob: pod,
		// when unmarshaling from JSON, metav1.Time also calls Local(), so we need to do too.
		ScannedAt: metav1.Time{Time: now().Local()},
	}}

	if tu.deletion {
		expected[0].DeletedAt = &metav1.Time{Time: now().Local()}
	}

	if !assert.ObjectsAreEqual(expected, req.Data.Attributes.Resources) {
		http.Error(w,
			fmt.Sprintf("objects are not equal. expected=%+v, got=%+v", expected, req.Data.Attributes.Resources),
			400)
		return
	}
}

var pod = &corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "normal-pod",
		Namespace: "default",
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
func (r *resource) UnmarshalJSON(data []byte) error {
	// create a temporary type to avoid recursive calls to this method.
	type s resource
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
	*r = resource(tmp)
	return nil
}

func TestJSONMatches(t *testing.T) {
	b := New("my pet cluster", &config.Egress{})
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
	r, err := b.newPostBody(pod, nil)
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
					"scanned_at": "2023-02-20T16:41:17Z"
				}
			]
		}
	}
}`
	require.Equal(t, expectedJSON, prettyJSON.String())
}
