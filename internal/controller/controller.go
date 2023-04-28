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
package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/snyk/kubernetes-scanner/internal/backend"
	"github.com/snyk/kubernetes-scanner/internal/config"
	"github.com/snyk/kubernetes-scanner/internal/kubeobjects"
	"golang.org/x/exp/slices"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func New(cfg *config.Config, s Store) (manager.Manager, error) {
	mgr, err := ctrl.NewManager(cfg.RestConfig, ctrl.Options{
		Scheme:                 cfg.Scheme,
		MetricsBindAddress:     cfg.MetricsAddress,
		HealthProbeBindAddress: cfg.ProbeAddress,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to start manager: %w", err)
	}

	discovery, err := cfg.Discovery()
	if err != nil {
		return nil, fmt.Errorf("unable to create discovery client: %w", err)
	}

	for _, scanType := range cfg.Scanning.Types {
		// TODO: we depend on the logger being setup implicitly...
		gvks, err := scanType.GetGVKs(discovery, log.Log)
		if err != nil {
			return nil, fmt.Errorf("could not get GVK: %w", err)
		}

		for _, gvk := range gvks {
			if err := (&reconciler{
				Reader:        mgr.GetClient(),
				requeueAfter:  cfg.Scanning.RequeueAfter.Duration,
				Store:         s,
				gvk:           gvk,
				routes:        newResourceRoutes(cfg.Routes),
				namespaces:    scanType.Namespaces,
				pathsToRemove: scanType.PathsToRemove,
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

type reconciler struct {
	client.Reader
	requeueAfter time.Duration
	gvk          config.GroupVersionKind
	Store
	namespaces    []string
	routes        resourceRoutes
	pathsToRemove []string
}

type resourceRoutes struct {
	clusterResources []string
	namespaceRoutes  map[string][]string
}

func newResourceRoutes(routes []config.Route) resourceRoutes {
	cfg := resourceRoutes{
		clusterResources: []string{},
		namespaceRoutes:  map[string][]string{},
	}

	// Creating clusterResources with unique organizationIDs
	// This de-duplicates possible misconfiguration with multiple ClusterScopedResources:true defined for same org
	clusterRouteSet := map[string]bool{}
	for _, route := range routes {
		if route.ClusterScopedResources {
			clusterRouteSet[route.OrganizationID] = true
		}
	}
	for orgID := range clusterRouteSet {
		cfg.clusterResources = append(cfg.clusterResources, orgID)
	}

	// Set with unique organizationIDs -> orgId -> namespace -> bool
	namespaceRouteSet := map[string]map[string]bool{}
	for _, route := range routes {
		if namespaceRouteSet[route.OrganizationID] == nil {
			namespaceRouteSet[route.OrganizationID] = map[string]bool{}
		}
		for _, ns := range route.Namespaces {
			namespaceRouteSet[route.OrganizationID][ns] = true
		}
	}
	// If there is a wildcard namespace route for an organization, do not store other namespace routes.
	// This helps to de-duplicate if an organization was configured with ["*", "ns-1", ....]
	for orgID, namespaceRoutes := range namespaceRouteSet {
		if namespaceRoutes["*"] {
			cfg.namespaceRoutes["*"] = append(cfg.namespaceRoutes["*"], orgID)
		} else {
			for ns := range namespaceRoutes {
				cfg.namespaceRoutes[ns] = append(cfg.namespaceRoutes[ns], orgID)
			}
		}
	}
	return cfg
}

// targetOrganizations returns target organizations for given request based on config.Routes
// Resources can be configured to be routed for zero or more organizations
func (r resourceRoutes) targetOrganizations(req ctrl.Request) []string {
	if req.Namespace == "" {
		return r.clusterResources
	}
	// For namespaced resource, return all organizations with * and this specific namespace routes
	return append(r.namespaceRoutes[req.Namespace], r.namespaceRoutes["*"]...)
}

type Store interface {
	// Upsert an object into the store. If the deletedAt time is non-zero, a deletion-event should
	// be recorded. Otherwise, the store should simply ensure that the object saved in the store
	// matches the one we're providing.
	Upsert(ctx context.Context, requestID string, obj client.Object, preferredVersion, orgID string, deletedAt *metav1.Time) error
}

// newObject creates a new object for this reconciler with the reconciler's GVK and the requests
// name & namespace. This is all we know about an object without getting it from the Kube API.
func (r *reconciler) newObject(req ctrl.Request) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(r.gvk.GroupVersionKind)
	obj.SetName(req.Name)
	obj.SetNamespace(req.Namespace)

	return obj
}

// isIgnored returns true if the given request should be ignored / skipped due to the namespace of
// the request and the setup of this reconciler.
func (r *reconciler) isIgnored(req ctrl.Request) bool {
	// as long as r.namespaces is set, we want to check it. It might be 0-length, which will skip
	// all namespaced resources. This is expected behavior.
	return req.Namespace != "" && r.namespaces != nil &&
		!slices.Contains(r.namespaces, req.Namespace)
}

func (r *reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx,
		"resource", map[string]string{
			"group":     r.gvk.Group,
			"version":   r.gvk.Version,
			"kind":      r.gvk.Kind,
			"name":      req.Name,
			"namespace": req.Namespace,
		},
	)
	logger.Info("reconciling resource")

	if r.isIgnored(req) {
		logger.Info("skipping resources as namespace is ignored")
		// Ignored resource means we don't need to requeue it either.
		return ctrl.Result{}, nil
	}

	orgs := r.routes.targetOrganizations(req)
	if len(orgs) == 0 {
		logger.Info("skipping resources as namespace has no routes")
		return ctrl.Result{}, nil
	}

	obj := r.newObject(req)
	var deleted *metav1.Time
	switch err := r.Get(ctx, req.NamespacedName, obj); {
	case kerrors.IsNotFound(err):
		logger = logger.WithValues("reconciliation_action", "delete")
		deleted = &metav1.Time{Time: time.Now()}

	case err != nil:
		logger.Error(fmt.Errorf("could not get object from api server: %w", err), "failed reconciliation")
		return ctrl.Result{}, fmt.Errorf("could not get referenced object %v: %w", req.NamespacedName, err)

	default:
		logger = logger.WithValues("uid", obj.GetUID(), "reconciliation_action", "upsert")
	}

	var allErrs error
	for _, orgID := range orgs {
		requestID := uuid.New().String()
		reqLogger := logger.WithValues("organization_id", orgID, "request_id", requestID)
		ctx = log.IntoContext(ctx, reqLogger)

		r.removeConfiguredAttributes(ctx, obj)
		if err := r.Store.Upsert(ctx, requestID, obj, r.gvk.PreferredVersion, orgID, deleted); err != nil {
			var httpErr *backend.HTTPError
			if errors.As(err, &httpErr) {
				// when zap finds an `error` type in the values, it will call `Error` and print that
				// message in the logs. We want to get the underlying values though, to have indexed
				// fields.
				reqLogger = reqLogger.WithValues("http_response_error", httpErr.Values())
			}

			reqLogger.Error(fmt.Errorf("could not upsert to store: %w", err), "failed reconciliation")
			if allErrs == nil {
				allErrs = err
			} else {
				allErrs = fmt.Errorf("%w, %w", err, allErrs)
			}
		} else {
			reqLogger.Info("successful upsert")
		}

	}

	if allErrs != nil {
		return ctrl.Result{}, allErrs
	}

	logger.Info("successful reconciliation")
	// don't requeue after deletion.
	if deleted != nil {
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: r.requeueAfter}, nil
}

func (r *reconciler) removeConfiguredAttributes(ctx context.Context, obj *unstructured.Unstructured) {
	if len(r.pathsToRemove) == 0 {
		return
	}

	logger := log.FromContext(ctx)
	logger.Info("removing configured resource attributes")

	for _, pathToRemove := range r.pathsToRemove {
		kubeobjects.RemoveAttributes(obj, pathToRemove)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *reconciler) SetupWithManager(mgr ctrl.Manager) error {
	o := &unstructured.Unstructured{}
	o.SetGroupVersionKind(r.gvk.GroupVersionKind)

	return ctrl.NewControllerManagedBy(mgr).
		For(o).
		Complete(r)
}
