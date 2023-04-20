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
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

	values := map[string]interface{}{
		"secretName": "snyk-service-account",
		"routes": []map[string]interface{}{
			{
				"organizationID":         "umbrella-corp",
				"namespaces":             []string{"*"},
				"clusterScopedResources": true,
			},
		},
		"clusterName": "default",
		// while this doesn't test the correctness of the podMonitor, it at least ensures that it
		// can be decoded and is templated correctly.
		"prometheus": map[string]interface{}{
			"podMonitor": map[string]interface{}{
				"enabled": true,
				"labels":  map[string]interface{}{"some": "label"},
			},
		},
	}

	p, err := git.ResolvePath("//helm/kubernetes-scanner")
	if err != nil {
		t.Fatalf("could not resolve git path: %v", err)
	}

	objs, err := helm.TemplateChart(p, values)
	if err != nil {
		t.Fatalf("could not template chart: %v", err)
	}

	var checksOk int
	for _, obj := range objs {
		switch o := obj.(type) {
		case *corev1.ConfigMap:
			checkConfigMap(t, o)
			checksOk++
		case *unstructured.Unstructured:
			apiVersion, kind := o.GetAPIVersion(), o.GetKind()
			switch {
			case apiVersion == "monitoring.coreos.com/v1" && kind == "PodMonitor":
				checkUnstructuredPodMonitor(t, o)
				checksOk++
			}
		}
	}
	if checksOk != 2 {
		t.Fatalf("wrong amount of checks ok. expected=%v, got=%v", 2, checksOk)
	}
}

func checkUnstructuredPodMonitor(t *testing.T, u *unstructured.Unstructured) {
	if u.GetLabels()["some"] != "label" {
		t.Fatalf("label 'release' is missing: %v", u.GetLabels())
	}
}

func checkConfigMap(t *testing.T, cm *corev1.ConfigMap) {
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
		ProbeAddress:   ":8081",
		ClusterName:    "default",
		Routes: []Route{{
			OrganizationID:         "umbrella-corp",
			Namespaces:             []string{"*"},
			ClusterScopedResources: true,
		},
		},
		Scanning: Scan{
			RequeueAfter: metav1.Duration{Duration: 6 * time.Hour},
			Types: []ScanType{{
				APIGroups: []string{""},
				Versions:  []string{"*"},
				Resources: []string{"pods", "services", "namespaces", "replicationcontrollers", "nodes", "configmaps"},
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
