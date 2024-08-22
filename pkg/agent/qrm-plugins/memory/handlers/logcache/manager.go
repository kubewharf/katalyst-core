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

	"k8s.io/apimachinery/pkg/util/errors"

	memconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/consts"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func canFilePathMatch(filePath string, patternStrings []string) bool {
	for _, str := range patternStrings {
		re := regexp.MustCompile(str)
		if re.MatchString(filePath) {
			return true
		}
	}
	return false
}

type fileCacheEvictionManager struct {
	metaServer *metaserver.MetaServer

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
	return canFilePathMatch(filePath, e.fileFilters)
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

func (e *fileCacheEvictionManager) getCachedMemoryInGB() uint64 {
	m, err := e.metaServer.GetNodeMetric(consts.MetricMemPageCacheSystem)
	if err != nil {
		return 0
	}

	cachedBytes := uint64(m.Value)
	cachedGB := cachedBytes >> 30
	return cachedGB
}

func (e *fileCacheEvictionManager) waitForNodeMetricsSync() {
	time.Sleep(time.Second * 30)
}

func (e *fileCacheEvictionManager) doEviction() error {
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
		return nil
	}

	beforeEvictedGB := e.getCachedMemoryInGB()

	var errList []error
	for _, path := range e.pathList {
		if err := e.evictWalk(path); err != nil {
			general.Errorf("walk path %s error: %v", path, err)
			errList = append(errList, err)
		}
	}

	elapsedTime := time.Since(now)

	e.waitForNodeMetricsSync()
	afterEvictedGB := e.getCachedMemoryInGB()
	var evictedGB uint64 = 0
	if beforeEvictedGB > afterEvictedGB {
		evictedGB = beforeEvictedGB - afterEvictedGB
	}

	e.lastEvictTime = &now
	e.determineNextInterval(evictedGB, elapsedTime)

	general.Infof("file cache eviction finished at %v, cost %v, cached memory from %d GB to %d GB, evicted %d GB, will run at %v later",
		e.lastEvictTime.String(), elapsedTime.String(), beforeEvictedGB, afterEvictedGB, evictedGB, e.curInterval)

	return errors.NewAggregate(errList)
}

func (e *fileCacheEvictionManager) EvictLogCache(_ *coreconfig.Configuration,
	_ interface{}, _ *dynamicconfig.DynamicAgentConfiguration, emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
) {
	var err error
	defer func() {
		_ = general.UpdateHealthzStateByError(memconsts.EvictLogCache, err)
	}()

	err = e.doEviction()
}

func NewManager(conf *coreconfig.Configuration, metaServer *metaserver.MetaServer) Manager {
	e := &fileCacheEvictionManager{
		metaServer:      metaServer,
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
