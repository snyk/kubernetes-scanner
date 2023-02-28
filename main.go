package main

import (
	"context"
	"fmt"
	"time"

	"flag"
	"os"

	"github.com/snyk/kubernetes-scanner/build"
	"github.com/snyk/kubernetes-scanner/internal/config"

	"golang.org/x/exp/slices"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func main() {
	var (
		printVersion = flag.Bool("version", false, "print the version of the kubernetes-scanner and exit")
		configFile   = flag.String("config", "/etc/kubernetes-scanner/config.yaml", "defines the location of the config file")
		setupLog     = ctrl.Log.WithName("setup")
		logOpts      = zap.Options{
			Development: true,
		}
	)

	logOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&logOpts)))

	if *printVersion {
		fmt.Println(build.Version())
		os.Exit(0)
	}

	cfg, err := config.Read(*configFile)
	if err != nil {
		setupLog.Error(err, "unable to read config file")
		os.Exit(1)
	}

	mgr, err := setupController(cfg, defaultBackend{})
	if err != nil {
		setupLog.Error(err, "unable to setup controller")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func setupController(cfg *config.Config, b backend) (manager.Manager, error) {
	mgr, err := ctrl.NewManager(cfg.RestConfig, ctrl.Options{
		Scheme:                 cfg.Scheme,
		MetricsBindAddress:     cfg.MetricsAddress,
		HealthProbeBindAddress: cfg.ProbeAddress,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to start manager: %w", err)
	}

	for _, scanType := range cfg.Scanning.Types {
		for _, gvk := range scanType.GVKs {
			if err := (&reconciler{
				Reader:       mgr.GetClient(),
				requeueAfter: cfg.Scanning.RequeueAfter.Duration,
				backend:      b,
				newObject:    newObjectFn(gvk),
				namespaces:   scanType.Namespaces,
			}).SetupWithManager(mgr); err != nil {
				return nil, fmt.Errorf("unable to create controller for GVK %v: %w", gvk, err)
			}
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return nil, fmt.Errorf("unable to setup health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return nil, fmt.Errorf("unable to setup readiness check: %w", err)
	}

	return mgr, nil
}

type defaultBackend struct{}

func (defaultBackend) reconcile(ctx context.Context, obj client.Object) error {
	log := log.FromContext(ctx)
	log.Info("reconciling resource",
		"gvk", obj.GetObjectKind().GroupVersionKind().String(),
		"name", obj.GetName(),
		"namespace", obj.GetNamespace(),
	)

	return nil
}
func (defaultBackend) delete(ctx context.Context, nn types.NamespacedName, gvk schema.GroupVersionKind) error {
	log := log.FromContext(ctx)
	log.Info("deleted resource",
		"gvk", gvk.String(),
		"name", nn.Name,
		"namespace", nn.Namespace,
	)
	return nil
}

func newObjectFn(forGVK schema.GroupVersionKind) func() client.Object {
	return func() client.Object {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(forGVK)
		return u
	}
}

type reconciler struct {
	client.Reader
	requeueAfter time.Duration
	// newObject should return an empty, "typed" object that needs to be reconciled.
	// This will be used to call `client.Get` and passed through to the reconcile function.
	newObject func() client.Object
	backend
	namespaces []string
}

type backend interface {
	reconcile(context.Context, client.Object) error
	delete(context.Context, types.NamespacedName, schema.GroupVersionKind) error
}

func (r *reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.namespaces != nil && !slices.Contains(r.namespaces, req.Namespace) {
		// don't set the requeueafter, we don't need it.
		return ctrl.Result{}, nil
	}

	obj := r.newObject()
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		if !kerrors.IsNotFound(err) {
			fmt.Printf("error getting object: %v\n", err)
			return ctrl.Result{}, fmt.Errorf("could not get referenced object %v: %w", req.NamespacedName, err)
		}

		// don't requeue after deletion.
		return ctrl.Result{}, r.delete(ctx, req.NamespacedName, obj.GetObjectKind().GroupVersionKind())
	}

	return ctrl.Result{RequeueAfter: r.requeueAfter}, r.reconcile(ctx, obj)
}

// SetupWithManager sets up the controller with the Manager.
func (r *reconciler) SetupWithManager(mgr ctrl.Manager) error {
	o := r.newObject()
	fmt.Println("setting up reconciler for", o.GetObjectKind().GroupVersionKind())
	return ctrl.NewControllerManagedBy(mgr).
		For(r.newObject()).
		Complete(r)
}
