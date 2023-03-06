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

package spd

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd/checkpoint"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type spdInfo struct {
	// lastFetchRemoteTime records the timestamp of the last attempt to fetch
	// the remote spd, not the actual fetch
	lastFetchRemoteTime time.Time

	// lastGetTime records the timestamp of the last time GetSPD was called to
	// get spd, which is used for gc spd cache
	lastGetTime time.Time

	// spd is target spd
	spd *workloadapis.ServiceProfileDescriptor
}

// Cache is spd cache stores current
type Cache struct {
	sync.RWMutex

	expiredTime time.Duration

	manager checkpointmanager.CheckpointManager
	spdInfo map[string]*spdInfo
}

func NewSPDCache(manager checkpointmanager.CheckpointManager, expiredTime time.Duration) *Cache {
	cache := &Cache{
		spdInfo:     map[string]*spdInfo{},
		manager:     manager,
		expiredTime: expiredTime,
	}

	err := cache.restore()
	if err != nil {
		klog.Errorf("restore spd from local disk failed")
		return nil
	}

	return cache
}

// SetLastFetchRemoteTime set last fetch remote spd timestamp
func (s *Cache) SetLastFetchRemoteTime(key string, t time.Time) {
	s.Lock()
	defer s.Unlock()

	s.initSPDInfoWithoutLock(key)
	s.spdInfo[key].lastFetchRemoteTime = t
}

// GetLastFetchRemoteTime get last fetch remote spd timestamp
func (s *Cache) GetLastFetchRemoteTime(key string) time.Time {
	s.RLock()
	defer s.RUnlock()

	info, ok := s.spdInfo[key]
	if ok && info != nil {
		return info.lastFetchRemoteTime
	}

	return time.Time{}
}

// SetSPD set target spd to cache and checkpoint
func (s *Cache) SetSPD(key string, spd *workloadapis.ServiceProfileDescriptor) error {
	s.Lock()
	defer s.Unlock()

	// if current spd hash is empty, calculate and set it
	if util.GetSPDHash(spd) == "" {
		hash, err := util.CalculateSPDHash(spd)
		if err != nil {
			return err
		}
		util.SetSPDHash(spd, hash)
	}

	s.initSPDInfoWithoutLock(key)
	err := checkpoint.WriteSPD(s.manager, spd)
	if err != nil {
		return err
	}

	s.spdInfo[key].spd = spd
	return nil
}

// DeleteSPD delete target spd by namespace/name key
func (s *Cache) DeleteSPD(key string) error {
	s.Lock()
	defer s.Unlock()

	info, ok := s.spdInfo[key]
	if ok && info != nil {
		err := checkpoint.DeleteSPD(s.manager, info.spd)
		if err != nil {
			return err
		}
		delete(s.spdInfo, key)
	}

	return nil
}

// GetSPD gets target spd by namespace/name key
func (s *Cache) GetSPD(key string) *workloadapis.ServiceProfileDescriptor {
	s.RLock()
	defer s.RUnlock()

	s.initSPDInfoWithoutLock(key)
	// update last get spd time
	s.spdInfo[key].lastGetTime = time.Now()

	info, ok := s.spdInfo[key]
	if ok && info != nil {
		return info.spd
	}

	return nil
}

// Run to clear local unused spd
func (s *Cache) Run(ctx context.Context) {
	go wait.UntilWithContext(ctx, s.clearUnusedSPDs, s.expiredTime)
}

// restore all spd from disk at startup
func (s *Cache) restore() error {
	s.Lock()
	defer s.Unlock()

	spdList, err := checkpoint.LoadSPDs(s.manager)
	if err != nil {
		return fmt.Errorf("restore spd failed: %v", err)
	}

	now := time.Now()
	for _, spd := range spdList {
		key := native.GenerateUniqObjectNameKey(spd)
		s.initSPDInfoWithoutLock(key)
		s.spdInfo[key].spd = spd
		s.spdInfo[key].lastGetTime = now
	}

	return nil
}

// clearUnusedSPDs is to clear unused spd according to its lastGetSPDTime
func (s *Cache) clearUnusedSPDs(_ context.Context) {
	s.Lock()
	defer s.Unlock()

	now := time.Now()
	for key, info := range s.spdInfo {
		if info != nil && info.lastGetTime.Add(s.expiredTime).Before(now) {
			err := checkpoint.DeleteSPD(s.manager, info.spd)
			if err != nil {
				klog.Errorf("clear unused spd %s failed: %v", key, err)
				continue
			}
			delete(s.spdInfo, key)
		}
	}
}

func (s *Cache) initSPDInfoWithoutLock(key string) {
	info, ok := s.spdInfo[key]
	if !ok || info == nil {
		s.spdInfo[key] = &spdInfo{}
	}
}
