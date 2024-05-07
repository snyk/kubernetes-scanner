package batcher

import (
	"context"
	"sync"
	"time"
)

type Config[K comparable, V any] struct {
	Context      context.Context
	MaxBatchSize int
	Interval     time.Duration
	Process      func(context.Context, K, []V)
}

type Batcher[K comparable, V any] struct {
	config Config[K, V]
	lock   *sync.Mutex
	queue  map[K][]V
}

func NewBatcher[K comparable, V any](config Config[K, V]) *Batcher[K, V] {
	b := &Batcher[K, V]{
		config: config,
		lock:   &sync.Mutex{},
		queue:  map[K][]V{},
	}

	if b.config.Context == nil {
		b.config.Context = context.Background()
	}

	go func() {
		for {
			// Wait until next iteration.
			time.Sleep(config.Interval)

			// Lock, and then create a new queue, we're going to process the old
			// queue.
			b.lock.Lock()
			processing := b.queue
			b.queue = map[K][]V{}
			b.lock.Unlock()

			// Process items per key, respecting MaxBatchSize.
			for key, items := range processing {
				for i := 0; i < len(items); i += config.MaxBatchSize {
					batch := items[i:min(i+config.MaxBatchSize, len(items))]
					if len(batch) > 0 {
						config.Process(b.config.Context, key, batch)
					}
				}
			}
		}
	}()

	return b
}

func (b *Batcher[K, V]) Queue(key K, value V) {
	b.lock.Lock()
	if _, ok := b.queue[key]; !ok {
		b.queue[key] = []V{}
	}
	b.queue[key] = append(b.queue[key], value)
	b.lock.Unlock()
}
