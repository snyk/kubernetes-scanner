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
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/snyk/kubernetes-scanner/internal/backend"
	"github.com/snyk/kubernetes-scanner/internal/config"
	controllertest "github.com/snyk/kubernetes-scanner/internal/test"
)

const (
	expectNoReconcilesLabel = "no-scrape"
	hasFinalizerLabel       = "has-finalizer"
	orgRouteAll             = "route-all"
	orgRouteTest            = "route-test"
	batcherInterval         = 1 * time.Second
)

func TestController(t *testing.T) {
	if testing.Short() {
		t.Skip("not running controller tests that spawn API server")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	log.SetLogger(zap.New(zap.UseDevMode(true)))

	test := newTest(t)
	c, err := client.New(test.config, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatalf("could not create client: %v", err)
	}
	k8sClient := testClient{c}

	cfg := &config.Config{
		Scheme:     scheme.Scheme,
		RestConfig: test.config,
		Scanning: config.Scan{
			Types:        test.types,
			RequeueAfter: metav1.Duration{Duration: time.Second},
		},
		Routes: []config.Route{
			{OrganizationID: orgRouteAll, ClusterScopedResources: true, Namespaces: []string{"*", "test"}},
			{OrganizationID: orgRouteTest, ClusterScopedResources: false, Namespaces: []string{"test"}},
			// adding possibly duplicating namespace route for orgRouteAll, we expect this to be de-duplicated automatically
			{OrganizationID: orgRouteAll, ClusterScopedResources: true, Namespaces: []string{"test"}},
		},
		Egress: &config.Egress{
			Batching: config.Batching{
				Interval: metav1.Duration{Duration: 1 * time.Second},
				MaxSize:  20,
			},
		},
		MetricsAddress: "localhost:9091",
	}

	if err := controllertest.WaitForAPI(ctx, c); err != nil {
		t.Fatalf("error waiting for API: %v", err)
	}

	// creating existing resources first to prove that the reconciler also reconciles already
	// existing objects.
	const initialResources = 1
	for _, res := range test.objects[:initialResources] {
		if err := k8sClient.Create(ctx, res); err != nil {
			t.Fatalf("could not create initial resource: %v", err)
		}
	}

	const timeout = 1500 * time.Millisecond
	// we have three goroutines that we need for this test:
	// 1) is responsible for creating the resources, waiting until the timeout and then deleting
	// these resources again. At the end, it will cancel the manager's context.
	// 2) is responsible for starting the controller (manager), waiting for it to finish (which is
	// triggered through the manager context cancelling) and then stop the backend's context.
	// 3) is the one which is running in this main flow, which simply calls `fb.collectEvents`,
	// stopped through 2).
	managerCtx, managerCancel := context.WithCancel(ctx)
	go func() {
		// cancel the context, which is important to stop the manager, once the resources are
		// deleted.
		defer managerCancel()
		wait := time.After(timeout)
		// create the rest of the resources which need to be reconciled.
		for _, res := range test.objects[initialResources:] {
			if err := k8sClient.Create(managerCtx, res); err != nil {
				t.Errorf("could not create resource: %v", err)
			}
		}

		<-wait
		if err := deleteResources(managerCtx, k8sClient, test.objects); err != nil {
			t.Errorf("could not delete resources: %v", err)
		}
		// we need to give the reconciler a bit of time in order to actually record all deletion
		// events, before we stop it
		time.Sleep(batcherInterval + 100*time.Millisecond)
	}()

	fb := newFakeBackend()
	backendCtx, backendCancel := context.WithCancel(ctx)
	var backendErr error
	go func() {
		defer backendCancel()

		mgr, err := New(cfg, fb)
		if err != nil {
			t.Errorf("could not setup controller: %v", err)
			backendErr = err
			return
		}

		// mgr.Start returns once ctx is done.
		if err := mgr.Start(managerCtx); err != nil {
			t.Errorf("could not start manager: %v", err)
			backendErr = err
		}
	}()

	<-backendCtx.Done()
	require.NoError(t, backendErr)
	events := fb.events()
	// e.g. a requeueAfter of 1s, timeout of 1.5 seconds means we expect 2 reconciliations each;
	// the first one immediately after create, and the next one a second later.
	numReconciliationLoops := int(timeout/cfg.Scanning.RequeueAfter.Duration) + 1
	for _, obj := range test.objects {
		if err := checkObject(obj, numReconciliationLoops, events, orgRouteAll); err != nil {
			t.Errorf("%v", err)
		}
		if err := checkObject(obj, numReconciliationLoops, events, orgRouteTest); err != nil {
			t.Errorf("%v", err)
		}
	}
}

type test struct {
	config  *rest.Config
	objects []client.Object
	types   []config.ScanType
}

func newTest(t *testing.T) test {
	return test{
		config: controllertest.SetupEnv(t),
		objects: []client.Object{
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "normal-pod",
					Namespace: "default",
					Labels: map[string]string{
						orgRouteTest + expectNoReconcilesLabel: "true",
					},
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Pod",
					APIVersion: "v1",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "bla",
						Image: "bla:latest",
					}},
				},
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "pod-with-finalizer",
					Namespace:  "default",
					Finalizers: []string{"yes.com/hello"},
					Labels: map[string]string{
						orgRouteAll + hasFinalizerLabel:        "true",
						orgRouteTest + expectNoReconcilesLabel: "true",
					},
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Pod",
					APIVersion: "v1",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "bla",
						Image: "bla:latest",
					}},
				},
			},
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "a-node",
					Labels: map[string]string{
						orgRouteTest + expectNoReconcilesLabel: "true",
					},
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Node",
					APIVersion: "v1",
				},
			},
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "normal-secret",
					Namespace: "default",
					Labels: map[string]string{
						orgRouteTest + expectNoReconcilesLabel: "true",
					},
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Secret",
					APIVersion: "v1",
				},
				Data: map[string][]byte{"hello": []byte("goodbye")},
			},
			&corev1.Namespace{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Namespace",
					APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Labels: map[string]string{
						orgRouteAll + expectNoReconcilesLabel:  "true",
						orgRouteTest + expectNoReconcilesLabel: "true",
					},
				},
			},
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Secret",
					APIVersion: "v1",
				},
				Data: map[string][]byte{"hello": []byte("goodbye")},
			},
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "object-we-dont-expect-to-scan",
					Namespace: "default",
					Labels: map[string]string{
						orgRouteAll + expectNoReconcilesLabel:  "true",
						orgRouteTest + expectNoReconcilesLabel: "true",
					},
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "ConfigMap",
					APIVersion: "v1",
				},
				Data: map[string]string{"hello": "goodbye"},
			},
		},
		types: []config.ScanType{{
			APIGroups: []string{""},
			Resources: []string{"secrets", "pods", "nodes"},
			Versions:  []string{"v1"},
		}, {
			APIGroups:  []string{""},
			Resources:  []string{"configmaps"},
			Namespaces: []string{"foo"},
		}},
	}
}

