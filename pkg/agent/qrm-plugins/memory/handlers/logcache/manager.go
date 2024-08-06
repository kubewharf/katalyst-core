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

package logcache

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"time"

	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func CanFilePathMatch(filePath string, patternStrings []string) bool {
	for _, str := range patternStrings {
		re := regexp.MustCompile(str)
		if re.MatchString(filePath) {
			return true
		}
	}
	return false
}

type fileCacheEvictionManager struct {
	highThresholdGB uint64
	lowThresholdGB  uint64

	minInterval time.Duration
	maxInterval time.Duration
	curInterval time.Duration

	lastEvictTime *time.Time

	pathList []string

	fileFilters []string
}

func (e *fileCacheEvictionManager) shouldEvictFile(filePath string) bool {
	return CanFilePathMatch(filePath, e.fileFilters)
}

func (e *fileCacheEvictionManager) evictWalk(path string) error {
	return filepath.Walk(path, func(path string, file fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !file.Mode().IsRegular() {
			return nil
		}

		if e.shouldEvictFile(path) && file.Size() > 0 {
			if err = EvictFileCache(path, file.Size()); err != nil {
				return err
			}
		}
		return nil
	})
}

func (e *fileCacheEvictionManager) determineNextInterval(evictedGB uint64, elapsedTime time.Duration) {
	interval := e.curInterval
	if evictedGB > e.highThresholdGB {
		interval -= e.minInterval
	} else if evictedGB < e.lowThresholdGB {
		interval += e.minInterval
	}
	if interval < elapsedTime {
		interval = elapsedTime * 4
	}

	if interval < e.minInterval {
		interval = e.minInterval
	} else if interval > e.maxInterval {
		interval = e.maxInterval
	}

	e.curInterval = interval
}

func (e *fileCacheEvictionManager) getMemInfoCachedGB() uint64 {
	cachedBytes, err := machine.GetMemoryCached()
	if err != nil {
		general.Errorf("get cached memory error: %v", err)
		return 0
	}
	return cachedBytes / 1024 / 1024 / 1024
}

func (e *fileCacheEvictionManager) doEviction() {
	now := time.Now()

	runAtOnce := true
	if e.lastEvictTime != nil {
		lastEvictTime := *e.lastEvictTime
		expectedToRunAt := lastEvictTime.Add(e.curInterval)
		if now.Before(expectedToRunAt) {
			runAtOnce = false
		}
	}

	if !runAtOnce {
		return
	}

	beforeEvictedGB := e.getMemInfoCachedGB()

	for _, path := range e.pathList {
		general.Infof("evict walk path %s", path)
		if err := e.evictWalk(path); err != nil {
			general.Errorf("walk path %s error: %v", path, err)
		}
	}

	afterEvictedGB := e.getMemInfoCachedGB()
	var evictedGB uint64 = 0
	if beforeEvictedGB > afterEvictedGB {
		evictedGB = beforeEvictedGB - afterEvictedGB
	}
	elapsedTime := time.Since(now)

	e.lastEvictTime = &now
	e.determineNextInterval(evictedGB, elapsedTime)

	general.Infof("file cache eviction finished at %v, cost %v, cached memory from %d GB to %d GB, evicted %d GB, will run at %v later",
		e.lastEvictTime.String(), elapsedTime.String(), beforeEvictedGB, afterEvictedGB, evictedGB, e.curInterval)
}

func (e *fileCacheEvictionManager) EvictLogCache(_ *coreconfig.Configuration,
	_ interface{}, _ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer) {
	e.doEviction()
}

func NewManager(conf *coreconfig.Configuration) Manager {
	e := &fileCacheEvictionManager{
		highThresholdGB: conf.HighThreshold,
		lowThresholdGB:  conf.LowThreshold,
		minInterval:     conf.MinInterval,
		maxInterval:     conf.MaxInterval,
		curInterval:     conf.MinInterval,
		lastEvictTime:   nil,
		pathList:        conf.PathList,
		fileFilters:     conf.FileFilters,
	}

	general.Infof("log cache manager: highThreshold: %v, lowThreshold: %v, minInterval: %v, maxInterval: %v, pathList: %v",
		conf.HighThreshold, conf.LowThreshold, conf.MinInterval, conf.MaxInterval, conf.PathList)
	return e
}
