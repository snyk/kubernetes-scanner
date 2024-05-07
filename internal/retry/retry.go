package retry

import (
	"time"

	"github.com/go-logr/logr"
)

func Seconds(seconds ...int) []time.Duration {
	times := make([]time.Duration, len(seconds))
	for i := range seconds {
		times[i] = time.Duration(seconds[i]) * time.Second
	}
	return times
}

func Retry(
	logger logr.Logger,
	intervals []time.Duration,
	worker func() error,
) error {
	for _, interval := range intervals {
		if err := worker(); err != nil {
			logger.Error(err, "retrying after error")
			time.Sleep(interval)
		} else {
			return nil
		}
	}
	// Need a final attempt after the last interval.
	return worker()
}
