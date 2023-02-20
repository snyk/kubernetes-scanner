package main

import (
	"fmt"

	"github.com/snyk/kubernetes-scanner/build"
)

func main() {
	fmt.Println(build.Version())
}
