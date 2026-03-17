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

package plugin

import (
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

var transparentMemoryOffloadingTestMutex sync.Mutex

type mockDirEntry struct {
	name  string
	isDir bool
}

func (m mockDirEntry) Name() string               { return m.name }
func (m mockDirEntry) IsDir() bool                { return m.isDir }
func (m mockDirEntry) Type() os.FileMode          { return 0 }
func (m mockDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func TestTransparentMemoryOffloading_GetAdvices_DyingMemcgReclaim(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()
	defer mockey.UnPatchAll()

	tmo := &transparentMemoryOffloading{}

	mockey.Mock(os.ReadDir).IncludeCurrentGoRoutine().Return([]os.DirEntry{
		mockDirEntry{name: "offline-besteffort-0", isDir: true},
		mockDirEntry{name: "offline-besteffort-1", isDir: true},
		mockDirEntry{name: "burstable", isDir: true},
	}, nil).Build()

	result := tmo.GetAdvices()
	assert.Len(t, result.ExtraEntries, 3)

	expectedPaths := map[string]struct{}{
		"/sys/fs/cgroup/" + memoryadvisor.OnlineBurstableCgroupPath:                    {},
		"/sys/fs/cgroup/" + memoryadvisor.KubePodsCgroupPath + "/offline-besteffort-0": {},
		"/sys/fs/cgroup/" + memoryadvisor.KubePodsCgroupPath + "/offline-besteffort-1": {},
	}

	for _, entry := range result.ExtraEntries {
		assert.Equal(t, consts.ControlKnobON, entry.Values[string(memoryadvisor.ControlKnowKeyDyingMemcgReclaim)])
		_, ok := expectedPaths[entry.CgroupPath]
		assert.True(t, ok)
		delete(expectedPaths, entry.CgroupPath)
	}
	assert.Empty(t, expectedPaths)
}

func TestTransparentMemoryOffloading_GetAdvices_DyingMemcgReclaimInterval(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()
	defer mockey.UnPatchAll()

	tmo := &transparentMemoryOffloading{
		lastDyingCGReclaimTime: time.Now(),
	}

	mockey.Mock(os.ReadDir).IncludeCurrentGoRoutine().Return([]os.DirEntry{
		mockDirEntry{name: "offline-besteffort-0", isDir: true},
	}, nil).Build()

	result := tmo.GetAdvices()
	assert.Len(t, result.ExtraEntries, 0)
}

func TestTransparentMemoryOffloading_GetAdvices_DyingMemcgReclaimReadDirError(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()
	defer mockey.UnPatchAll()

	tmo := &transparentMemoryOffloading{}

	mockey.Mock(os.ReadDir).IncludeCurrentGoRoutine().Return(nil, errors.New("read failed")).Build()

	result := tmo.GetAdvices()
	assert.Len(t, result.ExtraEntries, 0)
}