// fakeBackend implements the backend interface with a simple counter
// of the number of reconciliations and deletions. This only works if
// `collectEvents` is being called as well; the backend-implementations
// will block until then.
type fakeBackend struct {
	lock            sync.Mutex
	reconciliations map[resourceIdentifier]int
	deletions       map[resourceIdentifier]struct{}
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		reconciliations: map[resourceIdentifier]int{},
		deletions:       map[resourceIdentifier]struct{}{},
	}
}

func (f *fakeBackend) Upsert(ctx context.Context, requestID string, orgID string, resources []backend.Resource) error {
	for _, resource := range resources {
		rID := newResourceID(resource.ManifestBlob, orgID)
		f.lock.Lock()
		if resource.DeletedAt == nil {
			if _, ok := f.reconciliations[rID]; !ok {
				f.reconciliations[rID] = 0
			}
			f.reconciliations[rID] += 1
		} else {
			f.deletions[rID] = struct{}{}
		}
		f.lock.Unlock()
	}

	return nil
}

func (f *fakeBackend) events() reconciliationEvents {
	f.lock.Lock()
	reconciliationEvents := reconciliationEvents{
		reconciliations: map[resourceIdentifier]int{},
		deletions:       map[resourceIdentifier]struct{}{},
	}
	for k, v := range f.reconciliations {
		reconciliationEvents.reconciliations[k] = v
	}
	for k, v := range f.deletions {
		reconciliationEvents.deletions[k] = v
	}
	f.lock.Unlock()
	return reconciliationEvents
}

type reconciliationEvents struct {
	reconciliations map[resourceIdentifier]int
	deletions       map[resourceIdentifier]struct{}
}

type resourceIdentifier struct {
	schema.GroupVersionKind
	types.NamespacedName
	orgID string
}

func newResourceID(from client.Object, orgID string) resourceIdentifier {
	return resourceIdentifier{
		GroupVersionKind: from.GetObjectKind().GroupVersionKind(),
		NamespacedName: types.NamespacedName{
			Name:      from.GetName(),
			Namespace: from.GetNamespace(),
		},
		orgID: orgID,
	}
}

func (r resourceIdentifier) String() string {
	if r.Group == "" && r.Version == "" && r.Kind == "" {
		return r.Namespace + "/" + r.Name
	}

	return fmt.Sprintf("%v %v/%v",
		r.GroupVersionKind.String(), r.Namespace, r.Name,
	)
}

