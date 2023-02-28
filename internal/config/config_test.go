package config

import (
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/exp/slices"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/snyk/kubernetes-scanner/internal/test"
)

func TestConfigValid(t *testing.T) {
	cfg := &Config{
		Scanning: Scan{
			Types: []ScanType{{
				APIGroups: []string{""},
				Versions:  []string{"v1"},
				Resources: []string{"pods"},
			}, {
				APIGroups: []string{"custom-crd.snyk.io"},
				Versions:  []string{"v1alpha2"},
				Resources: []string{"securityconfigurations"},
			}},
		},
	}

	if err := cfg.validate(newFakeDiscoveryClient()); err != nil {
		t.Errorf("validation error on config: %v", err)
	}
}

func TestConfigInvalid(t *testing.T) {
	cfg := &Config{
		Scanning: Scan{
			Types: []ScanType{{
				APIGroups: []string{""},
				Versions:  []string{"v1"},
				Resources: []string{"pods"},
			}, {
				APIGroups: []string{"custom-crd.snyk.io"},
				Versions:  []string{"v1beta1"},
				Resources: []string{"securityconfigurations"},
			}},
		},
	}

	if err := cfg.validate(newFakeDiscoveryClient()); err == nil {
		t.Errorf("expected validation error, did not get one")
	}
}

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
`

	expected := &Config{
		MetricsAddress: ":8080",
		Scanning: Scan{
			RequeueAfter: metav1.Duration{Duration: time.Minute},
			Types: []ScanType{{
				APIGroups: []string{""},
				Versions:  []string{},
				Resources: []string{"pods"},
				GVKs: []schema.GroupVersionKind{{
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				}},
			}, {
				APIGroups:  []string{"apps"},
				Versions:   []string{"v1"},
				Resources:  []string{"deployments"},
				Namespaces: []string{"default"},
				GVKs: []schema.GroupVersionKind{{
					Group:   "apps",
					Version: "v1",
					Kind:    "Deployment",
				}},
			}},
		},
	}
	restCfg := test.SetupEnv(t)
	kubeCfgFile, err := os.CreateTemp(dir, "")
	if err != nil {
		t.Fatalf("could not create temporary kubeconfig file for testing: %v", err)
	}

	if err := generateKubeconfig(restCfg, kubeCfgFile.Name()); err != nil {
		t.Fatalf("could not generate kubeconfig: %v", err)
	}

	if err := flag.Set("kubeconfig", kubeCfgFile.Name()); err != nil {
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

	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validation error: %v", err)
	}

	if err := compareConfig(expected, cfg); err != nil {
		t.Fatalf("config differs: %v", err)
	}

	cfg.Scanning.Types = append(cfg.Scanning.Types, ScanType{
		APIGroups: []string{"custom-crd.snyk.io"},
		Versions:  []string{"v1beta1"},
		Resources: []string{"securityconfigurations"},
	})

	if err := cfg.Validate(); err == nil {
		t.Errorf("expected validation error, did not get one")
	}
}

func compareConfig(expected, actual *Config) error {
	if actual.MetricsAddress != expected.MetricsAddress {
		return fmt.Errorf("incorrect metrics address. expected=%q, got=%q", expected.MetricsAddress, actual.MetricsAddress)
	}
	if actual.Scanning.RequeueAfter.Duration != expected.Scanning.RequeueAfter.Duration {
		return fmt.Errorf("incorrect requeueAfter duration. expected=%v, got=%v",
			expected.Scanning.RequeueAfter.Duration, actual.Scanning.RequeueAfter.Duration)
	}

	if len(actual.Scanning.Types) != len(expected.Scanning.Types) {
		return fmt.Errorf("incorrect amount of scan types. expected=%v, got=%v",
			len(expected.Scanning.Types), len(actual.Scanning.Types))
	}

	for i, typ := range actual.Scanning.Types {
		// TODO: should the order be relevant?
		exp := expected.Scanning.Types[i]
		if len(typ.GVKs) != len(exp.GVKs) {
			return fmt.Errorf("amount of GroupVersionKinds do not match. expected=%v, got=%v",
				len(exp.GVKs), len(typ.GVKs))
		}

		for j, gvk := range typ.GVKs {
			expGVK := exp.GVKs[j]
			if gvk.Group != expGVK.Group || gvk.Version != expGVK.Version || gvk.Kind != expGVK.Kind {
				return fmt.Errorf("GVK for type %v do not match. expected=%#v, got=%#v", i, expGVK, gvk)
			}
		}

		if !slices.Equal(typ.Namespaces, exp.Namespaces) {
			return fmt.Errorf("namespaces do not match. expected=%v, got=%v", exp.Namespaces, typ.Namespaces)
		}
	}
	return nil
}

func generateKubeconfig(restCfg *rest.Config, targetFile string) error {
	clientConfig := clientcmdapi.Config{
		Kind:       "Config",
		APIVersion: "v1",
		Clusters: map[string]*clientcmdapi.Cluster{
			"default": {
				Server:                   restCfg.Host,
				CertificateAuthorityData: restCfg.CAData,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"default": {
				Cluster:  "default",
				AuthInfo: "default",
			},
		},
		CurrentContext: "default",
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"default": {
				Token:                 restCfg.BearerToken,
				ClientKeyData:         restCfg.KeyData,
				ClientCertificateData: restCfg.CertData,
			},
		},
	}
	return clientcmd.WriteToFile(clientConfig, targetFile)
}

func newFakeDiscoveryClient() *fake.FakeDiscovery {
	return &fake.FakeDiscovery{
		Fake: &k8stesting.Fake{
			Resources: []*metav1.APIResourceList{{
				GroupVersion: "v1",
				APIResources: []metav1.APIResource{{
					Name:    "pods",
					Kind:    "Pod",
					Group:   "",
					Version: "v1",
				}},
			}, {
				GroupVersion: "custom-crd.snyk.io/v1alpha2",
				APIResources: []metav1.APIResource{{
					Name:    "securityconfigurations",
					Kind:    "SecurityConfiguration",
					Group:   "custom-crd.snyk.io",
					Version: "v1alpha2",
				}},
			}},
		},
	}
}
