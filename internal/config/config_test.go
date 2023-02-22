package config

import (
	"flag"
	"os"
	"testing"
	"time"

	"github.com/snyk/kubernetes-scanner/internal/test"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestConfigValid(t *testing.T) {
	cfg := &Config{
		Scanning: Scan{
			Types: []ScanType{{
				GroupVersionKind: schema.GroupVersionKind{
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
			}, {
				GroupVersionKind: schema.GroupVersionKind{
					Group:   "custom-crd.snyk.io",
					Version: "v1alpha2",
					Kind:    "SecurityConfiguration",
				},
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
				GroupVersionKind: schema.GroupVersionKind{Group: "",
					Version: "v1",
					Kind:    "Pod",
				},
			}, {
				GroupVersionKind: schema.GroupVersionKind{
					Group:   "custom-crd.snyk.io",
					Version: "v1beta1",
					Kind:    "SecurityConfiguration",
				},
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

	const exampleConfig = `
scanning:
  types:
  - group: ""
    version: "v1"
    kind: "Pod"
  - group: "apps"
    version: "v1"
    kind: "Deployment"
    namespaces: ["default"]
  requeueAfter: 1m
metricsAddress: ":8080"
`

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

	if _, err := f.Write([]byte(exampleConfig)); err != nil {
		t.Fatalf("error writing config file: %v", err)
	}

	cfg, err := Read(f.Name())
	if err != nil {
		t.Fatalf("could not read config: %v", err)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validation error: %v", err)
	}

	if cfg.MetricsAddress != ":8080" {
		t.Fatalf("incorrect metrics address. expected=%q, got=%q", ":8080", cfg.MetricsAddress)
	}
	if cfg.Scanning.RequeueAfter.Duration != time.Minute {
		t.Fatalf("incorrect requeueAfter duration. expected=%v, got=%v",
			time.Minute, cfg.Scanning.RequeueAfter.Duration)
	}

	if len(cfg.Scanning.Types) != 2 {
		t.Fatalf("incorrect amount of scan types. expected=%v, got=%v", 2, len(cfg.Scanning.Types))
	}
	if st := cfg.Scanning.Types[0]; st.Group != "" || st.Version != "v1" || st.Kind != "Pod" || st.Namespaces != nil {
		t.Fatalf("incorrect ScanType %v", st)
	}
	if st := cfg.Scanning.Types[1]; st.Group != "apps" || st.Version != "v1" ||
		st.Kind != "Deployment" || st.Namespaces[0] != "default" {
		t.Fatalf("incorrect ScanType %v", st)
	}

	cfg.Scanning.Types = append(cfg.Scanning.Types, ScanType{
		GroupVersionKind: schema.GroupVersionKind{
			Group:   "custom-crd.snyk.io",
			Version: "v1beta1",
			Kind:    "SecurityConfiguration",
		}})

	if err := cfg.Validate(); err == nil {
		t.Errorf("expected validation error, did not get one")
	}
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
