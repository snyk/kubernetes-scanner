package config

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
)

type Config struct {
	Scanning       Scan            `json:"scanning"`
	MetricsAddress string          `json:"metricsAddress"`
	ProbeAddress   string          `json:"probeAddress"`
	Scheme         *runtime.Scheme `json:"-"`
	RestConfig     *rest.Config    `json:"-"`
}

type ScanType struct {
	schema.GroupVersionKind `json:",inline"`
	// Namespaces allows to restrict scrapes to specific namespaces. An empty list means all
	// namespaces.
	Namespaces []string `json:"namespaces"`
}

type Scan struct {
	Types []ScanType `json:"types"`
	// RequeueAfter defines the duration after which an object is requeued when we've visited it.
	// Note that due to the event handlers, objects that are being changed will be requeued earlier
	// in such cases.
	RequeueAfter metav1.Duration `json:"requeueAfter"`
}

// Read reads the config file from the specificied flag "-config" and returns a
// struct that contains all options, including other flags. It is ensured that
// the config is valid, see the Validate method for details.
func Read(configFile string) (*Config, error) {
	if configFile == "" {
		return nil, fmt.Errorf("no config file set!")
	}

	b, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("could not read config file: %w", err)
	}

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("could not get kubernetes REST config: %v", err)
	}

	c := &Config{
		Scheme:     runtime.NewScheme(),
		RestConfig: restCfg,
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("could not unmarshal config file: %w", err)
	}

	return c, c.Validate()
}

// Validate the given config, ensuring that all defined scanning types exist on
// the APIServer.
func (c *Config) Validate() error {
	cs, err := kubernetes.NewForConfig(c.RestConfig)
	if err != nil {
		return fmt.Errorf("could not create kubernetes clientset: %w", err)
	}

	return c.validate(cs.Discovery())
}

func (c *Config) validate(discoveryClient discovery.DiscoveryInterface) error {
	cache := make(map[string]*metav1.APIResourceList)

nextType:
	for _, gvk := range c.Scanning.Types {
		gv := gvk.GroupVersion().String()
		list, ok := cache[gv]
		if !ok {
			var err error
			list, err = discoveryClient.ServerResourcesForGroupVersion(gv)
			if err != nil {
				var statusErr *k8serrors.StatusError
				if errors.As(err, &statusErr) && statusErr.Status().Code == http.StatusNotFound {
					return fmt.Errorf("GroupVersion %v does not seem to be supported by the server",
						gvk.String())
				}
				return fmt.Errorf("could not get server resources for groupversion %v: %w", gv, err)
			}
			cache[gv] = list
		}

		for _, res := range list.APIResources {
			if res.Kind == gvk.Kind {
				continue nextType
			}
		}

		return fmt.Errorf("Kind %v does not seem to be supported by the server", gvk.String())
	}
	return nil
}
