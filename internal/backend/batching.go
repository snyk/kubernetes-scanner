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
package backend

import (
	"context"
	"github.com/google/uuid"
	"os"
	"os/signal"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sync"
	"syscall"
	"time"
)

// BatchingBackend queues upsert requests internally and reports them to configured backend asynchronously
// every executionPeriod in bulkSize
type BatchingBackend struct {
	resources       orgResourcesMap
	bulkSize        int
	executionPeriod time.Duration
	backend         *Backend
}

func NewBatchingBackend(backend *Backend, bulkSize int, executionPeriod time.Duration) *BatchingBackend {
	return &BatchingBackend{
		resources:       orgResourcesMap{values: &sync.Map{}},
		backend:         backend,
		bulkSize:        bulkSize,
		executionPeriod: executionPeriod,
	}
}

// Upsert queues the given kubernetes objects to be sent to backend
func (b *BatchingBackend) Upsert(_ context.Context, _ string, orgID string, kubeObjects []KubeObj) error {
	b.resources.add(orgID, kubeObjects)
	return nil
}

// Start starts the worker goroutine that processes resources until an OS signal is captured to terminate the program.
func (b *BatchingBackend) Start() *BatchingBackend {
	osSignal := make(chan os.Signal, 1)
	signal.Notify(osSignal,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)

	go func() {
		for {
			select {
			case <-osSignal:
				return
			default:
				// TODO: signal based on size of pending resources to process?
				// instead of sleeping, we can also generate periodical signals
				b.bulkProcessor()
				time.Sleep(b.executionPeriod)
			}
		}
	}()

	return b
}

// bulkProcessor sends resources to backend and logs failures
func (b *BatchingBackend) bulkProcessor() {
	ctx := context.Background()

	b.resources.consume(b.bulkSize, func(orgID string, kubeObjects []KubeObj) error {
		// TODO: Reconciler was generating a request id, now that one is no more relevant
		requestID := uuid.New().String()
		err := b.backend.Upsert(ctx, requestID, orgID, kubeObjects)
		if err != nil {
			for _, item := range kubeObjects {
				log.FromContext(ctx).
					WithValues(
						"request_id", requestID,
						"org_id", orgID,
						"uid", item.Obj.GetUID(),
						"gvk", item.Obj.GetObjectKind().GroupVersionKind(),
						"name", item.Obj.GetName(),
						"namespace", item.Obj.GetNamespace(),
					).Error(err, "failed to bulk process object")
			}
		}
		return err
	})
}

type queueConsumer func(orgID string, kubeObjects []KubeObj) error

// orgResourcesMap is a sync map[orgID] -> map[UID] -> KubeObj
// We store resources by their UID to be able to overwrite them
// with the newer representations if they are not sent to backend yet
type orgResourcesMap struct {
	values *sync.Map
}

func (q *orgResourcesMap) add(orgID string, kubeObjects []KubeObj) {
	v, _ := q.values.LoadOrStore(orgID, &sync.Map{})
	orgResources := v.(*sync.Map)

	for _, obj := range kubeObjects {
		orgResources.Store(obj.Obj.GetUID(), obj)
	}
}

// consume attempts to run consumer for at most count items for each organisation's resources
// in case of failures objects are re-added to map if there is no never representation
func (q *orgResourcesMap) consume(count int, consumer queueConsumer) {
	q.values.Range(func(k, v any) bool {
		orgID := k.(string)
		orgResources := v.(*sync.Map)

		// Retrieve objects for each organisation and prepare to consume them in bulks
		kubeObjects := make([]KubeObj, 0, count)
		orgResources.Range(func(key, value any) bool {
			obj := value.(KubeObj)
			kubeObjects = append(kubeObjects, obj)
			return len(kubeObjects) < count
		})

		// Consume objects
		if err := consumer(orgID, kubeObjects); err != nil {
			// If we failed to consume objects, add them to the queue again
			for _, obj := range kubeObjects {
				// LoadOrStore ensures we don't override a newer version of the object
				// with the old value we failed to process
				orgResources.LoadOrStore(obj.Obj.GetUID(), obj)
			}
		}

		return true
	})
}
