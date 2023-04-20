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
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/exp/slices"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	k8sdiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
)

type Config struct {
	Scanning       Scan   `json:"scanning"`
	MetricsAddress string `json:"metricsAddress"`
	// MetricsNamespace defines the namespace that will be used for the prometheus metrics.
	MetricsNamespace string `json:"metricsNamespace"`
	ProbeAddress     string `json:"probeAddress"`

	// Routes contain configuration resources from which namespaces are routed for which organization
	Routes []Route `json:"routes"`

	// Egress contains configuration for everything that's related to sending data to Snyk's
	// backend.
	Egress *Egress `json:"egress"`

	// ClusterName should be the "friendly" name of the cluster where the scanner is running in.
	// For example, "prod-us" or "dev-eu."
	ClusterName string `json:"clusterName"`

	Scheme     *runtime.Scheme `json:"-"`
	RestConfig *rest.Config    `json:"-"`
}

type Egress struct {
	// HTTPClientTimeout sets the timeout for the HTTP client that is being used for connections to
	// the Snyk backend.
	HTTPClientTimeout metav1.Duration `json:"httpClientTimeout"`

	// SnykAPIBaseURL defines the endpoint where the scanner will send data to.
	SnykAPIBaseURL string `json:"snykAPIBaseURL"`

	// SnykServiceAccountToken is the token of the Snyk Service Account. Is not read from the config
	// file, can only be set through the environment variable.
	SnykServiceAccountToken string `json:"-" env:"SNYK_SERVICE_ACCOUNT_TOKEN"`
}

type Route struct {
	// OrganizationID is the snyk organization ID where data should be routed to.
	OrganizationID string `json:"organizationID"`
	// ClusterScopedResources defines if cluster-scoped resources should be sent to the API.
	ClusterScopedResources bool `json:"clusterScopedResources"`
	// Namespaces from which resources will be sent to the API.
	// If empty, namespaced resources will not be sent at all.
	// Supports "*" to match all namespaces
	Namespaces []string `json:"namespaces"`
}

type GroupVersionKind struct {
	schema.GroupVersionKind
	PreferredVersion string
}

func (e Egress) validate() error {
	url, err := url.Parse(e.SnykAPIBaseURL)
	if err != nil {
		return fmt.Errorf("could not parse Snyk API Base URL %v: %w", e.SnykAPIBaseURL, err)
	}

	if url.Scheme == "" {
		return fmt.Errorf("Snyk API Base URL has no scheme set")
	}

	if e.SnykServiceAccountToken == "" {
		return fmt.Errorf("no Snyk service account token set")
	}

	return nil
}

func (r Route) validate() error {
	if r.OrganizationID == "" {
		return fmt.Errorf("organization ID is missing")
	}
	if len(r.Namespaces) == 0 && !r.ClusterScopedResources {
		return fmt.Errorf("no namespace or ClusterResource routing defined for the organization %s", r.OrganizationID)
	}
	return nil
}

// default values for config settings
const (
	// HTTPClientDefaultTimeout is the default value for the HTTPClientTimeout setting.
	HTTPClientDefaultTimeout = 5 * time.Second

	// SnykAPIDefaultBaseURL is the default endpoint that the scanner will talk to.
	SnykAPIDefaultBaseURL = "https://api.snyk.io"
)

type Scan struct {
	Types []ScanType `json:"types"`
	// RequeueAfter defines the duration after which an object is requeued when we've visited it.
	// Note that due to the event handlers, objects that are being changed will be requeued earlier
	// in such cases.
	RequeueAfter metav1.Duration `json:"requeueAfter"`
}

// Read reads the config file from the specificied flag "-config" and returns a
// struct that contains all options, including other flags.
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
		Egress: &Egress{
			HTTPClientTimeout:       metav1.Duration{Duration: HTTPClientDefaultTimeout},
			SnykAPIBaseURL:          SnykAPIDefaultBaseURL,
			SnykServiceAccountToken: os.Getenv("SNYK_SERVICE_ACCOUNT_TOKEN"),
		},
	}

	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("could not unmarshal config file: %w", err)
	}

	if len(c.Routes) == 0 {
		return nil, fmt.Errorf("no routes defined in config file")
	}

	for _, route := range c.Routes {
		if err := route.validate(); err != nil {
			return nil, fmt.Errorf("could not validate routes in config file: %w", err)
		}
	}

	if err := c.Egress.validate(); err != nil {
		return nil, fmt.Errorf("could not validate egress settings: %w", err)
	}

	return c, nil
}

type Discovery interface {
	// versionsForGroup should return all versions for a given group, where the first version should
	// be the preferredVersion.
	versionsForGroup(string) ([]string, error)
	findGVK(schema.GroupVersionResource) (schema.GroupVersionKind, error)
	findGroupPreferredVersion(group string) (string, error)
}

type ScanType struct {
	// TODO: The "*" group / resource specifier isn't implemented yet (and maybe never will).
	APIGroups []string `json:"apiGroups"`
	Resources []string `json:"resources"`
	// Versions is an optional field to specify which exact versions should be scanned. If unset,
	// the scanner will use the API Server's preferred version.
	Versions []string `json:"versions"`
	// Namespaces allows to restrict scanning to specific namespaces. An empty list means no
	// namespaces. Omit to scan resources in all namespaces. Does not affect the scanning of
	// cluster-scoped resources.
	Namespaces []string `json:"namespaces,omitempty"`
}

