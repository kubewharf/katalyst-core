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

package resourcepoolvalidator

import (
	"context"
	"sync"
	"time"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// DefaultCacheInterval is the default interval for background cache refresh.
const DefaultCacheInterval = 15 * time.Second

// CachedResourcePoolsProvider wraps a ResourcePoolsProvider with an
// asynchronously-refreshed cache. The hot-path NodeResourcePools call never
// blocks on the inner provider; it always returns the latest cached data.
//
// Call Run(stopCh) to start the background refresh goroutine. The goroutine
// exits when stopCh is closed.
type CachedResourcePoolsProvider struct {
	inner    ResourcePoolsProvider
	mu       sync.RWMutex
	cached   map[int]map[string]nodev1alpha1.ResourcePool
	interval time.Duration
}

// NewCachedResourcePoolsProvider creates a CachedResourcePoolsProvider that
// wraps the given inner provider. It performs an initial synchronous fetch to
// populate the cache so that the first NodeResourcePools call returns valid
// data. If the initial fetch fails the cache starts empty and the background
// goroutine will retry on each tick.
func NewCachedResourcePoolsProvider(inner ResourcePoolsProvider) *CachedResourcePoolsProvider {
	c := &CachedResourcePoolsProvider{
		inner:    inner,
		interval: DefaultCacheInterval,
	}
	if cached, err := inner.NodeResourcePools(context.Background()); err == nil {
		c.cached = cached
	}
	return c
}

// NodeResourcePools returns the cached resource pool data. It never blocks on
// the inner provider.
func (c *CachedResourcePoolsProvider) NodeResourcePools(_ context.Context) (map[int]map[string]nodev1alpha1.ResourcePool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cached, nil
}

// Run starts the background goroutine that periodically refreshes the cache.
// It blocks until stopCh is closed. Typically launched as:
//
//	go cachedProvider.Run(p.stopCh)
func (c *CachedResourcePoolsProvider) Run(stopCh <-chan struct{}) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			c.refresh()
		}
	}
}

// refresh fetches the latest data from the inner provider and updates the
// cache. On error the old cache is preserved.
func (c *CachedResourcePoolsProvider) refresh() {
	fresh, err := c.inner.NodeResourcePools(context.Background())
	if err != nil {
		general.Warningf("CachedResourcePoolsProvider: refresh failed, keeping old cache: %v", err)
		return
	}
	c.mu.Lock()
	c.cached = fresh
	c.mu.Unlock()
}
