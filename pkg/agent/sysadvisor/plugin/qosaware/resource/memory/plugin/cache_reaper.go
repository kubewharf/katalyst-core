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
	"sync"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	CacheReaper = "cache-reaper"
)

type cacheReaper struct {
	conf                  *config.Configuration
	mutex                 sync.RWMutex
	metaReader            metacache.MetaReader
	metaServer            *metaserver.MetaServer
	emitter               metrics.MetricEmitter
	containersToReapCache map[consts.PodContainerName]*types.ContainerInfo
}

func NewCacheReaper(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) MemoryAdvisorPlugin {
	return &cacheReaper{
		conf:                  conf,
		metaReader:            metaReader,
		metaServer:            metaServer,
		containersToReapCache: make(map[consts.PodContainerName]*types.ContainerInfo),
		emitter:               emitter,
	}
}

func (cp *cacheReaper) selectContainers(containers []*types.ContainerInfo, cacheToReap resource.Quantity, numaID int, metricName string) []*types.ContainerInfo {
	general.NewMultiSorter(func(s1, s2 interface{}) int {
		c1, c2 := s1.(*types.ContainerInfo), s2.(*types.ContainerInfo)
		c1Metric, c1Err := helper.IgnoreMetricValueExpired(helper.GetContainerMetric(cp.metaServer.MetricsFetcher, cp.emitter, c1.PodUID, c1.ContainerName, metricName, numaID))
		c2Metric, c2Err := helper.IgnoreMetricValueExpired(helper.GetContainerMetric(cp.metaServer.MetricsFetcher, cp.emitter, c2.PodUID, c2.ContainerName, metricName, numaID))
		if c1Err != nil || c2Err != nil {
			return general.CmpError(c1Err, c2Err)
		}

		// prioritize evicting the pod whose metric value is greater
		return general.CmpFloat64(c1Metric, c2Metric)
	}).Sort(types.NewContainerSourceImpList(containers))

	selected := make([]*types.ContainerInfo, 0)
	sum := resource.NewQuantity(0, resource.BinarySI)

	for _, ci := range containers {
		metric, err := helper.IgnoreMetricValueExpired(helper.GetContainerMetric(cp.metaServer.MetricsFetcher, cp.emitter, ci.PodUID, ci.ContainerName, metricName, numaID))
		if err != nil {
			general.Errorf("failed to get metric %v for pod %v/%v container %v on numa %v err %v", metricName, ci.PodNamespace, ci.PodName, ci.ContainerName, numaID, err)
			continue
		}
		selected = append(selected, ci)
		sum.Add(*resource.NewQuantity(int64(metric), resource.BinarySI))
		if sum.Cmp(cacheToReap) > 0 {
			break
		}
	}
	return selected
}

func (cp *cacheReaper) reclaimedContainersFilter(ci *types.ContainerInfo, numaID int, minCacheUtilizationThreshold float64) bool {
	if ci == nil || ci.QoSLevel != apiconsts.PodAnnotationQoSLevelReclaimedCores || ci.ContainerType != v1alpha1.ContainerType_MAIN {
		return false
	}

	var (
		total metric.MetricData
		cache metric.MetricData
		err   error
	)

	if numaID < 0 {
		total, err = cp.metaServer.GetNodeMetric(consts.MetricMemTotalSystem)
		if err != nil {
			general.ErrorS(err, "failed to get MetricMemTotalSystem")
			return true
		}
		cache, err = cp.metaServer.GetContainerMetric(ci.PodUID, ci.ContainerName, consts.MetricMemCacheContainer)
		if err != nil {
			general.ErrorS(err, "failed to get MetricMemCacheContainer", "podName", ci.PodName, "containerName", ci.ContainerName)
			return true
		}
	} else {
		total, err = cp.metaServer.GetNumaMetric(numaID, consts.MetricMemTotalNuma)
		if err != nil {
			general.ErrorS(err, "failed to get MetricMemTotalNuma")
			return true
		}
		cache, err = cp.metaServer.GetContainerNumaMetric(ci.PodUID, ci.ContainerName, strconv.Itoa(numaID), consts.MetricsMemFilePerNumaContainer)
		if err != nil {
			general.ErrorS(err, "failed to get MetricsMemFilePerNumaContainer", "podName", ci.PodName, "containerName", ci.ContainerName, "numaID", numaID)
			return true
		}
	}

	if cache.Value < total.Value*minCacheUtilizationThreshold {
		general.InfoS("skip reclaiming it because cache usage is less than threshold",
			"podName", ci.PodName, "containerName", ci.ContainerName, "numaID", numaID,
			"minCacheUtilizationThreshold", minCacheUtilizationThreshold, "cache", general.FormatMemoryQuantity(cache.Value),
			"total", general.FormatMemoryQuantity(total.Value))
		return false
	}
	return true
}

func (cp *cacheReaper) Reconcile(status *types.MemoryPressureStatus) error {
	containersToReapCache := make(map[consts.PodContainerName]*types.ContainerInfo)
	minCacheUtilizationThreshold := cp.conf.MinCacheUtilizationThreshold

	containers := make([]*types.ContainerInfo, 0)
	cp.metaReader.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool {
		if cp.reclaimedContainersFilter(containerInfo, cpuadvisor.FakedNUMAID, minCacheUtilizationThreshold) {
			containers = append(containers, containerInfo)
		}
		return true
	})

	if status.NodeCondition.State == types.MemoryPressureDropCache && status.NodeCondition.TargetReclaimed != nil {
		selected := cp.selectContainers(containers, *status.NodeCondition.TargetReclaimed, -1, consts.MetricMemCacheContainer)
		for _, ci := range selected {
			containersToReapCache[native.GeneratePodContainerName(ci.PodName, ci.ContainerName)] = ci
		}
	}

	for numaID, condition := range status.NUMAConditions {
		if condition.State == types.MemoryPressureDropCache && condition.TargetReclaimed != nil {
			containers = make([]*types.ContainerInfo, 0)
			cp.metaReader.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool {
				if cp.reclaimedContainersFilter(containerInfo, numaID, minCacheUtilizationThreshold) {
					containers = append(containers, containerInfo)
				}
				return true
			})
			selected := cp.selectContainers(containers, *condition.TargetReclaimed, numaID, consts.MetricsMemFilePerNumaContainer)
			for _, ci := range selected {
				containersToReapCache[native.GeneratePodContainerName(ci.PodName, ci.ContainerName)] = ci
			}
		}
	}

	cp.mutex.Lock()
	defer cp.mutex.Unlock()
	cp.containersToReapCache = containersToReapCache
	return nil
}

func (cp *cacheReaper) GetAdvices() types.InternalMemoryCalculationResult {
	result := types.InternalMemoryCalculationResult{
		ContainerEntries: make([]types.ContainerMemoryAdvices, 0),
	}
	cp.mutex.RLock()
	defer cp.mutex.RUnlock()
	for _, ci := range cp.containersToReapCache {
		entry := types.ContainerMemoryAdvices{
			PodUID:        ci.PodUID,
			ContainerName: ci.ContainerName,
			Values:        map[string]string{string(memoryadvisor.ControlKnobKeyDropCache): "true"},
		}
		result.ContainerEntries = append(result.ContainerEntries, entry)
	}

	return result
}
