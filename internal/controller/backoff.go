package controller

import (
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// Set up a new backoff implementation for HTTP 5xx errors received from the
// backend.  Currently this is not configurable, but we could expose some of
// these options in `Config`.
func newHttp5xxBackOff() backoff.BackOff {
    exp := backoff.NewExponentialBackOff()
    exp.InitialInterval = 2 * time.Second
    exp.RandomizationFactor = 0.3
    exp.Multiplier = 2
    exp.MaxElapsedTime = 10 * time.Minute
    return newThreadSafeBackOff(exp)
}

// The exponential backoff implementation in the library is not thread safe.
// This wraps it with a mutex to fix that.
type threadSafeBackOff struct {
	mutex          *sync.Mutex
	implementation backoff.BackOff
}

func newThreadSafeBackOff(implementation backoff.BackOff) backoff.BackOff {
    return &threadSafeBackOff{
        mutex: &sync.Mutex{},
        implementation: implementation,
    }
}

func (b *threadSafeBackOff) NextBackOff() time.Duration {
	b.mutex.Lock()
	next := b.implementation.NextBackOff()
	b.mutex.Unlock()
	return next
}

func (b *threadSafeBackOff) Reset() {
    b.mutex.Lock()
    b.implementation.Reset()
    b.mutex.Unlock()
}
