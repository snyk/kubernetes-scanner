package test

import (
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func SetupEnv(t *testing.T) *rest.Config {
	testEnv := &envtest.Environment{}
	env, err := testEnv.Start()
	if err != nil {
		t.Fatalf("could not setup test environment: %v", err)
	}

	t.Cleanup(func() {
		testEnv.ControlPlaneStopTimeout = 10 * time.Second
		if err := testEnv.Stop(); err != nil {
			t.Errorf("error stopping test env: %v\n", err)
		}
	})

	return env
}