// GetGVKs returns all the GVKs that are defined in the ScanType and are available on the server.
func (st ScanType) GetGVKs(d Discovery, log logr.Logger) ([]GroupVersionKind, error) {
	var gvks []GroupVersionKind
	for _, group := range st.APIGroups {
		versions := st.Versions
		if slices.Contains(versions, "*") || len(versions) == 0 {
			var err error
			versions, err = d.versionsForGroup(group)
			if err != nil {
				if !k8serrors.IsNotFound(err) {
					return nil, fmt.Errorf("could not get versions for group %v: %w", group, err)
				}

				log.Info("skipping group as it does not exist", "group", group)
				continue
			}
		}

		// Only find preferred version after ensuring that the group exists
		preferredVersion, err := d.findGroupPreferredVersion(group)
		if err != nil {
			return nil, err
		}

	nextResource:
		for _, resource := range st.Resources {
			for _, version := range versions {
				gvr := schema.GroupVersionResource{
					Group:    group,
					Version:  version,
					Resource: resource,
				}
				gvk, err := d.findGVK(gvr)
				if err != nil {
					if !k8serrors.IsNotFound(err) {
						return nil, fmt.Errorf("could not get GVK for GVR %v: %w", gvr, err)
					}

					log.Info("skipping GVR as resource does not exist within groupversion",
						"group", group, "resource", resource, "version", version)
					// try finding this resource type in another version of this group.
					continue
				}
				gvks = append(gvks, GroupVersionKind{GroupVersionKind: gvk, PreferredVersion: preferredVersion})

				// if no versions where initially specified, we're looking for a single
				// GroupVersionResource combination. Usually, the version in that should be the
				// APIServer's preferredVersion. However, as the preferredVersion might not always
				// contain all specified resource types, we can only continue with the nextResource
				// once we've found the resource in a version.
				if len(st.Versions) == 0 {
					continue nextResource
				}
			}
		}
	}

	return gvks, nil
}

func (c *Config) Discovery() (Discovery, error) {
	// TODO: should we cache a discovery helper?
	cs, err := kubernetes.NewForConfig(c.RestConfig)
	if err != nil {
		return nil, fmt.Errorf("could not create kubernetes clientset: %w", err)
	}

	discovery, err := newDiscoveryHelper(cs.Discovery())
	if err != nil {
		return nil, fmt.Errorf("could not create discovery helper: %w", err)
	}

	return discovery, nil
}

type discoveryHelper struct {
	k8sdiscovery.DiscoveryInterface
	// to cache all group versions that we retrieve.
	resourcesForGroupVersion map[schema.GroupVersion][]metav1.APIResource
	groups                   []metav1.APIGroup
}

func newDiscoveryHelper(discoveryClient k8sdiscovery.DiscoveryInterface) (*discoveryHelper, error) {
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

func (d *discoveryHelper) versionsForGroup(apiGroup string) ([]string, error) {
	for _, group := range d.groups {
		if group.Name == apiGroup {
			// we always want to build a slice with the preferredVersion *first*.
			var versions = []string{group.PreferredVersion.Version}
			for _, ver := range group.Versions {
				if !slices.Contains(versions, ver.Version) {
					versions = append(versions, ver.Version)
				}
			}
			return versions, nil
		}
	}
	return nil, newNotFoundError(schema.GroupVersionResource{Group: apiGroup})
}

func (d *discoveryHelper) findGroupPreferredVersion(group string) (string, error) {
	for _, knownGroup := range d.groups {
		if group == knownGroup.Name {
			preferredVersion := knownGroup.PreferredVersion.Version
			if group == "" {
				return preferredVersion, nil
			}
			return fmt.Sprintf("%s/%s", group, preferredVersion), nil
		}
	}
	return "", fmt.Errorf("no known preferred version for group %s", group)
}

// findGVK returns the GroupVersionKind for the given GVR.
func (d *discoveryHelper) findGVK(gvr schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	gv := gvr.GroupVersion()

	resources, ok := d.resourcesForGroupVersion[gv]
	if !ok {
		list, err := d.ServerResourcesForGroupVersion(gv.String())
		if err != nil {
			return schema.GroupVersionKind{}, fmt.Errorf(
				"could not get server resources for groupversion %v: %w", gv, err,
			)
		}

		d.resourcesForGroupVersion[gv] = list.APIResources
		resources = list.APIResources
	}

	for _, res := range resources {
		if res.Name == gvr.Resource {
			return schema.GroupVersionKind{
				Group:   gv.Group,
				Version: gv.Version,
				Kind:    res.Kind,
			}, nil
		}
	}

	return schema.GroupVersionKind{}, newNotFoundError(gvr)
}

func newNotFoundError(gvr schema.GroupVersionResource) error {
	return k8serrors.NewNotFound(gvr.GroupResource(), gvr.Resource)
}
