package test

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/msr"
)

func TestMSRDev_Close(t *testing.T) {
	t.Parallel()

	// set up test syscall harness
	setupTestSyscaller()

	testMSRDev := msr.MSRDev{}
	if err := testMSRDev.Close(); err != nil {
		t.Errorf("expcted no error, got %#v", err)
	}
}

func TestMSR(t *testing.T) {
	t.Parallel()

	// set up test syscall harness
	setupTestSyscaller()

	_, err := msr.MSR(9)
	if err != nil {
		t.Errorf("unexpected error: %#v", err)
	}
}
