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
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/snyk/kubernetes-scanner/build"
	"github.com/snyk/kubernetes-scanner/internal/backend"
	"github.com/snyk/kubernetes-scanner/internal/config"
	"github.com/snyk/kubernetes-scanner/internal/controller"
	"github.com/snyk/kubernetes-scanner/internal/retry"
	"github.com/snyk/kubernetes-scanner/licenses"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

func main() {
	var (
		printVersion = flag.Bool("version", false, "print the version of the kubernetes-scanner and exit")
		configFile   = flag.String("config", "/etc/kubernetes-scanner/config.yaml", "defines the location of the config file")
		showLicenses = flag.Bool("licenses", false, "show license information")
		logOpts      = zap.Options{
			// The various `zap-` flags in this struct definition can be passed to
			// this program due to the call to BindFlags() below. None of this is
			// exposed through helm, yet - a decision we might revisit.
		}
	)

	logOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	switch {
	case *printVersion:
		fmt.Println(build.Version())

	case *showLicenses:
		os.Exit(licenses.Print())

	default:
		os.Exit(runController(*configFile, &logOpts))
	}
}

func runController(configFile string, logOpts *zap.Options) (code int) {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(logOpts)))

	cfg, err := config.Read(configFile)
	if err != nil {
		ctrl.Log.Error(err, "error reading config file")
		return 1
	}

	backend := backend.New(cfg.ClusterName, cfg.Egress, ctrlmetrics.Registry)
	err = retry.Retry(ctrl.Log, 3, 5*time.Second, func() error {
		ctrl.Log.Info("sanity checking backend")
		return backend.SanityCheck(context.Background())
	})
	if err != nil {
		ctrl.Log.Error(err, "sanity check failed")
		return 1
	}

	mgr, err := controller.New(cfg, backend)
	if err != nil {
		ctrl.Log.Error(err, "error setting up controller")
		return 1
	}

	ctrl.Log.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		ctrl.Log.Error(err, "error running manager")
		return 1
	}

	return 0
}
