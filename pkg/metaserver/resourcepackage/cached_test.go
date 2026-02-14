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

package resourcepackage

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

// MockResourcePackageManager is a mock implementation of ResourcePackageManager
type MockResourcePackageManager struct {
	lock        sync.Mutex
	returnItems resourcepackage.NUMAResourcePackageItems
	returnErr   error
	called      int
}

func (m *MockResourcePackageManager) NodeResourcePackages(ctx context.Context) (resourcepackage.NUMAResourcePackageItems, error) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.called++
	return m.returnItems, m.returnErr
}

func (m *MockResourcePackageManager) ConvertNPDResourcePackages(npd *nodev1alpha1.NodeProfileDescriptor) (resourcepackage.NUMAResourcePackageItems, error) {
	return nil, nil
}

func (m *MockResourcePackageManager) setReturn(items resourcepackage.NUMAResourcePackageItems, err error) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.returnItems = items
	m.returnErr = err
}

func (m *MockResourcePackageManager) getCalled() int {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.called
}

func TestNewCachedResourcePackageManager(t *testing.T) {
	t.Parallel()
	mockRPM := &MockResourcePackageManager{}
	cachedMgr := NewCachedResourcePackageManager(mockRPM)
	assert.NotNil(t, cachedMgr)
	assert.Equal(t, mockRPM, cachedMgr.ResourcePackageManager)
}

func TestCachedResourcePackageManager_NodeResourcePackages(t *testing.T) {
	t.Parallel()
	mockRPM := &MockResourcePackageManager{}
	cachedMgr := NewCachedResourcePackageManager(mockRPM)

	expectedItems := make(resourcepackage.NUMAResourcePackageItems)
	expectedItems[0] = map[string]resourcepackage.ResourcePackageItem{
		"test": {},
	}

	// Directly set the cache since we are in the same package
	cachedMgr.resourcePackageMap = expectedItems

	// Test normal retrieval
	items, err := cachedMgr.NodeResourcePackages(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, expectedItems, items)

	// Verify mock was NOT called (should use cache)
	assert.Equal(t, 0, mockRPM.getCalled())
}

func TestCachedResourcePackageManager_Run(t *testing.T) {
	t.Parallel()
	mockRPM := &MockResourcePackageManager{}
	cachedMgr := NewCachedResourcePackageManager(mockRPM)

	expectedItems := make(resourcepackage.NUMAResourcePackageItems)
	expectedItems[0] = map[string]resourcepackage.ResourcePackageItem{
		"test": {},
	}
	mockRPM.setReturn(expectedItems, nil)

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Run calls updateResourcePackageMap synchronously once
	err := cachedMgr.Run(stopCh)
	assert.NoError(t, err)

	// Check if cache is updated
	cachedMgr.mux.RLock()
	items := cachedMgr.resourcePackageMap
	cachedMgr.mux.RUnlock()

	assert.Equal(t, expectedItems, items)
	assert.GreaterOrEqual(t, mockRPM.getCalled(), 1)
}

func TestCachedResourcePackageManager_updateResourcePackageMap(t *testing.T) {
	t.Parallel()
	mockRPM := &MockResourcePackageManager{}
	cachedMgr := NewCachedResourcePackageManager(mockRPM)

	// Case 1: Error from upstream
	mockRPM.setReturn(nil, fmt.Errorf("some error"))
	cachedMgr.updateResourcePackageMap()
	assert.Nil(t, cachedMgr.resourcePackageMap)

	// Case 2: Success update
	expectedItems := make(resourcepackage.NUMAResourcePackageItems)
	expectedItems[0] = map[string]resourcepackage.ResourcePackageItem{
		"test": {},
	}
	mockRPM.setReturn(expectedItems, nil)
	cachedMgr.updateResourcePackageMap()
	assert.Equal(t, expectedItems, cachedMgr.resourcePackageMap)

	// Case 3: No change (deep equal)
	// We call it again. The logic should just return.
	// We can't easily verify "return" happened without logs, but we verify state remains same.
	cachedMgr.updateResourcePackageMap()
	assert.Equal(t, expectedItems, cachedMgr.resourcePackageMap)

	// Case 4: Change
	newItems := make(resourcepackage.NUMAResourcePackageItems)
	newItems[0] = map[string]resourcepackage.ResourcePackageItem{
		"test2": {},
	}
	mockRPM.setReturn(newItems, nil)
	cachedMgr.updateResourcePackageMap()
	assert.Equal(t, newItems, cachedMgr.resourcePackageMap)
}

func TestCachedResourcePackageManager_Concurrency(t *testing.T) {
	t.Parallel()
	mockRPM := &MockResourcePackageManager{}
	cachedMgr := NewCachedResourcePackageManager(mockRPM)

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Setup initial data
	items1 := make(resourcepackage.NUMAResourcePackageItems)
	items1[0] = map[string]resourcepackage.ResourcePackageItem{"v1": {}}
	mockRPM.setReturn(items1, nil)

	// Initialize
	cachedMgr.Run(stopCh)

	// Concurrently read and update
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer routine
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			mockRPM.setReturn(make(resourcepackage.NUMAResourcePackageItems), nil)
			cachedMgr.updateResourcePackageMap()
			time.Sleep(1 * time.Millisecond)
		}
	}()

	// Reader routine
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_, err := cachedMgr.NodeResourcePackages(context.Background())
			assert.NoError(t, err)
			time.Sleep(1 * time.Millisecond)
		}
	}()

	wg.Wait()
}
