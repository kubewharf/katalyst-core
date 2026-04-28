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

package region

import (
	"fmt"
	"strconv"

	"k8s.io/klog/v2"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/provision"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type QoSRegionEmptyNUMA struct {
	*QoSRegionBase
}

func NewQoSRegionEmptyNUMA(ci *types.ContainerInfo, conf *config.Configuration, extraConf interface{}, numaID int,
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) QoSRegion {
	isNumaBinding := numaID != commonstate.FakedNUMAID
	r := &QoSRegionEmptyNUMA{
		QoSRegionBase: NewQoSRegionBase(
			string(configapi.QoSRegionEmptyNUMA)+types.RegionNameSeparator+strconv.Itoa(numaID),
			"none",
			configapi.QoSRegionEmptyNUMA,
			conf,
			extraConf,
			isNumaBinding,
			false,
			metaReader,
			metaServer,
			emitter,
		),
	}

	if isNumaBinding {
		r.cpusetMems = machine.NewCPUSet(numaID)
	}

	// Dummy regions are only used for empty NUMAs; they still need basic indicators so that
	// provision policies (e.g. dynamic-quota) can work when the config enables them.
	r.indicatorCurrentGetters = map[string]types.IndicatorCurrentGetter{
		string(workloadapis.ServiceSystemIndicatorNameCPUUsageRatio): r.getCPUUsageRatio,
	}

	return r
}

func (r *QoSRegionEmptyNUMA) getEffectiveControlKnobs() types.ControlKnob {
	quota, _, err := r.getEffectiveReclaimResource()
	if err != nil {
		klog.Errorf("[qosaware-cpu] failed to get effective reclaim resource, ignore it: %v", err)
	} else if r.isNumaBinding && quota > 0 {
		return types.ControlKnob{
			configapi.ControlKnobReclaimedCoresCPUQuota: {
				Value:  quota,
				Action: types.ControlKnobActionNone,
			},
		}
	}

	reclaimedCPUSize := 0
	if reclaimedInfo, ok := r.metaReader.GetPoolInfo(commonstate.PoolNameReclaim); ok {
		for _, numaID := range r.cpusetMems.ToSliceInt() {
			reclaimedCPUSize += reclaimedInfo.TopologyAwareAssignments[numaID].Size()
		}
	}

	cpuRequirement := r.ResourceUpperBound + r.ReservedForReclaim - float64(reclaimedCPUSize)

	return types.ControlKnob{
		configapi.ControlKnobNonReclaimedCPURequirement: {
			Value:  float64(cpuRequirement),
			Action: types.ControlKnobActionNone,
		},
	}
}

func (r *QoSRegionEmptyNUMA) TryUpdateProvision() {
	r.Lock()
	defer r.Unlock()

	r.ControlEssentials = types.ControlEssentials{
		ControlKnobs:   r.getEffectiveControlKnobs(),
		ReclaimOverlap: r.AllowSharedCoresOverlapReclaimedCores,
	}

	indicators, err := r.getIndicators()
	if err != nil {
		klog.Warningf("[qosaware-cpu] failed to get indicators, ignore it: %v", err)
	} else {
		r.ControlEssentials.Indicators = indicators
		general.Infof("indicators %v for region %v", indicators, r.name)
	}

	ctx := provision.PolicyContext{
		ResourceEssentials: r.ResourceEssentials,
		ControlEssentials:  r.ControlEssentials,
		PodSet:             r.podSet,
		CpusetMems:         r.cpusetMems,
		IsNUMABinding:      r.isNumaBinding,
		RegulatorOptions:   r.cpuRegulatorOptions,
	}
	if err := r.pm.Update(ctx); err != nil {
		general.ErrorS(err, " failed to update policy", "regionName", r.name)
	}
}

func (r *QoSRegionEmptyNUMA) getCPUUsageRatio() (float64, error) {
	numaIDs := r.cpusetMems.ToSliceInt()
	if len(numaIDs) != 1 {
		return 0, fmt.Errorf("invalid empty NUMA region binding numas: %v", r.cpusetMems.String())
	}
	numaID := numaIDs[0]

	// FIXME: regard all cpus as reclaimed_cores affinity
	cpuSet := r.metaServer.NUMAToCPUs[numaID]
	if cpuSet.Size() <= 0 {
		return 0, fmt.Errorf("invalid numa cpu size %d for numa %d", cpuSet.Size(), numaID)
	}

	usageRatio := r.metaServer.AggregateCoreMetric(cpuSet, pkgconsts.MetricCPUUsageRatio, metric.AggregatorAvg)
	return usageRatio.Value, nil
}
