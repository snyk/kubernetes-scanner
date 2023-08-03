package test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/snyk/kubernetes-scanner/internal/backend"
	"github.com/snyk/kubernetes-scanner/internal/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	k8sconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

const scannerNamespace = "kubernetes-scanner"

// Smoke runs the scanner's smoke test; deploying the scanner into a Tilt environment and ensuring
// that the backend has received the resources.
// The Smoke tests has a couple of prerequisites:
// - the kubectl context needs to point to a running cluster (where tilt won't block on)
// - tilt, kubectl and helm need to be installed.
func TestSmoke(t *testing.T) {
	if os.Getenv("TEST_SMOKE") == "" {
		t.Skip("not running smoke tests as env var TEST_SMOKE isn't set.")
	}

	ctx := context.Background()
	values, valuesFile, err := newHelmValues()
	if err != nil {
		t.Fatalf("could not create Helm values: %v", err)
	}

	if err := tiltCI(ctx,
		"--routes_values_yaml="+valuesFile,
		"--snyk_service_account_token="+values.Config.Egress.SnykServiceAccountToken,
	); err != nil {
		t.Fatalf("could not setup tilt environment: %v", err)
	}

	defer func() {
		if err := tiltDown(ctx,
			"--routes_values_yaml="+valuesFile,
			"--snyk_service_account_token="+values.Config.Egress.SnykServiceAccountToken,
		); err != nil {
			t.Errorf("error tearing down Tilt environment: %v", err)
		}
	}()

	if err := runTests(ctx, values.Config); err != nil {
		t.Fatalf("test error: %v", err)
	}
}

func tiltCI(ctx context.Context, tiltfileArgs ...string) error {
	return runTilt(ctx, "ci", append([]string{
		// Even if we're on a debug build, don't start a debug webserver
		"--web-mode=prod",
		//  to separate the Tilt cmd args to the tiltfile args.
		"--",
	}, tiltfileArgs...)...)
}

func tiltDown(ctx context.Context, tiltfileArgs ...string) error {
	return runTilt(ctx, "down", append([]string{"--delete-namespaces", "--"}, tiltfileArgs...)...)
}

func runTilt(ctx context.Context, tiltCmd string, args ...string) error {
	cmd := exec.CommandContext(ctx, "tilt", append([]string{
		tiltCmd,
		// Debug logging for integration tests
		"--debug",
		"--klog=1",
	}, args...)...,
	)
	// TODO: should we use something else?
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	return cmd.Run()
}

func runTests(ctx context.Context, cfg config.Config) error {
	restCfg, err := k8sconfig.GetConfig()
	if err != nil {
		return fmt.Errorf("could not get REST config: %w", err)
	}

	c, err := client.New(restCfg, client.Options{})
	if err != nil {
		return fmt.Errorf("could not create kube client: %w", err)
	}

	if err := waitForDeployment(ctx, c); err != nil {
		return fmt.Errorf("error waiting for deployment to be up: %w", err)
	}

	// TODO: the tests currently check whether the single node we expect in our test env has synced
	// to the backend. This isn't much, and we probably want to extend this. Also, the code could be
	// cleaned up a bit as well.
	nl := &corev1.NodeList{}
	if err := c.List(ctx, nl); err != nil {
		return fmt.Errorf("error listing nodes: %w", err)
	}

	b := backend.New(cfg.ClusterName, cfg.Egress, prometheus.NewPedanticRegistry())
	orgID := cfg.Routes[0].OrganizationID

	if err := eventually(func() error {
		resp, err := b.List(ctx, orgID)
		if err != nil {
			return fmt.Errorf("could not execute list request: %w", err)
		}
		var nodesFromBackend []*corev1.Node
		for _, data := range resp {
			if data.Attributes.ClusterName != cfg.ClusterName {
				continue
			}

			unstructuredObj := &unstructured.Unstructured{Object: data.Attributes.ManifestBlob}
			if unstructuredObj.GetKind() == "Node" {
				node := &corev1.Node{}
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(data.Attributes.ManifestBlob, node); err != nil {
					return fmt.Errorf("error converting unstructured resource: %w", err)
				}
				nodesFromBackend = append(nodesFromBackend, node)
			}
		}

		if len(nodesFromBackend) != 1 {
			return fmt.Errorf("got wrong amount of nodes from backend: %v", len(nodesFromBackend))
		}

		return assertNodesEqual(&nl.Items[0], nodesFromBackend[0])
	}, time.Minute, 3*time.Second); err != nil {
		return fmt.Errorf("error waiting for backend to receive node: %w", err)
	}

	return nil
}

