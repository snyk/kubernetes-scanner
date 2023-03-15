package helm

import (
	"fmt"
	"io"
	"os"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	helmchart "helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/provenance"
	helmrepo "helm.sh/helm/v3/pkg/repo"
	"sigs.k8s.io/yaml"
)

type PackagedChart struct {
	name    string
	version string
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
		name:    name,
		version: version,
		File:    file,
	}, nil
}

func (ch *PackagedChart) Name() string                { return ch.name }
func (ch *PackagedChart) Version() string             { return ch.version }
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
		Name:        ch.name,
		Version:     ch.version,
		Description: ch.name + " Chart",
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

		index.Entries[ch.name] = append(index.Entries[ch.name], &helmrepo.ChartVersion{
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
