package retry

import (
	"time"

	"github.com/go-logr/logr"
)

func Retry(
	logger logr.Logger,
	attempts int,
	pause time.Duration,
	worker func() error,
) error {
	for attempts >= 0 {
		err := worker()
		if err != nil {
			if attempts <= 1 {
				return err
			} else {
				logger.Error(err, "retrying after error")
				attempts--
				time.Sleep(pause)
			}
		} else {
			return nil
		}
	}
	return nil // Should not be reached
}
