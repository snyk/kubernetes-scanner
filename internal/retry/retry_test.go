package retry

import (
	"errors"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
)

func TestRetrySuccess(t *testing.T) {
	calls := 0
	err := Retry(testr.New(t), Seconds(0, 0), func() error {
		calls++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, calls, 1)
}

func TestRetryTwice(t *testing.T) {
	calls := 0
	err := Retry(testr.New(t), Seconds(0, 0), func() error {
		calls++
		if calls >= 2 {
			return nil
		}
		return errors.New("boom")
	})
	require.NoError(t, err)
	require.Equal(t, calls, 2)
}

func TestRetryFails(t *testing.T) {
	calls := 0
	err := Retry(testr.New(t), Seconds(0, 0), func() error {
		calls++
		return errors.New("boom")
	})
	require.Error(t, err)
	require.Equal(t, calls, 3)
}
