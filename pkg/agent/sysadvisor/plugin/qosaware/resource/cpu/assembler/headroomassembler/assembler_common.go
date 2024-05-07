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

package headroomassembler

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/config"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type HeadroomAssemblerCommon struct {
	conf *config.Configuration

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

func NewHeadroomAssemblerCommon(conf *config.Configuration, _ interface{}, _ *map[string]region.QoSRegion,
	_ *map[int]int, _ *map[int]int, _ *machine.CPUSet, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) HeadroomAssembler {
	return &HeadroomAssemblerCommon{
		conf:       conf,
		metaReader: metaReader,
		metaServer: metaServer,
		emitter:    emitter,
	}
}

func (ha *HeadroomAssemblerCommon) GetHeadroom() (resource.Quantity, error) {
	dynamicConfig := ha.conf.GetDynamicConfiguration()

	// return zero when reclaim is disabled
	if !dynamicConfig.EnableReclaim {
		return *resource.NewQuantity(0, resource.DecimalSI), nil
	}

	reclaimedMetrics, err := ha.getPoolMetrics(state.PoolNameReclaim)
	if err != nil {
		return resource.Quantity{}, err
	}

	// if util based cpu headroom disable, just return total reclaim pool size as headroom
	if !dynamicConfig.CPUUtilBasedConfiguration.Enable {
		return *resource.NewQuantity(int64(reclaimedMetrics.poolSize), resource.DecimalSI), nil
	}

	return ha.getUtilBasedHeadroom(dynamicConfig, reclaimedMetrics)
}

type poolMetrics struct {
	coreAvgUtil float64
	poolSize    int
}

// getPoolMetrics get reclaimed pool metrics, including the average utilization of each core in
// the reclaimed pool and the size of the pool
func (ha *HeadroomAssemblerCommon) getPoolMetrics(poolName string) (*poolMetrics, error) {
	reclaimedInfo, ok := ha.metaReader.GetPoolInfo(poolName)
	if !ok {
		return nil, fmt.Errorf("failed get reclaim pool info")
	}

	cpuSet := reclaimedInfo.TopologyAwareAssignments.MergeCPUSet()
	m := ha.metaServer.AggregateCoreMetric(cpuSet, pkgconsts.MetricCPUUsageRatio, metric.AggregatorAvg)
	return &poolMetrics{
		coreAvgUtil: m.Value,
		poolSize:    cpuSet.Size(),
	}, nil
}
