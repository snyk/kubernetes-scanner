package config

import (
	"flag"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/snyk/kubernetes-scanner/internal/test"
)

func TestConfigRealAPIServer(t *testing.T) {
	if testing.Short() {
		t.Skip("not spawning API Server in the interest of time")
	}

	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "")
	if err != nil {
		t.Fatalf("could not create temporary file for testing: %v", err)
	}

	const actualConfig = `
metricsAddress: ":8080"
clusterName: dev
organizationID: "some-id"
scanning:
  requeueAfter: 1m
  types:
  - apiGroups: [""]
    resources: 
      - pods
  - apiGroups:
    - "apps"
    versions: ["v1"]
    resources: ["deployments"]
    namespaces: ["default"]
egress:
  httpClientTimeout: 5s
  snykAPIBaseURL: https://app.dev.snyk.io
`

	const testToken = "my-token"
	t.Setenv("SNYK_SERVICE_ACCOUNT_TOKEN", testToken)

	expected := &Config{
		MetricsAddress: ":8080",
		ClusterName:    "dev",
		OrganizationID: "some-id",
		Scanning: Scan{
			RequeueAfter: metav1.Duration{Duration: time.Minute},
			Types: []ScanType{{
				APIGroups: []string{""},
				Versions:  nil,
				Resources: []string{"pods"},
			}, {
				APIGroups:  []string{"apps"},
				Versions:   []string{"v1"},
				Resources:  []string{"deployments"},
				Namespaces: []string{"default"},
			}},
		},
		Egress: &Egress{
			SnykServiceAccountToken: testToken,
			HTTPClientTimeout:       metav1.Duration{Duration: 5 * time.Second},
			SnykAPIBaseURL:          "https://app.dev.snyk.io",
		},
	}
	expectedGVKs := [][]schema.GroupVersionKind{
		{{
			Group:   "",
			Version: "v1",
			Kind:    "Pod",
		}}, {{
			Group:   "apps",
			Version: "v1",
			Kind:    "Deployment",
		}},
	}

	restCfg := test.SetupEnv(t)
	if err := flag.Set("kubeconfig", test.GenerateKubeconfig(t, restCfg)); err != nil {
		t.Fatalf("could not set kubeconfig flag: %v", err)
	}
	flag.Parse()

	if _, err := f.Write([]byte(actualConfig)); err != nil {
		t.Fatalf("error writing config file: %v", err)
	}

	cfg, err := Read(f.Name())
	if err != nil {
		t.Fatalf("could not read config: %v", err)
	}

	require.Equal(t, expected.Scanning, cfg.Scanning)
	require.Equal(t, expected.MetricsAddress, cfg.MetricsAddress)
	require.Equal(t, expected.ProbeAddress, cfg.ProbeAddress)
	require.Equal(t, expected.Egress, cfg.Egress)

	d, err := cfg.Discovery()
	if err != nil {
		t.Fatalf("could not get discovery client: %v", err)
	}

	for i, st := range cfg.Scanning.Types {
		gvks, err := st.GetGVKs(d, zap.New(zap.UseDevMode(true)))
		if err != nil {
			t.Fatalf("could not get GVKs: %v", err)
		}
		require.Equal(t, gvks, expectedGVKs[i])
	}
}

