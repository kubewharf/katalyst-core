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
	"strconv"

	"go.uber.org/atomic"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	MemoryGuard = "memory-guard"
)

type memoryGuard struct {
	metaReader                    metacache.MetaReader
	metaServer                    *metaserver.MetaServer
	emitter                       metrics.MetricEmitter
	reclaimRelativeRootCgroupPath string
	reclaimMemoryLimit            *atomic.Int64
	minCriticalWatermark          int64
}

func NewMemoryGuard(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) MemoryAdvisorPlugin {
	return &memoryGuard{
		metaReader:                    metaReader,
		metaServer:                    metaServer,
		emitter:                       emitter,
		reclaimRelativeRootCgroupPath: conf.ReclaimRelativeRootCgroupPath,
		reclaimMemoryLimit:            atomic.NewInt64(-1),
		minCriticalWatermark:          conf.MinCriticalWatermark,
	}
}

func (mg *memoryGuard) Reconcile(status *types.MemoryPressureStatus) error {
	memoryTotal, err := mg.metaServer.GetNodeMetric(consts.MetricMemTotalSystem)
	if err != nil {
		return err
	}

	memoryFree, err := mg.metaReader.GetNodeMetric(consts.MetricMemFreeSystem)
	if err != nil {
		return err
	}

	memoryCache, err := mg.metaReader.GetNodeMetric(consts.MetricMemPageCacheSystem)
	if err != nil {
		return err
	}

	memoryBuffer, err := mg.metaReader.GetNodeMetric(consts.MetricMemBufferSystem)
	if err != nil {
		return err
	}

	scaleFactor, err := mg.metaReader.GetNodeMetric(consts.MetricMemScaleFactorSystem)
	if err != nil {
		return err
	}

	criticalWatermark := general.MaxFloat64(float64(mg.minCriticalWatermark*int64(mg.metaServer.NumNUMANodes)), memoryTotal.Value*scaleFactor.Value/10000)
	buffer := memoryFree.Value + memoryCache.Value + memoryBuffer.Value - criticalWatermark
	if buffer < 0 {
		buffer = 0
	}

	reclaimGroupRss, err := mg.metaReader.GetCgroupMetric(mg.reclaimRelativeRootCgroupPath, consts.MetricMemRssCgroup)
	if err != nil {
		return err
	}

	reclaimGroupUsed, err := mg.metaReader.GetCgroupMetric(mg.reclaimRelativeRootCgroupPath, consts.MetricMemUsageCgroup)
	if err != nil {
		return err
	}

	reclaimMemoryLimit := general.MaxFloat64(reclaimGroupUsed.Value, reclaimGroupRss.Value+buffer)

	general.InfoS("memory details",
		"system total", general.FormatMemoryQuantity(memoryTotal.Value),
		"system free", general.FormatMemoryQuantity(memoryFree.Value),
		"system cache", general.FormatMemoryQuantity(memoryCache.Value),
		"system buffer", general.FormatMemoryQuantity(memoryBuffer.Value),
		"system scaleFactor", general.FormatMemoryQuantity(scaleFactor.Value),
		"criticalWatermark", general.FormatMemoryQuantity(criticalWatermark),
		"buffer", general.FormatMemoryQuantity(buffer),
		"reclaim cgroup rss", general.FormatMemoryQuantity(reclaimGroupRss.Value),
		"reclaim cgroup used", general.FormatMemoryQuantity(reclaimGroupUsed.Value),
	)

	mg.reclaimMemoryLimit.Store(int64(reclaimMemoryLimit))

	return nil
}

func (mg *memoryGuard) GetAdvices() types.InternalMemoryCalculationResult {
	result := types.InternalMemoryCalculationResult{
		ExtraEntries: []types.ExtraMemoryAdvices{
			{
				CgroupPath: mg.reclaimRelativeRootCgroupPath,
				Values:     map[string]string{string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): strconv.FormatInt(mg.reclaimMemoryLimit.Load(), 10)},
			},
		},
	}

	return result
}
