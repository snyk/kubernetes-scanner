package batcher_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/snyk/kubernetes-scanner/internal/batcher"

	"github.com/stretchr/testify/require"
)

type org struct {
	id string
}

type thing struct {
	value int
}

func generate(n int) []thing {
	items := make([]thing, n)
	for i := 0; i < n; i++ {
		items[i] = thing{value: i}
	}
	return items
}

func TestBatcherMaxBatchSize(t *testing.T) {
	// In this case, the batcher will be triggered by the MaxbatchSize.
	input := generate(50)
	var firstBatch []thing
	processed := map[thing]int{}
	for i := range input {
		processed[input[i]] = 0
	}
	lock := sync.Mutex{}
	b := batcher.NewBatcher[org, thing](batcher.Config[org, thing]{
		MaxBatchSize: 10,
		Interval:     time.Duration(100 * time.Millisecond),
		Process: func(ctx context.Context, k org, batch []thing) {
			lock.Lock()
			if firstBatch == nil {
				firstBatch = batch
			}
			for i := range batch {
				processed[batch[i]] += 1
			}
			lock.Unlock()
		},
	})
	for _, item := range input {
		b.Queue(org{"foo"}, item)
	}
	time.Sleep(200 * time.Millisecond)
	lock.Lock()
	require.Equal(t, 10, len(firstBatch))
	for i := range input {
		require.Equal(t, 1, processed[input[i]])
	}
	lock.Unlock()
}

func TestBatcherInterval(t *testing.T) {
	// In this case, the batcher will be triggered by the Interval.
	input := generate(50)
	var firstBatch []thing
	processed := map[thing]int{}
	for i := range input {
		processed[input[i]] = 0
	}
	lock := sync.Mutex{}
	b := batcher.NewBatcher[org, thing](batcher.Config[org, thing]{
		MaxBatchSize: 10,
		Interval:     time.Duration(100 * time.Millisecond),
		Process: func(ctx context.Context, k org, batch []thing) {
			lock.Lock()
			if firstBatch == nil {
				firstBatch = batch
			}
			for i := range batch {
				processed[batch[i]] += 1
			}
			lock.Unlock()
		},
	})
	go func() {
		for _, item := range input {
			b.Queue(org{"foo"}, item)
			time.Sleep(40 * time.Millisecond)
		}
	}()
	time.Sleep(200 * time.Millisecond)
	lock.Lock()
	require.Equal(t, 3, len(firstBatch))
	lock.Unlock()
}

func TestBatcherNoZeroBatches(t *testing.T) {
	called := false
	batcher.NewBatcher[org, thing](batcher.Config[org, thing]{
		MaxBatchSize: 10,
		Interval:     time.Duration(10 * time.Millisecond),
		Process: func(ctx context.Context, k org, batch []thing) {
			called = true
		},
	})
	time.Sleep(30 * time.Millisecond)
	require.False(t, called)
}