func TestGetGVKs(t *testing.T) {
	testTypes := map[string]struct {
		scanType     ScanType
		expectedGVKs []schema.GroupVersionKind
	}{
		"standard": {
			scanType: ScanType{
				APIGroups: []string{"apps", ""},
				Versions:  []string{"v1"},
				Resources: []string{"deployments", "pods"},
			},
			expectedGVKs: []schema.GroupVersionKind{{
				Group:   "apps",
				Version: "v1",
				Kind:    "Deployment",
			}, {
				Group:   "",
				Version: "v1",
				Kind:    "Pod",
			}},
		},
		"inexistent-group": {
			scanType: ScanType{
				APIGroups: []string{"custom-crd.io"},
				Versions:  []string{"v1"},
				Resources: []string{"foo"},
			},
			expectedGVKs: nil,
		},
		"multiple-versions-resource-only-in-one": {
			scanType: ScanType{
				APIGroups: []string{"storage.k8s.io"},
				Versions:  []string{"v1", "v1beta1"},
				Resources: []string{"csidrivers"},
			},
			expectedGVKs: []schema.GroupVersionKind{{
				Group:   "storage.k8s.io",
				Version: "v1",
				Kind:    "CSIDriver",
			}},
		},
		"multiple-versions-in-all-groups": {
			scanType: ScanType{
				APIGroups: []string{"autoscaling"},
				Versions:  []string{"v1", "v2"},
				Resources: []string{"horizontalpodautoscalers", "scales"},
			},
			expectedGVKs: []schema.GroupVersionKind{{
				Group:   "autoscaling",
				Version: "v1",
				Kind:    "HorizontalPodAutoscaler",
			}, {
				Group:   "autoscaling",
				Version: "v1",
				Kind:    "Scale",
			}, {
				Group:   "autoscaling",
				Version: "v2",
				Kind:    "HorizontalPodAutoscaler",
			}, {
				Group:   "autoscaling",
				Version: "v2",
				Kind:    "Scale",
			}},
		},
		"preferred-version": {
			scanType: ScanType{
				APIGroups: []string{"autoscaling"},
				Versions:  nil,
				Resources: []string{"horizontalpodautoscalers"},
			},
			expectedGVKs: []schema.GroupVersionKind{{
				Group:   "autoscaling",
				Version: "v2",
				Kind:    "HorizontalPodAutoscaler",
			}},
		},
		"wildcard-version": {
			scanType: ScanType{
				APIGroups: []string{"autoscaling"},
				Versions:  []string{"*"},
				Resources: []string{"horizontalpodautoscalers"},
			},
			expectedGVKs: []schema.GroupVersionKind{{
				Group:   "autoscaling",
				Version: "v2",
				Kind:    "HorizontalPodAutoscaler",
			}, {
				Group:   "autoscaling",
				Version: "v1",
				Kind:    "HorizontalPodAutoscaler",
			}},
		},
		"wildcard-version-inexistent-group": {
			scanType: ScanType{
				APIGroups: []string{"custom-crd.whatever.io"},
				Versions:  []string{"*"},
				Resources: []string{"mycustomresource"},
			},
			expectedGVKs: nil,
		},
	}
	fakeLog := zap.New(zap.UseDevMode(true))
	fakeDiscovery := &fakeDiscovery{
		groupVersions: map[string][]string{
			"apps":           {"v1"},
			"":               {"v1"},
			"storage.k8s.io": {"v1", "v1beta1"},
			"autoscaling":    {"v2", "v1"},
		},
		gvrToKind: map[schema.GroupVersionResource]string{
			{Group: "apps", Version: "v1", Resource: "deployments"}: "Deployment",
			{Group: "", Version: "v1", Resource: "pods"}:            "Pod",
			// this reflects reality on k8s 1.26 (and other versions): while the CSIDriver type
			// already existed in v1beta1, it is not being served anymore since 1.22. The
			// CSIStorageCapacity type however was served under v1beta1 until 1.27.
			{Group: "storage.k8s.io", Version: "v1", Resource: "csidrivers"}:                "CSIDriver",
			{Group: "storage.k8s.io", Version: "v1", Resource: "csidrivers"}:                "CSIDriver",
			{Group: "storage.k8s.io", Version: "v1beta1", Resource: "csistoragecapacities"}: "CSIStorageCapacity",
			{Group: "autoscaling", Version: "v1", Resource: "horizontalpodautoscalers"}:     "HorizontalPodAutoscaler",
			{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}:     "HorizontalPodAutoscaler",
			{Group: "autoscaling", Version: "v1", Resource: "scales"}:                       "Scale",
			{Group: "autoscaling", Version: "v2", Resource: "scales"}:                       "Scale",
		},
	}

	for name, tc := range testTypes {
		t.Run(name, func(t *testing.T) {
			gvks, err := tc.scanType.GetGVKs(fakeDiscovery, fakeLog)
			require.NoError(t, err)
			require.Equal(t, tc.expectedGVKs, gvks)
		})
	}
}

type fakeDiscovery struct {
	groupVersions map[string][]string
	gvrToKind     map[schema.GroupVersionResource]string
}

func (fd *fakeDiscovery) preferredVersionForGroup(group string) (string, error) {
	vers, err := fd.versionsForGroup(group)
	if err != nil {
		return "", err
	}
	return vers[0], nil
}

func (fd *fakeDiscovery) versionsForGroup(group string) ([]string, error) {
	versions, ok := fd.groupVersions[group]
	if !ok {
		return nil, newNotFoundError(schema.GroupVersionResource{Group: group})
	}
	return versions, nil
}

func (fd *fakeDiscovery) findGVK(gvr schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	kind, ok := fd.gvrToKind[gvr]
	if !ok {
		return schema.GroupVersionKind{}, newNotFoundError(gvr)
	}
	return gvr.GroupVersion().WithKind(kind), nil
}
