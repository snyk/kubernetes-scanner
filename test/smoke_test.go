package test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/snyk/kubernetes-scanner/internal/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	k8sconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
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
	// Logger must be set or else the controller-runtime framework will
	// complain.
	ctrl.SetLogger(zap.New())

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

	// The backend no longer offers a way to read resources back, so we can't compare what the
	// scanner pushed with the local nodes. Instead we check that the scanner has scanned the
	// single node we expect in our test env and that it isn't reporting any failures to push it.
	// TODO: the tests currently only cover that one node. We probably want to extend this.
	if err := eventually(func() error {
		nl := &corev1.NodeList{}
		if err := c.List(ctx, nl); err != nil {
			return fmt.Errorf("error listing nodes: %w", err)
		}
		if len(nl.Items) != 1 {
			return fmt.Errorf("unexpected amount of nodes in CI env: %v", len(nl.Items))
		}
		return nil
	}, time.Minute, 3*time.Second); err != nil {
		return fmt.Errorf("error listing nodes: %w", err)
	}

	if err := assertNoBackendErrors(ctx, restCfg, c); err != nil {
		return fmt.Errorf("scanner reported errors sending resources to the backend: %w", err)
	}

	return nil
}

// assertNoBackendErrors scrapes the scanner's metrics and fails if it has recorded any error while
// sending resources to the backend, or still has unreconciled resources. A resource that the
// backend rejected would show up in one of the two. It needs to see a quiet scanner for a while, so
// that it doesn't pass before the scanner had a chance to push anything.
func assertNoBackendErrors(ctx context.Context, restCfg *rest.Config, c client.Client) error {
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("could not create clientset: %w", err)
	}

	var quietScrapes int
	return eventually(func() error {
		pl := &corev1.PodList{}
		if err := c.List(ctx, pl, &client.ListOptions{Namespace: scannerNamespace}); err != nil {
			return fmt.Errorf("error listing pods: %w", err)
		}
		if len(pl.Items) != 1 {
			return fmt.Errorf("expected 1 scanner pod, but got %v", len(pl.Items))
		}

		raw, err := clientset.CoreV1().Pods(scannerNamespace).
			ProxyGet("http", pl.Items[0].Name, "8080", "metrics", nil).DoRaw(ctx)
		if err != nil {
			quietScrapes = 0
			return fmt.Errorf("could not scrape scanner metrics: %w", err)
		}

		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "kubernetes_scanner_backend_errors_total{") {
				return fmt.Errorf("unexpected metric: %s", line)
			}
			if strings.HasPrefix(line, "kubernetes_scanner_unreconciled_resources_total ") &&
				!strings.HasSuffix(line, " 0") {
				return fmt.Errorf("unexpected metric: %s", line)
			}
		}

		quietScrapes++
		if quietScrapes < 10 {
			return fmt.Errorf("not enough quiet scrapes yet: %v", quietScrapes)
		}
		return nil
	}, 2*time.Minute, 3*time.Second)
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
