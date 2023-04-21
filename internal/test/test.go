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
package test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// SetupEnv sets up a test environment, meaning a kube-apiserver and an etcd store in order to run
// tests. This depends on "envtest" to be setup locally (e.g. for the binaries to be present), and
// will register a cleanup hook to stop everything at the end of the test.
func SetupEnv(t *testing.T) *rest.Config {
	testEnv := &envtest.Environment{}
	env, err := testEnv.Start()
	if err != nil {
		t.Fatalf("could not setup test environment: %v", err)
	}

	t.Cleanup(func() {
		testEnv.ControlPlaneStopTimeout = 10 * time.Second
		if err := testEnv.Stop(); err != nil {
			t.Errorf("error stopping test env: %v\n", err)
		}
	})

	return env
}

// GenerateKubeconfig generates a kubeconfig in a temporary directory that will automatically be
// cleaned up once the given testing.T test (and all of its subtests) end.
// The returned filename includes the path of the file.
func GenerateKubeconfig(t *testing.T, restCfg *rest.Config) (filename string) {
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

	// t.TempDir is automatically cleaned up after the test.
	// We do want to make sure though that we don't clash on filenames.
	file, err := os.CreateTemp(t.TempDir(), "")
	if err != nil {
		t.Fatalf("could not create temporary kubeconfig file for testing: %v", err)
	}

	if err := clientcmd.WriteToFile(clientConfig, file.Name()); err != nil {
		t.Fatalf("could not write kubeconfig: %v", err)
	}
	return file.Name()
}

func WaitForAPI(ctx context.Context, c client.Client) error {
	for {
		if err := c.Get(ctx, types.NamespacedName{Name: "default"}, &corev1.Namespace{}); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("timeout waiting for API to be ready")
			}
			continue
		}
		return nil
	}
}

// ParseKubernetesManifests parses the yaml manifests from the given io.Reader into a list of
// (typed) objects. Note that CRDs cannot be decoded into their typed representation, and will
// instead be returned as `unstructured.Unstructured` resources.
func ParseKubernetesManifests(yamlManifests io.Reader) ([]runtime.Object, error) {
	multidocReader := utilyaml.NewYAMLReader(bufio.NewReader(yamlManifests))
	var objs []runtime.Object
	for {
		buf, err := multidocReader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("could not read yaml document: %w", err)
		}

		// if we'd want to decode CRDs, we'd need to register the codecs.
		obj, _, err := scheme.Codecs.UniversalDeserializer().Decode(buf, nil, nil)
		if err != nil {
			// if we couldn't decode it with the core scheme, try decoding it into an ustructured
			// type. For this, we first need to convert the YAML buffer to JSON, so that we can pass
			// it to the unstructured decoder.
			json, err := yaml.YAMLToJSON(buf)
			if err != nil {
				return nil, fmt.Errorf("could not re-encode manifest to JSON for fallback: %w", err)
			}

			obj, _, err = unstructured.UnstructuredJSONScheme.Decode(json, nil, nil)
			if err != nil {
				return nil, fmt.Errorf("could not decode resource: %w", err)
			}
		}
		objs = append(objs, obj)
	}
	return objs, nil
}
