/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package test

import (
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils"
)

// below for testing only; MUST not be used in prod code
var (
	syscallTestOnce sync.Once
)

func init() {
	setupTestSyscaller()
}

// caveat: NEVER EVER call this in prod code
func setupTestSyscaller() {
	syscallTestOnce.Do(func() {
		utils.AppSyscall = &stubSyscaller{}
	})
}

type stubSyscaller struct{}

func (s stubSyscaller) Pwrite(fd int, p []byte, offset int64) (n int, err error) {
	return 8, nil
}

func (s stubSyscaller) Pread(fd int, p []byte, offset int64) (n int, err error) {
	p[7] = 0x003B // 59
	return 8, nil
}

func (s stubSyscaller) Close(fd int) (err error) {
	return nil
}

func (s stubSyscaller) Open(path string, mode int, perm uint32) (fd int, err error) {
	return 99, nil
}
