package build

var (
	// these variables are set at build time to their respective values.
	commitSHA string
	tag       string
)

func Version() string {
	if tag == "" {
		tag = "v0.0.0"
	}

	if commitSHA != "" {
		return tag + "-" + commitSHA
	}
	return "unknown"
}
