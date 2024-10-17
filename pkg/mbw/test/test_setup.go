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
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/afero"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils"
)

// ATTENTION: NEVER EVER call functions in prod code
// this file is for test setup exposed to other module's test cases;
// MUST NOT to call any from none test files, although it is not *_test file.
// in other words, this file's content is for testing only; MUST not be used in prod code

var (
	syscallTestOnce sync.Once
	osTestOnce      sync.Once
	filerTestOnce   sync.Once
)

func SetupTestSyscaller() {
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

func SetupTestFiler() {
	filerTestOnce.Do(func() {
		utils.FilerSingleton = &filerMock{}
	})
}

type filerMock struct{}

func (f filerMock) ReadFileIntoInt(filepath string) (int, error) {
	switch filepath {
	case "/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_cur_freq":
		return 101, nil
	default:
		return 0, errors.New("mock test error")
	}
}

func SetupTestOS() {
	osTestOnce.Do(func() {
		testOS := &afero.Afero{Fs: afero.NewMemMapFs()}

		// we would like to have below device files exist for testing
		fakeFiles := []struct {
			dir     string
			file    string
			content string
		}{
			{
				dir:     "/sys/devices/system/node/node0/cpu0/cache/index3/",
				file:    "shared_cpu_list",
				content: "0-1\n",
			},
			{
				dir:     "/sys/devices/system/node/node0/cpu1/cache/index3/",
				file:    "shared_cpu_list",
				content: "0-1\n",
			},
			{
				dir:     "/sys/devices/system/node/node1/cpu2/cache/index3/",
				file:    "shared_cpu_list",
				content: "2-3\n",
			},
			{
				dir:     "/sys/devices/system/node/node1/cpu3/cache/index3/",
				file:    "shared_cpu_list",
				content: "2-3\n",
			},
		}

		for _, entry := range fakeFiles {
			_ = testOS.MkdirAll(entry.dir, os.ModePerm)
			_ = testOS.WriteFile(filepath.Join(entry.dir, entry.file), []byte(entry.content), os.ModePerm)
		}

		utils.OSSingleton = testOS
	})
}
