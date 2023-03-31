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
package helm

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	helmchart "helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/provenance"
	helmrepo "helm.sh/helm/v3/pkg/repo"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/yaml"
)

// TemplateChart  templates the helm chart at the given path with the given values and returns the
// decoded manifests. Note that CRDs cannot be decoded into their typed representation, and will
// instead be returned as `unstructured.Unstructured` resources.
func TemplateChart(path string, values map[string]interface{}) ([]runtime.Object, error) {
	chart, err := loader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("could not load Helm chart: %w", err)
	}

	actionConfig := &action.Configuration{}
	if err := actionConfig.Init(nil, "snyk", "", nil); err != nil {
		return nil, fmt.Errorf("could not initialise chart action config: %w", err)
	}

	iCli := action.NewInstall(actionConfig)
	iCli.ClientOnly = true
	iCli.ReleaseName = "test-render"
	rel, err := iCli.Run(chart, values)
	if err != nil {
		return nil, fmt.Errorf("could not render helm chart: %w", err)
	}

	multidocReader := utilyaml.NewYAMLReader(bufio.NewReader(strings.NewReader(rel.Manifest)))
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

type PackagedChart struct {
	Name    string
	Version string
	File    string
}

// PackageChart packages the chart at the given path with the given name and version.
func PackageChart(name string, version string, path string) (*PackagedChart, error) {
	tmpDir, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, err
	}

	file, err := (&action.Package{
		Version:     version,
		AppVersion:  version,
		Destination: tmpDir,
	}).Run(path, nil)
	if err != nil {
		return nil, fmt.Errorf("could not package chart: %w", err)
	}

	return &PackagedChart{
		Name:    name,
		Version: version,
		File:    file,
	}, nil
}

func (ch *PackagedChart) OpenFile() (*os.File, error) { return os.Open(ch.File) }

func (ch *PackagedChart) UploadedTo(urls ...string) *UploadedChart {
	return &UploadedChart{
		URLs:          urls,
		PackagedChart: ch,
	}
}

type UploadedChart struct {
	*PackagedChart
	URLs []string
}

func (ch *PackagedChart) Metadata() *helmchart.Metadata {
	return &helmchart.Metadata{
		Name:        ch.Name,
		Version:     ch.Version,
		Description: ch.Name + " Chart",
		APIVersion:  chart.APIVersionV1,
	}
}

// FileOverwriter is a subset of os.File methods that allow us to overwrite the whole file.
type FileOverwriter interface {
	// yes, we need all of these methods to read the file and then overwrite it.
	io.Reader
	io.Writer
	io.Seeker
	io.Closer
	Truncate(int64) error
}

// UpdateIndexYAML updates the given index.yaml file to include the new uploadedCharts.
// The index.yaml file is the file that indicates which charts are available in a Chart Repo.
func UpdateIndexYAML(indexFile FileOverwriter, charts ...*UploadedChart) error {
	indexBytes, err := io.ReadAll(indexFile)
	if err != nil {
		return fmt.Errorf("could not read index file: %w", err)
	}

	index := &helmrepo.IndexFile{}
	if err := yaml.Unmarshal(indexBytes, index); err != nil {
		return fmt.Errorf("could not unmarshal index file: %w", err)
	}

	for _, ch := range charts {
		digest, err := provenance.DigestFile(ch.File)
		if err != nil {
			return fmt.Errorf("could not get digest for chart: %w", err)
		}

		md := ch.Metadata()
		if err := md.Validate(); err != nil {
			return fmt.Errorf("error validating chart metadata: %w", err)
		}

		index.Entries[ch.Name] = append(index.Entries[ch.Name], &helmrepo.ChartVersion{
			URLs:     ch.URLs,
			Metadata: md,
			Digest:   digest,
			Created:  time.Now(),
		})
	}
	index.SortEntries()
	index.Generated = time.Now()

	newIndex, err := yaml.Marshal(index)
	if err != nil {
		return fmt.Errorf("could not marshal new index file: %w", err)
	}

	if err := indexFile.Truncate(0); err != nil {
		return fmt.Errorf("could not truncate file: %w", err)
	}
	if _, err := indexFile.Seek(0, 0); err != nil {
		return fmt.Errorf("could not seek to beginning of file: %w", err)
	}

	if _, err := indexFile.Write(newIndex); err != nil {
		return fmt.Errorf("could not write new index file: %w", err)
	}
	return nil
}
