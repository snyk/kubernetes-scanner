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
	// TODO: because this isn't RBAC, the "multiple groups, multiple resources" idea is a bit
	// tricky. In RBAC, if you have n apiGroups and m resources, not all of these combinations need
	// to exist (e.g. n[1] x m[1] may not necessarily be an existing resource type). The config
	// validation currently fails on such combinations, because we couldn't scan it. We might want
	// to just log that out instead.
	// TODO: The "*" group / resource specifier isn't implemented yet.
	APIGroups []string `json:"apiGroups"`
	Resources []string `json:"resources"`
	// Versions is an optional field to specify which exact versions should be scanned. If unset,
	// the scanner will use the API Server's preferred version.
	// TODO: "*" wildcard is not supported yet.
	Versions []string `json:"versions"`
	// Namespaces allows to restrict scanning to specific namespaces. An empty list means all
	// namespaces.
	Namespaces []string `json:"namespaces"`
	// GKVs contains the parsed groupVersionKinds that have been extracted from the APIGroups /
	// Resources / Version.
	GVKs []schema.GroupVersionKind `json:"-"`
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

type discoveryHelper struct {
	discovery.DiscoveryInterface
	// to cache all group versions that we retrieve.
	resourcesForGroupVersion map[schema.GroupVersion][]metav1.APIResource
	groups                   []metav1.APIGroup
}

func newDiscoveryHelper(discoveryClient discovery.DiscoveryInterface) (*discoveryHelper, error) {
	groups, err := discoveryClient.ServerGroups()
	if err != nil {
		return nil, err
	}

	return &discoveryHelper{
		DiscoveryInterface:       discoveryClient,
		resourcesForGroupVersion: make(map[schema.GroupVersion][]metav1.APIResource),
		groups:                   groups.Groups,
	}, nil
}

func (d *discoveryHelper) preferredVersionForGroup(apiGroup string) (string, error) {
	for _, group := range d.groups {
		if group.Name == apiGroup {
			return group.PreferredVersion.Version, nil
		}
	}
	return "", fmt.Errorf("group %v does not exist", apiGroup)
}

// findGVK returns the GroupVersionKind for the given group, resource and version. Version is
// optional, and if empty, the preferred version of the API Server will be used.
func (d *discoveryHelper) findGVK(apiGroup, resource, version string) (schema.GroupVersionKind, error) {
	gv := schema.GroupVersion{Group: apiGroup, Version: version}
	if gv.Version == "" {
		var err error
		gv.Version, err = d.preferredVersionForGroup(apiGroup)
		if err != nil {
			return schema.GroupVersionKind{}, err
		}
	}

	resources, ok := d.resourcesForGroupVersion[gv]
	if !ok {
		list, err := d.ServerResourcesForGroupVersion(gv.String())
		if err != nil {
			var statusErr *k8serrors.StatusError
			if errors.As(err, &statusErr) && statusErr.Status().Code == http.StatusNotFound {
				return schema.GroupVersionKind{}, fmt.Errorf(
					"GroupVersion %v does not seem to be supported by the server", gv,
				)
			}
			return schema.GroupVersionKind{}, fmt.Errorf(
				"could not get server resources for groupversion %v: %w", gv, err,
			)
		}

		d.resourcesForGroupVersion[gv] = list.APIResources
		resources = list.APIResources
	}

	for _, res := range resources {
		if res.Name == resource {
			return schema.GroupVersionKind{
				Group:   gv.Group,
				Version: gv.Version,
				Kind:    res.Kind,
			}, nil
		}
	}

	return schema.GroupVersionKind{}, fmt.Errorf("group version %v does not have resource %v",
		gv, resource)
}

// validate the configuration, and add all computed fields to the config. Returns an error if the
// config should be invalid or the validity of the config couldn't be determined.
func (c *Config) validate(discoveryClient discovery.DiscoveryInterface) error {
	dh, err := newDiscoveryHelper(discoveryClient)
	if err != nil {
		return err
	}

	for i, st := range c.Scanning.Types {
		var gvks []schema.GroupVersionKind
		// create a n x m "matrix" of all apiGroups & resource combination.
		for _, stAPIGroup := range st.APIGroups {
			for _, stResource := range st.Resources {
				versions := st.Versions
				// TODO: improve this with https://snyksec.atlassian.net/browse/TIKI-20
				if len(versions) == 0 {
					versions = append(versions, "")
				}
				for _, stVersion := range versions {
					gvk, err := dh.findGVK(stAPIGroup, stResource, stVersion)
					if err != nil {
						// TODO: we should probably log errors And continue anyway, so that not all
						// combinations of the n x m matrix need to exist.
						return fmt.Errorf("could not find GVK for group %v, resource %v, version %v: %w",
							stAPIGroup, stResource, st.Versions, err)
					}
					gvks = append(gvks, gvk)
				}
			}
		}
		c.Scanning.Types[i].GVKs = gvks
	}

	return nil
}
