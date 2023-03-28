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
package config

import (
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/snyk/kubernetes-scanner/build/helmreleaser/git"
	"github.com/snyk/kubernetes-scanner/build/helmreleaser/helm"
	"github.com/snyk/kubernetes-scanner/internal/test"
)

func TestHelmChartConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("not spawning API Server in the interest of time")
	}

	cm, err := getHelmChartConfigMap("//helm/kubernetes-scanner", map[string]interface{}{
		"secretName":     "snyk-service-account",
		"organizationID": "umbrella-corp",
	})
	if err != nil {
		t.Fatalf("could not load config from helm chart: %v", err)
	}
	config, ok := cm.Data["config.yaml"]
	if !ok {
		t.Fatalf("configMap does not contain `config.yaml`")
	}

	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "")
	if err != nil {
		t.Fatalf("could not create temporary file for testing: %v", err)
	}

	const testToken = "my-token"
	t.Setenv("SNYK_SERVICE_ACCOUNT_TOKEN", testToken)

	expected := &Config{
		MetricsAddress: ":8080",
		ClusterName:    "default",
		OrganizationID: "umbrella-corp",
		Scanning: Scan{
			RequeueAfter: metav1.Duration{Duration: 6 * time.Hour},
			Types: []ScanType{{
				APIGroups:  []string{""},
				Versions:   []string{"*"},
				Resources:  []string{"pods", "services", "namespaces", "replicationcontrollers", "nodes", "configmaps"},
				Namespaces: []string{},
			}, {
				APIGroups: []string{"batch"},
				Versions:  []string{"*"},
				Resources: []string{"cronjobs", "jobs"},
			}},
		},
		Egress: &Egress{
			SnykServiceAccountToken: testToken,
			HTTPClientTimeout:       metav1.Duration{Duration: 5 * time.Second},
			SnykAPIBaseURL:          "https://api.snyk.io",
		},
	}
	// these are just *some* GVKs, not all of them.
	expectedGVKs := [][]GroupVersionKind{
		{
			{GroupVersionKind: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}, PreferredVersion: "v1"},
			{GroupVersionKind: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Service"}, PreferredVersion: "v1"},
			{GroupVersionKind: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"}, PreferredVersion: "v1"},
			{GroupVersionKind: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ReplicationController"}, PreferredVersion: "v1"},
			{GroupVersionKind: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Node"}, PreferredVersion: "v1"},
			{GroupVersionKind: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, PreferredVersion: "v1"},
		}, {
			{GroupVersionKind: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, PreferredVersion: "apps/v1"},
			{GroupVersionKind: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "DaemonSet"}, PreferredVersion: "apps/v1"},
			{GroupVersionKind: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, PreferredVersion: "apps/v1"},
			{GroupVersionKind: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}, PreferredVersion: "apps/v1"},
		}, {
			{GroupVersionKind: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"}, PreferredVersion: "batch/v1"},
			{GroupVersionKind: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}, PreferredVersion: "batch/v1"},
		}, {
			{GroupVersionKind: schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"}, PreferredVersion: "networking.k8s.io/v1"},
		},
		// we don't deploy any CRDs, so we can't check them.
		// TODO: should we?
	}

	restCfg := test.SetupEnv(t)
	if err := flag.Set("kubeconfig", test.GenerateKubeconfig(t, restCfg)); err != nil {
		t.Fatalf("could not set kubeconfig flag: %v", err)
	}
	flag.Parse()

	if _, err := f.Write([]byte(config)); err != nil {
		t.Fatalf("error writing config file: %v", err)
	}

	cfg, err := Read(f.Name())
	if err != nil {
		t.Fatalf("could not read config: %v", err)
	}

	for _, typ := range expected.Scanning.Types {
		require.Contains(t, cfg.Scanning.Types, typ)
	}
	require.Equal(t, expected.Scanning.RequeueAfter, cfg.Scanning.RequeueAfter)
	require.Equal(t, expected.MetricsAddress, cfg.MetricsAddress)
	require.Equal(t, expected.ProbeAddress, cfg.ProbeAddress)
	require.Equal(t, expected.Egress, cfg.Egress)

	d, err := cfg.Discovery()
	if err != nil {
		t.Fatalf("could not get discovery client: %v", err)
	}

	var allGVKs [][]GroupVersionKind
	for _, st := range cfg.Scanning.Types {
		gvks, err := st.GetGVKs(d, zap.New(zap.UseDevMode(true)))
		if err != nil {
			t.Fatalf("could not get GVKs: %v", err)
		}
		// gvks might be nil, but we don't care.
		allGVKs = append(allGVKs, gvks)
	}

	for _, expectedGVK := range expectedGVKs {
		require.Contains(t, allGVKs, expectedGVK)
	}
}

func getHelmChartConfigMap(path string, values map[string]interface{}) (*corev1.ConfigMap, error) {
	p, err := git.ResolvePath(path)
	if err != nil {
		return nil, fmt.Errorf("could not resolve git path: %w", err)
	}

	objs, err := helm.TemplateChart(p, values)
	if err != nil {
		return nil, fmt.Errorf("could not template chart: %w", err)
	}

	for _, obj := range objs {
		if cm, ok := obj.(*corev1.ConfigMap); ok {
			return cm, nil
		}
	}

	return nil, fmt.Errorf("no config file in templated manifests found")
}
