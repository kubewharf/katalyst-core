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

package dynamicpolicy

import (
	"fmt"
	"time"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/metricthreshold"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func (p *DynamicPolicy) collectNUMAMetrics() {
	_ = p.emitter.StoreInt64(util.MetricNameCollectNUMAMetrics, 1, metrics.MetricTypeNameRaw)
	collectTime := time.Now().UnixNano()
	machineState := p.state.GetMachineState()
	for numaID, subEntries := range p.numaMetrics {
		for _, resourceName := range []string{consts.MetricCPUUsageContainer, consts.MetricLoad1MinContainer} {
			value, err := p.getNUMAMeric(numaID, resourceName, machineState)
			if err != nil {
				general.Errorf("getNUMAMeric failed with error: %v", err)
				continue
			}

			if subEntries[resourceName] == nil {
				subEntries[resourceName] = strategy.CreateMetricRing(10)
			}

			subEntries[resourceName].Push(&strategy.MetricSnapshot{
				Info: strategy.MetricInfo{
					Value: value,
				},
				Time: collectTime,
			})

			general.Infof("numa: %d, resourceName: %s, value: %.2f, window_size: %d", numaID, resourceName, value, subEntries[resourceName].Len())
		}
	}
}

func (p *DynamicPolicy) getNUMAMeric(numa int, resourceName string, machineState state.NUMANodeMap) (float64, error) {
	if machineState == nil || machineState[numa] == nil {
		return 0.0, fmt.Errorf("invalid machineState")
	}

	snbEntries := machineState[numa].PodEntries.GetFilteredPodEntries(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedNUMABinding))

	sum := 0.0
	for podUID, containerEntries := range snbEntries {
		for containerName := range containerEntries {
			data, err := p.metaServer.GetContainerMetric(podUID, containerName, resourceName)
			if err != nil {
				return 0.0, fmt.Errorf("fetch metric for container failed: %v", err)
			}

			sum += data.Value
		}
	}

	return sum, nil
}

func (p *DynamicPolicy) getNUMAMetricThreshold(resourceName string) (float64, error) {
	if p.dynamicConfig == nil {
		return 0.0, fmt.Errorf("nil dynamicConf")
	} else if p.metaServer == nil {
		return 0.0, fmt.Errorf("nil metaServer")
	}

	metricThreshold := p.dynamicConfig.GetDynamicConfiguration().MetricThreshold
	if metricThreshold == nil {
		return 0.0, fmt.Errorf("nil metricThreshold")
	}

	cpuCodeNameInterface := p.metaServer.MetricsFetcher.GetByStringIndex(consts.MetricCPUCodeName)
	cpuCodeName, ok := cpuCodeNameInterface.(string)
	if !ok {
		general.Warningf("parse cpu code name %v failed", cpuCodeNameInterface)
		cpuCodeName = metricthreshold.DefaultCPUCodeName
	}

	isVMInterface := p.metaServer.MetricsFetcher.GetByStringIndex(consts.MetricInfoIsVM)
	isVM, ok := isVMInterface.(bool)
	if !ok {
		return 0.0, fmt.Errorf("parse is_vm failed")
	}
	switch resourceName {
	case consts.MetricCPUUsageContainer:
		if value, found := metricThreshold.Threshold[cpuCodeName][isVM]["cpu_usage_threshold"]; found {
			return value, nil
		} else {
			return 0.0, fmt.Errorf("threshold: %s isn't found cpuCodeName: %s and isVM: %v", "cpu_usage_threshold", cpuCodeName, isVM)
		}
	case consts.MetricLoad1MinContainer:
		if value, found := metricThreshold.Threshold[cpuCodeName][isVM]["cpu_load_threshold"]; found {
			return value, nil
		} else {
			return 0.0, fmt.Errorf("threshold: %s isn't found cpuCodeName: %s and isVM: %v", "cpu_usage_threshold", cpuCodeName, isVM)
		}
	default:
		return 0.0, fmt.Errorf("invalid resourceName: %s", resourceName)
	}
}

func (p *DynamicPolicy) isNUMAOverThreshold(numa int, threshold, request float64, resourceName string, machineState state.NUMANodeMap) (bool, error) {
	if machineState == nil || machineState[numa] == nil {
		return false, fmt.Errorf("invalid machineState")
	} else if p.numaMetrics[numa][resourceName] == nil {
		return false, fmt.Errorf("invalid machineState")
	}

	used, err := p.numaMetrics[numa][resourceName].AvgAfterTimestampWithCountBound(time.Now().UnixNano()-10*time.Minute.Nanoseconds(), 10)
	if err != nil {
		return false, fmt.Errorf("get numa metric failed: %v", err)
	}

	used += request

	allocatable := float64(machineState[numa].GetFilteredDefaultCPUSet(nil, nil).Difference(p.reservedCPUs).Size())

	if allocatable == 0 {
		return false, fmt.Errorf("invalid allocatable")
	}

	general.Infof("numa: %d, resourceName: %s, request: %.2f, used: %.2f, allocatable: %.2f, ratio: %.2f, threshold: %.2f, overThreshold: %v",
		numa, resourceName, request,
		used, allocatable, used/allocatable,
		threshold, (used/allocatable) >= threshold)

	return (used / allocatable) >= threshold, nil
}

func (p *DynamicPolicy) populateHintsByMetricPolicy(numaNodes []int,
	hints *pluginapi.ListOfTopologyHints, machineState state.NUMANodeMap, request float64,
) error {
	general.Infof("candidate numaNodes: %+v", numaNodes)
	tmpHints := make([]*pluginapi.TopologyHint, 0, len(numaNodes))
	for _, numaID := range numaNodes {
		prefer := true
		for _, resourceName := range []string{consts.MetricCPUUsageContainer, consts.MetricLoad1MinContainer} {
			threshold, err := p.getNUMAMetricThreshold(resourceName)
			if err != nil {
				return fmt.Errorf("get numa metric threshold for %s failed: %v", resourceName, err)
			}

			overThreshold, err := p.isNUMAOverThreshold(numaID, threshold, request, resourceName, machineState)
			if err != nil {
				return fmt.Errorf("get overThreshold for numa: %d failed: %v", numaID, err)
			}

			if overThreshold {
				prefer = false
				break
			}
		}

		if !prefer {
			_ = p.emitter.StoreInt64(util.MetricNameNUMAMetricOverThreshold, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "numa_id", Val: fmt.Sprintf("%d", numaID)})
		}

		general.Infof("numa: %d, prefer: %v", numaID, prefer)

		tmpHints = append(tmpHints, &pluginapi.TopologyHint{
			Nodes:     []uint64{uint64(numaID)},
			Preferred: prefer,
		})
	}

	hints.Hints = tmpHints
	return nil
}
