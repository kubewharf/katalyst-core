package test

import (
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils"
)

// below for testing only; MUST not be used in prod code
var (
	instanceTest utils.Syscaller
	onceTest     sync.Once
)

// caveat: NEVER EVER call this in prod code
func setupTestSyscaller() {
	onceTest.Do(func() {
		instanceTest = &stubSyscaller{}
	})
	utils.AppSyscall = instanceTest
}

type stubSyscaller struct{}

func (s stubSyscaller) Pwrite(fd int, p []byte, offset int64) (n int, err error) {
	return 8, nil
}

func (s stubSyscaller) Pread(fd int, p []byte, offset int64) (n int, err error) {
	p[7] = 0x003B //59
	return 8, nil
}

func (s stubSyscaller) Close(fd int) (err error) {
	return nil
}

func (s stubSyscaller) Open(path string, mode int, perm uint32) (fd int, err error) {
	return 99, nil
}

var _ utils.Syscaller = &stubSyscaller{}
