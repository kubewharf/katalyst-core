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

package client

import (
	"context"
	"strconv"
	"sync"
	"time"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type cachedCgroupClient struct {
	inner CgroupClient

	mu    sync.Mutex
	cache map[cacheKey]cacheEntry
	now   func() time.Time
}

type cacheKey struct {
	rel  string
	file string
}

type cacheEntry struct {
	value     string
	mtime     time.Time
	size      int64
	writtenAt time.Time
}

// NewCachedCgroupClient decorates inner with write-if-change caching.
func NewCachedCgroupClient(inner CgroupClient) CgroupClient {
	if inner == nil {
		panic("NewCachedCgroupClient: inner CgroupClient must not be nil")
	}
	return &cachedCgroupClient{
		inner: inner,
		cache: map[cacheKey]cacheEntry{},
		now:   time.Now,
	}
}

func (c *cachedCgroupClient) Version(ctx context.Context) CgroupVersion {
	return c.inner.Version(ctx)
}

func (c *cachedCgroupClient) ReadCPUSet(ctx context.Context, rel string) (machine.CPUSet, error) {
	return c.inner.ReadCPUSet(ctx, rel)
}

func (c *cachedCgroupClient) StatCPUSet(ctx context.Context, rel string) (time.Time, int64, error) {
	return c.inner.StatCPUSet(ctx, rel)
}

func (c *cachedCgroupClient) StatCgroupFile(ctx context.Context, rel, file string) (time.Time, int64, error) {
	return c.inner.StatCgroupFile(ctx, rel, file)
}

func (c *cachedCgroupClient) ReadCPUSetPartition(ctx context.Context, rel string) (cgcommon.CPUSetPartitionFlag, error) {
	return c.inner.ReadCPUSetPartition(ctx, rel)
}

func (c *cachedCgroupClient) ListRootChildren(ctx context.Context) ([]string, error) {
	return c.inner.ListRootChildren(ctx)
}

func (c *cachedCgroupClient) ListChildren(ctx context.Context, rel string) ([]string, error) {
	return c.inner.ListChildren(ctx, rel)
}

func (c *cachedCgroupClient) StatDir(ctx context.Context, rel string) (time.Time, error) {
	return c.inner.StatDir(ctx, rel)
}

func (c *cachedCgroupClient) AttachPID(ctx context.Context, rel string, pid int) error {
	return c.inner.AttachPID(ctx, rel, pid)
}

func (c *cachedCgroupClient) ApplyCPUSet(ctx context.Context, rel string, data *cgcommon.CPUSetData) error {
	if data == nil {
		return c.inner.ApplyCPUSet(ctx, rel, data)
	}
	skipCPUs := data.CPUs == "" || c.shouldSkip(ctx, rel, "cpuset.cpus", data.CPUs)
	skipMems := data.Mems == "" || c.shouldSkip(ctx, rel, "cpuset.mems", data.Mems)
	if skipCPUs && skipMems {
		return nil
	}
	if err := c.inner.ApplyCPUSet(ctx, rel, data); err != nil {
		if data.CPUs != "" {
			c.invalidate(rel, "cpuset.cpus")
		}
		if data.Mems != "" {
			c.invalidate(rel, "cpuset.mems")
		}
		return err
	}
	if data.CPUs != "" {
		c.recordWrite(ctx, rel, "cpuset.cpus", data.CPUs)
	}
	if data.Mems != "" {
		c.recordWrite(ctx, rel, "cpuset.mems", data.Mems)
	}
	return nil
}

func (c *cachedCgroupClient) ApplyCPUSetPartition(ctx context.Context, rel string, flag cgcommon.CPUSetPartitionFlag) error {
	value := string(flag)
	if c.shouldSkip(ctx, rel, "cpuset.cpus.partition", value) {
		return nil
	}
	if err := c.inner.ApplyCPUSetPartition(ctx, rel, flag); err != nil {
		c.invalidate(rel, "cpuset.cpus.partition")
		return err
	}
	c.recordWrite(ctx, rel, "cpuset.cpus.partition", value)
	return nil
}

func (c *cachedCgroupClient) ApplyCPU(ctx context.Context, rel string, data *cgcommon.CPUData) error {
	if data == nil {
		return c.inner.ApplyCPU(ctx, rel, data)
	}
	fields := cpuDataFields(data)
	allSkip := true
	for _, f := range fields {
		if !c.shouldSkip(ctx, rel, f.file, f.value) {
			allSkip = false
			break
		}
	}
	if allSkip && len(fields) > 0 {
		return nil
	}
	if err := c.inner.ApplyCPU(ctx, rel, data); err != nil {
		for _, f := range fields {
			c.invalidate(rel, f.file)
		}
		return err
	}
	for _, f := range fields {
		c.recordWrite(ctx, rel, f.file, f.value)
	}
	return nil
}

func (c *cachedCgroupClient) ApplySchedLoadBalance(ctx context.Context, rel string, enabled bool) error {
	value := "0"
	if enabled {
		value = "1"
	}
	if c.shouldSkip(ctx, rel, "cpuset.sched_load_balance", value) {
		return nil
	}
	if err := c.inner.ApplySchedLoadBalance(ctx, rel, enabled); err != nil {
		c.invalidate(rel, "cpuset.sched_load_balance")
		return err
	}
	c.recordWrite(ctx, rel, "cpuset.sched_load_balance", value)
	return nil
}

func (c *cachedCgroupClient) shouldSkip(ctx context.Context, rel, file, want string) bool {
	c.mu.Lock()
	entry, ok := c.cache[cacheKey{rel: rel, file: file}]
	c.mu.Unlock()
	if !ok {
		return false
	}
	if entry.value != want {
		return false
	}
	mtime, size, err := c.statFile(ctx, rel, file)
	if err != nil {
		c.invalidate(rel, file)
		return false
	}
	return mtime.Equal(entry.mtime) && size == entry.size
}

func (c *cachedCgroupClient) recordWrite(ctx context.Context, rel, file, value string) {
	mtime, size, err := c.statFile(ctx, rel, file)
	if err != nil {
		c.invalidate(rel, file)
		return
	}
	c.mu.Lock()
	c.cache[cacheKey{rel: rel, file: file}] = cacheEntry{
		value:     value,
		mtime:     mtime,
		size:      size,
		writtenAt: c.now(),
	}
	c.mu.Unlock()
}

func (c *cachedCgroupClient) invalidate(rel, file string) {
	c.mu.Lock()
	delete(c.cache, cacheKey{rel: rel, file: file})
	c.mu.Unlock()
}

func (c *cachedCgroupClient) Prune(activeRels map[string]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.cache {
		if _, ok := activeRels[k.rel]; !ok {
			delete(c.cache, k)
		}
	}
}

func (c *cachedCgroupClient) statFile(ctx context.Context, rel, file string) (time.Time, int64, error) {
	switch file {
	case "cpuset.cpus":
		return c.inner.StatCPUSet(ctx, rel)
	case "cpuset.mems", "cpuset.cpus.partition", "cpuset.sched_load_balance":
		return c.inner.StatCgroupFile(ctx, rel, file)
	}
	mtime, err := c.inner.StatDir(ctx, rel)
	return mtime, 0, err
}

type cachedApplyFieldValue struct {
	file  string
	value string
}

func cpuDataFields(data *cgcommon.CPUData) []cachedApplyFieldValue {
	if data == nil {
		return nil
	}
	var out []cachedApplyFieldValue
	if data.Shares != 0 {
		out = append(out, cachedApplyFieldValue{file: "cpu.shares", value: strconv.FormatUint(data.Shares, 10)})
	}
	if data.CpuPeriod != 0 {
		out = append(out, cachedApplyFieldValue{file: "cpu.cfs_period_us", value: strconv.FormatUint(data.CpuPeriod, 10)})
	}
	if data.CpuQuota != 0 {
		out = append(out, cachedApplyFieldValue{file: "cpu.cfs_quota_us", value: strconv.FormatInt(data.CpuQuota, 10)})
	}
	if data.CpuIdlePtr != nil {
		v := "0"
		if *data.CpuIdlePtr {
			v = "1"
		}
		out = append(out, cachedApplyFieldValue{file: "cpu.idle", value: v})
	}
	if data.CpuBurst != nil {
		out = append(out, cachedApplyFieldValue{file: "cpu.cfs_burst_us", value: strconv.FormatUint(*data.CpuBurst, 10)})
	}
	return out
}
