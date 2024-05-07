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

	// penaltyForFetchingRemoteTime records the penalty of fetching remote spd if it was deleted
	penaltyForFetchingRemoteTime time.Duration

	// retryCount records the count of fetching remote deleted spd
	retryCount int64

	// lastGetTime records the timestamp of the last time GetSPD was called to
	// get spd, which is used for gc spd cache
	lastGetTime time.Time

	// spd is target spd
	spd *workloadapis.ServiceProfileDescriptor
}

// Cache is spd cache stores current
type Cache struct {
	sync.RWMutex

	skipCorruptionError bool
	expiredTime         time.Duration
	cacheTTL            time.Duration
	jitterFactor        float64
	maxRetryCount       int64

	manager checkpointmanager.CheckpointManager
	spdInfo map[string]*spdInfo
}

func NewSPDCache(manager checkpointmanager.CheckpointManager, skipCorruptionError bool,
	cacheTTL, expiredTime time.Duration, maxRetryCount int64, jitterFactor float64,
) (*Cache, error) {
	cache := &Cache{
		spdInfo:             map[string]*spdInfo{},
		manager:             manager,
		skipCorruptionError: skipCorruptionError,
		expiredTime:         expiredTime,
		cacheTTL:            cacheTTL,
		jitterFactor:        jitterFactor,
		maxRetryCount:       maxRetryCount,
	}

	err := cache.restore()
	if err != nil {
		klog.Errorf("restore spd from local disk failed, %v", err)
		return nil, err
	}

	return cache, nil
}

// SetLastFetchRemoteTime set last fetch remote spd timestamp
func (s *Cache) SetLastFetchRemoteTime(key string, t time.Time) {
	s.Lock()
	defer s.Unlock()

	s.initSPDInfoWithoutLock(key)
	s.spdInfo[key].lastFetchRemoteTime = t
}

// GetNextFetchRemoteTime get next fetch remote spd timestamp
func (s *Cache) GetNextFetchRemoteTime(key string, now time.Time, skipBackoff bool) time.Time {
	s.RLock()
	defer s.RUnlock()

	info, ok := s.spdInfo[key]
	if ok && info != nil {
		if !skipBackoff && info.penaltyForFetchingRemoteTime > 0 {
			return info.lastFetchRemoteTime.Add(info.penaltyForFetchingRemoteTime)
		}

		nextFetchRemoteTime := info.lastFetchRemoteTime
		// to avoid burst remote request when lastFetchRemoteTime is too old, add some random
		if !info.lastFetchRemoteTime.IsZero() && info.lastFetchRemoteTime.Add(time.Duration((1+s.jitterFactor)*float64(s.cacheTTL))).Before(now) {
			nextFetchRemoteTime = now.Add(-wait.Jitter(s.cacheTTL, s.jitterFactor))
		}

		nextFetchRemoteTime = nextFetchRemoteTime.Add(wait.Jitter(s.cacheTTL, s.jitterFactor))
		// If no one tries to get this spd for a long time, a penalty from lastGetTime to lastFetchRemoteTime will be added,
		// which will linearly increase the period of accessing the remote, thereby reducing the frequency of accessing the api-server
		if !skipBackoff && info.lastFetchRemoteTime.After(info.lastGetTime) {
			nextFetchRemoteTime = nextFetchRemoteTime.Add(info.lastFetchRemoteTime.Sub(info.lastGetTime))
		}

		return nextFetchRemoteTime
	}

	return time.Time{}
}

// ListAllSPDKeys list all spd key
func (s *Cache) ListAllSPDKeys() []string {
	s.RLock()
	defer s.RUnlock()

	spdKeys := make([]string, 0, len(s.spdInfo))
	for key := range s.spdInfo {
		spdKeys = append(spdKeys, key)
	}

	return spdKeys
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
	util.SetLastFetchTime(spd, s.spdInfo[key].lastFetchRemoteTime)
	err := checkpoint.WriteSPD(s.manager, spd)
	if err != nil {
		return err
	}

	s.spdInfo[key].spd = spd
	s.spdInfo[key].penaltyForFetchingRemoteTime = 0
	s.spdInfo[key].retryCount = 0
	return nil
}

// DeleteSPD delete target spd by namespace/name key
func (s *Cache) DeleteSPD(key string) error {
	s.Lock()
	defer s.Unlock()

	info, ok := s.spdInfo[key]
	if ok && info != nil {
		// update the penalty of fetching remote spd if it was already deleted
		if info.retryCount < s.maxRetryCount {
			info.retryCount += 1
			info.penaltyForFetchingRemoteTime += wait.Jitter(s.cacheTTL, s.jitterFactor)
		} else {
			info.penaltyForFetchingRemoteTime = s.expiredTime
		}

		err := checkpoint.DeleteSPD(s.manager, info.spd)
		if err != nil {
			return err
		}
	}

	return nil
}

// GetSPD gets target spd by namespace/name key
func (s *Cache) GetSPD(key string, updateLastGetTime bool) *workloadapis.ServiceProfileDescriptor {
	s.Lock()
	defer s.Unlock()

	s.initSPDInfoWithoutLock(key)

	if updateLastGetTime {
		// update last get spd time
		s.spdInfo[key].lastGetTime = time.Now()
	}

	info, ok := s.spdInfo[key]
	if ok && info != nil {
		return info.spd
	}

	return nil
}

// Run to clear local unused spd
func (s *Cache) Run(ctx context.Context) {
	// sleep with cacheTTL to wait the last get time update
	// for each spd
	time.Sleep(s.cacheTTL)
	wait.UntilWithContext(ctx, s.clearUnusedSPDs, s.expiredTime)
}

// restore all spd from disk at startup
func (s *Cache) restore() error {
	s.Lock()
	defer s.Unlock()

	spdList, err := checkpoint.LoadSPDs(s.manager, s.skipCorruptionError)
	if err != nil {
		return fmt.Errorf("restore spd failed: %v", err)
	}

	for _, spd := range spdList {
		if spd == nil {
			continue
		}
		key := native.GenerateUniqObjectNameKey(spd)
		s.initSPDInfoWithoutLock(key)
		s.spdInfo[key].spd = spd
		s.spdInfo[key].lastFetchRemoteTime = util.GetLastFetchTime(spd)
		klog.Infof("restore spd cache %s: %+v", key, s.spdInfo[key])
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
			klog.Infof("clear spd cache %s", key)
		}
	}
}

func (s *Cache) initSPDInfoWithoutLock(key string) {
	info, ok := s.spdInfo[key]
	if !ok || info == nil {
		s.spdInfo[key] = &spdInfo{}
	}
}