func waitForDeployment(ctx context.Context, c client.Client) error {
	var continuousUptime int
	return eventually(func() error {
		dl := &appsv1.DeploymentList{}
		if err := c.List(ctx, dl, &client.ListOptions{Namespace: scannerNamespace}); err != nil {
			return fmt.Errorf("error listing deployments: %w", err)
		}
		if len(dl.Items) != 1 {
			continuousUptime = 0
			return fmt.Errorf("expected 1 deployment, but got %v", len(dl.Items))
		}
		deploy := dl.Items[0]
		if deploy.Status.AvailableReplicas < 1 {
			continuousUptime = 0
			return fmt.Errorf("no replicas available")
		}
		continuousUptime++
		if continuousUptime > 10 {
			return nil

		}
		return fmt.Errorf("not enough continuous uptime yet: %v", continuousUptime)
	}, 2*time.Minute, time.Second)
}

func eventually(fn func() error, timeout time.Duration, tick time.Duration) error {
	after := time.After(timeout)
	ticker := time.NewTicker(tick)

	var err error
	if err = fn(); err == nil {
		return nil
	}
	for {
		select {
		case <-after:
			return fmt.Errorf("timed out waiting for condition. last error: %w", err)
		case <-ticker.C:
			err = fn()
			if err == nil {
				return nil
			}
		}
	}
}

type helmValues struct {
	Config config.Config `json:"config"`
}

func getSnykAPIBaseURL() string {
	if api := os.Getenv("SNYK_API"); api != "" {
		return api
	}

	return "https://api.dev.snyk.io"
}

func newHelmValues() (vals helmValues, filename string, err error) {
	orgID := os.Getenv("TEST_ORGANIZATION_ID")
	if orgID == "" {
		return helmValues{}, "", fmt.Errorf("no TEST_ORGANIZATION_ID env var set")
	}

	snykSAToken := os.Getenv("TEST_SNYK_SERVICE_ACCOUNT_TOKEN")
	if snykSAToken == "" {
		return helmValues{}, "", fmt.Errorf("TEST_SNYK_SERVICE_ACCOUNT_TOKEN not specified!")
	}

	vals = helmValues{
		Config: config.Config{
			ClusterName: newClusterName(),
			// TODO: test more complex routing?
			Routes: []config.Route{{
				ClusterScopedResources: true,
				Namespaces:             []string{"*"},
				OrganizationID:         orgID,
			}},
			Scanning: config.Scan{
				Types: []config.ScanType{{
					APIGroups: []string{""},
					Versions:  []string{"v1"},
					Resources: []string{"nodes"},
				}},
				RequeueAfter: metav1.Duration{Duration: time.Hour},
			},
			Egress: &config.Egress{
				SnykAPIBaseURL:          getSnykAPIBaseURL(),
				SnykServiceAccountToken: snykSAToken,
			},
		},
	}

	values, err := json.Marshal(vals)
	if err != nil {
		return helmValues{}, "", fmt.Errorf("could not render routing config: %v", err)
	}

	f, err := os.CreateTemp("", "routes_values_*.yaml")
	if err != nil {
		return helmValues{}, "", fmt.Errorf("could not write temporary values.yaml file: %v", err)
	}

	if _, err := f.Write(values); err != nil {
		return helmValues{}, "", fmt.Errorf("could not write routes to temporary file: %v", err)
	}

	f.Close()

	return vals, f.Name(), nil
}

// returns a new random cluster name with a "smoke_test_" prefix.
func newClusterName() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("smoke_test_%x", b)
}

func assertNodesEqual(expected, actual *corev1.Node) error {
	removeManagedFieldsTimestamp(expected.ManagedFields)
	removeManagedFieldsTimestamp(actual.ManagedFields)
	removeNodeConditionsLastHeartbeat(expected.Status.Conditions)
	removeNodeConditionsLastHeartbeat(actual.Status.Conditions)

	expected.ResourceVersion = ""
	actual.ResourceVersion = ""

	// when doing `client.List`, the typed objects will not have a GVK set. Because of that, we'll
	// just remove it from both expceted & actual before comparison.
	expected.SetGroupVersionKind(schema.GroupVersionKind{})
	actual.SetGroupVersionKind(schema.GroupVersionKind{})

	if !reflect.DeepEqual(expected, actual) {
		ex, _ := json.Marshal(expected)
		ac, _ := json.Marshal(actual)
		return fmt.Errorf("nodes are not equal.\n\n\nexpected: %s\n\nactual: %s", ex, ac)
	}

	return nil
}

func removeNodeConditionsLastHeartbeat(ncs []corev1.NodeCondition) {
	for i := range ncs {
		ncs[i].LastHeartbeatTime = metav1.Time{}
	}
}

func removeManagedFieldsTimestamp(managedFields []metav1.ManagedFieldsEntry) {
	for i := range managedFields {
		managedFields[i].Time = nil
	}
}