// checkObject checks that the given object has the correct amount of expectedReconciliations
// tracked in the events.
func checkObject(obj client.Object, expectedReconciliations int, events reconciliationEvents, orgID string) error {
	rID := newResourceID(obj, orgID)
	numReconciles, wasReconciled := events.reconciliations[rID]
	// if the resource is not expected to be reconciled - we mark this with the label - then we need to
	// make sure it wasn't.
	if _, noReconciles := obj.GetLabels()[orgID+expectNoReconcilesLabel]; noReconciles {
		if wasReconciled {
			return fmt.Errorf("object %v should not have been reconciled, but was for org %s", rID, orgID)
		}
		return nil
	}

	if !wasReconciled {
		return fmt.Errorf("object %v was not reconciled, but should have been for org %s", obj, orgID)
	}

	// if the resource had a finalizer - also marked with the label - the resource should have had
	// one more reconciliation than others; the one in between "deletion request" and actual
	// deletion.
	if _, ok := obj.GetLabels()[orgID+hasFinalizerLabel]; ok {
		numReconciles--
	}

	if numReconciles != expectedReconciliations {
		return fmt.Errorf("resource %v has wrong amount of reconciles for org %s. expected=%v, got=%v",
			newResourceID(obj, orgID), orgID, expectedReconciliations, numReconciles)
	}

	if _, ok := events.deletions[rID]; !ok {
		return fmt.Errorf("did not record resource deletion of %v for org %s", rID, orgID)
	}

	return nil
}

// removeFinalizers removes the finalizers from the given resource.
func removeFinalizers(ctx context.Context, c client.Client, from client.Object) error {
	nn := types.NamespacedName{Name: from.GetName(), Namespace: from.GetNamespace()}
	if err := c.Get(ctx, nn, from); err != nil {
		if kerrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("could not get update of resource: %w", err)
	}

	from.SetFinalizers(nil)
	if err := c.Update(ctx, from); err != nil {
		return fmt.Errorf("could not update resource: %w", err)
	}
	return nil
}

// deleteResources deletes all the given resources, potentially removing finalizers after a
// short grace period as well.
func deleteResources(ctx context.Context, c client.Client, resources []client.Object) error {
	for _, res := range resources {
		if err := c.Delete(ctx, res); err != nil {
			return fmt.Errorf("could not delete resource: %v", err)
		}
	}

	// give some time to record the initial deletion events, before we also remove finalizers.
	time.Sleep(100 * time.Millisecond)
	for _, res := range resources {
		if err := removeFinalizers(ctx, c, res); err != nil {
			return fmt.Errorf("could not remove finalizer: %v", err)
		}
	}
	return nil
}

// testClient implements client.Client, but makes sure that the GroupVersionKind is never removed
// from the resource. The "original" client.Client suffers of this issue, see
// https://github.com/kubernetes/kubernetes/issues/80609 for more info. This is annoying if the GVK
// + NamespacedName is used for resource identification purposes within the test.
type testClient struct {
	client.Client
}

func (c testClient) Get(ctx context.Context, nn types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	gvk := obj.GetObjectKind().GroupVersionKind()
	defer obj.GetObjectKind().SetGroupVersionKind(gvk)

	return c.Client.Get(ctx, nn, obj, opts...)
}

func (c testClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	gvk := obj.GetObjectKind().GroupVersionKind()
	defer obj.GetObjectKind().SetGroupVersionKind(gvk)

	return c.Client.Update(ctx, obj, opts...)
}

func (c testClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	gvk := obj.GetObjectKind().GroupVersionKind()
	defer obj.GetObjectKind().SetGroupVersionKind(gvk)

	return c.Client.Create(ctx, obj, opts...)
}

func TestGenerateControllerName(t *testing.T) {
	require.Equal(t, "snyk-scanner-my-company-io-v1-test-databaseconfig", generateControllerName(schema.GroupVersionKind{
		Group:   "my_company.io",
		Version: "v1_test",
		Kind:    "DatabaseConfig",
	}))

	long1 := generateControllerName(schema.GroupVersionKind{
		Group:   "long.domain.name.with.many.sections.example.io",
		Version: "v1alpha1",
		Kind:    "SuperLongResourceNameThatShouldDefinitelyBeTruncated",
	})
	require.True(
		t,
		strings.HasPrefix(long1, "snyk-scanner-long-domain-name-with-many-sections-examp-"),
	)
	require.LessOrEqual(t, len(long1), 63)

	long2 := generateControllerName(schema.GroupVersionKind{
		Group:   "long.domain.name.with.many.sections.example.io",
		Version: "v1alpha1",
		Kind:    "DifferentLongResourceName",
	})
	require.True(
		t,
		strings.HasPrefix(long2, "snyk-scanner-long-domain-name-with-many-sections-examp-"),
	)
	require.LessOrEqual(t, len(long2), 63)
	require.NotEqual(t, long1, long2)
}
