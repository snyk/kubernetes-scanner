package licenses

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

// we embed the full directory tree within this package, as we can't just select the "files" due to
// a circular dependency between the license-tool and a Go build command. The license-tool starts
// off with removing the `licenses/store` directory, then tries to build the Go module, and then
// writes all licenses back into `licenses/store`. However, if we embed `licenses/store`, the Go
// compiler will return a build-failure, thus breaking our licensing-step.
//
//go:embed *
var licenseFS embed.FS

const separator = "\n------------------------------------------------\n\n"

func Print() (exitCode int) {
	var licenses strings.Builder
	var readErr []error
	var numLicenses int
	licenses.WriteString("License Information for kubernetes-scanner:\n")
	_ = fs.WalkDir(licenseFS, ".", func(path string, d fs.DirEntry, _ error) error {
		if d.IsDir() || strings.HasSuffix(d.Name(), ".go") {
			return nil
		}

		contents, nErr := licenseFS.ReadFile(path)
		if nErr != nil {
			readErr = append(readErr, nErr)
			return nil
		}

		numLicenses++

		licenses.WriteString("License for " + strings.TrimSuffix(path, "/LICENSE") + ":\n\n")
		licenses.Write(contents)
		licenses.WriteString(separator)

		return nil
	})

	if numLicenses != 0 {
		fmt.Print(licenses.String())
	}

	var errMsg strings.Builder
	switch len(readErr) {
	case 0:
		return 0

	case 1:
		errMsg.WriteString("could not read license for a library:\n")
		errMsg.WriteString(readErr[0].Error())

	default:
		errMsg.WriteString(fmt.Sprintf("could not read licenses for %v libraries:\n", len(readErr)))
		for _, err := range readErr {
			// we rely on the errors already containing the filename.
			errMsg.WriteString(fmt.Sprintf("%v\n", err.Error()))
		}
	}

	errMsg.WriteString(separator + "all license information can be found at https://github.com/snyk/kubernetes-scanner\n")
	fmt.Print(errMsg.String())
	return 1
}
