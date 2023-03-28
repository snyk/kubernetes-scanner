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
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

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
		"inexistent-group-with-version-set": {
			scanType: ScanType{
				APIGroups: []string{"custom-crd.io"},
				Versions:  []string{"v1"},
				Resources: []string{"foo"},
			},
			expectedGVKs: nil,
		},
		"inexistent-group-with-no-version": {
			scanType: ScanType{
				APIGroups: []string{"custom-crd.io"},
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
		"preferred-version-with-fallback-for-inexisting-resources": {
			scanType: ScanType{
				APIGroups: []string{"networking.gke.io"},
				Versions:  nil,
				// serviceattachments exist in both v1 & v1beta1, but we only want to use the
				// preferredVersion for these, which is v1.
				// frontendconfigs only exist in v1beta1, managedcertificates only in v1.
				Resources: []string{"serviceattachments", "frontendconfigs", "managedcertificates"},
			},
			expectedGVKs: []schema.GroupVersionKind{{
				Group:   "networking.gke.io",
				Version: "v1",
				Kind:    "ServiceAttachment",
			}, {
				Group:   "networking.gke.io",
				Version: "v1beta1",
				Kind:    "FrontendConfig",
			}, {
				Group:   "networking.gke.io",
				Version: "v1",
				Kind:    "ManagedCertificate",
			}},
		},
	}
	fakeLog := zap.New(zap.UseDevMode(true))
	fakeDiscovery := &fakeDiscovery{
		groupVersions: map[string][]string{
			"apps":              {"v1"},
			"":                  {"v1"},
			"storage.k8s.io":    {"v1", "v1beta1"},
			"autoscaling":       {"v2", "v1"},
			"networking.gke.io": {"v1", "v1beta1"},
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
			// this is *mostly* reality. On GKE, there's networking CRDs where some resource types
			// are only registered at a specific API Version. The preferredVersion for these APIs is
			// v1, but not all resources exist in v1.
			{Group: "networking.gke.io", Version: "v1beta1", Resource: "frontendconfigs"}:              "FrontendConfig",
			{Group: "networking.gke.io", Version: "v1beta1", Resource: "servicenetworkendpointgroups"}: "ServiceNetworkEndpointGroup",
			// serviceattachments only exist in v1 on an actual GKE cluster, but we want to make
			// sure that even if it would exist in multiple versions, we'd only select the
			// preferredVersion.
			{Group: "networking.gke.io", Version: "v1beta1", Resource: "serviceattachments"}: "ServiceAttachment",
			{Group: "networking.gke.io", Version: "v1", Resource: "managedcertificates"}:     "ManagedCertificate",
			{Group: "networking.gke.io", Version: "v1", Resource: "serviceattachments"}:      "ServiceAttachment",
		},
	}

	for name, tc := range testTypes {
		t.Run(name, func(t *testing.T) {
			gvks, err := tc.scanType.GetGVKs(fakeDiscovery, fakeLog)
			require.NoError(t, err)
			require.ElementsMatch(t, tc.expectedGVKs, gvks)
		})
	}
}

type fakeDiscovery struct {
	groupVersions map[string][]string
	gvrToKind     map[schema.GroupVersionResource]string
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
