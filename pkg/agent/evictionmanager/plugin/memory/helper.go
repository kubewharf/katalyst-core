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

package memory

import (
	"strconv"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/config"
	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// eviction scope related variables
// actionReclaimedEviction for reclaimed_cores, while actionEviction for all pods
const (
	actionNoop = iota
	actionReclaimedEviction
	actionEviction
)

// control-related variables
const (
	kswapdStealPreviousCycleMissing = -1
	nonExistNumaID                  = -1
)

const (
	metricsNameFetchMetricError   = "fetch_metric_error_count"
	metricsNameNumberOfTargetPods = "number_of_target_pods_raw"
	metricsNameThresholdMet       = "threshold_met_count"
	metricsNameNumaMetric         = "numa_metric_raw"
	metricsNameSystemMetric       = "system_metric_raw"

	metricsTagKeyEvictionScope  = "eviction_scope"
	metricsTagKeyDetectionLevel = "detection_level"
	metricsTagKeyNumaID         = "numa_id"
	metricsTagKeyAction         = "action"
	metricsTagKeyMetricName     = "metric_name"

	metricsTagValueDetectionLevelNuma             = "numa"
	metricsTagValueDetectionLevelSystem           = "system"
	metricsTagValueActionReclaimedEviction        = "reclaimed_eviction"
	metricsTagValueActionEviction                 = "eviction"
	metricsTagValueNumaFreeBelowWatermarkTimes    = "numa_free_below_watermark_times"
	metricsTagValueSystemKswapdDiff               = "system_kswapd_diff"
	metricsTagValueSystemKswapdRateExceedDuration = "system_kswapd_rate_exceed_duration"
)

const (
	errMsgCheckReclaimedPodFailed = "failed to check reclaimed pod, pod: %s/%s, err: %v"
)

// EvictionHelper is a general tool collection for all memory eviction plugin
type EvictionHelper struct {
	metaServer         *metaserver.MetaServer
	emitter            metrics.MetricEmitter
	reclaimedPodFilter func(pod *v1.Pod) (bool, error)
}

func NewEvictionHelper(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer, conf *config.Configuration) *EvictionHelper {
	return &EvictionHelper{
		metaServer:         metaServer,
		emitter:            emitter,
		reclaimedPodFilter: conf.CheckReclaimedQoSForPod,
	}
}

func (e *EvictionHelper) selectTopNPodsToEvictByMetrics(activePods []*v1.Pod, topN uint64, numaID,
	action int, rankingMetrics []string, podToEvictMap map[string]*v1.Pod) {
	filteredPods := e.filterPods(activePods, action)
	if filteredPods != nil {
		general.NewMultiSorter(e.getEvictionCmpFuncs(rankingMetrics, numaID)...).Sort(native.NewPodSourceImpList(filteredPods))
		for i := 0; uint64(i) < general.MinUInt64(topN, uint64(len(filteredPods))); i++ {
			podToEvictMap[string(filteredPods[i].UID)] = filteredPods[i]
		}
	}
}

func (e *EvictionHelper) filterPods(pods []*v1.Pod, action int) []*v1.Pod {
	switch action {
	case actionReclaimedEviction:
		return native.FilterPods(pods, e.reclaimedPodFilter)
	case actionEviction:
		return pods
	default:
		return nil
	}
}

// getEvictionCmpFuncs returns a comparison function list to judge the eviction order of different pods
func (e *EvictionHelper) getEvictionCmpFuncs(rankingMetrics []string, numaID int) []general.CmpFunc {
	cmpFuncs := make([]general.CmpFunc, 0, len(rankingMetrics))

	for _, m := range rankingMetrics {
		currentMetric := m
		cmpFuncs = append(cmpFuncs, func(s1, s2 interface{}) int {
			p1, p2 := s1.(*v1.Pod), s2.(*v1.Pod)
			switch currentMetric {
			case evictionconfig.FakeMetricQoSLevel:
				isReclaimedPod1, err1 := e.reclaimedPodFilter(p1)
				if err1 != nil {
					general.Errorf(errMsgCheckReclaimedPodFailed, p1.Namespace, p1.Name, err1)
				}

				isReclaimedPod2, err2 := e.reclaimedPodFilter(p2)
				if err2 != nil {
					general.Errorf(errMsgCheckReclaimedPodFailed, p2.Namespace, p2.Name, err2)
				}

				if err1 != nil || err2 != nil {
					// prioritize evicting the pod for which no error is returned
					return general.CmpError(err1, err2)
				}

				// prioritize evicting the pod whose QoS level is reclaimed_cores
				return general.CmpBool(isReclaimedPod1, isReclaimedPod2)
			case evictionconfig.FakeMetricPriority:
				// prioritize evicting the pod whose priority is lower
				return general.ReverseCmpFunc(native.PodPriorityCmpFunc)(p1, p2)
			default:
				p1Metric, p1Found := helper.GetPodMetric(e.metaServer.MetricsFetcher, e.emitter, p1, currentMetric, numaID)
				p2Metric, p2Found := helper.GetPodMetric(e.metaServer.MetricsFetcher, e.emitter, p2, currentMetric, numaID)
				if !p1Found || !p2Found {
					_ = e.emitter.StoreInt64(metricsNameFetchMetricError, 1, metrics.MetricTypeNameCount,
						metrics.ConvertMapToTags(map[string]string{
							metricsTagKeyNumaID: strconv.Itoa(numaID),
						})...)
					// prioritize evicting the pod for which no stats were found
					return general.CmpBool(!p1Found, !p2Found)
				}

				// prioritize evicting the pod whose metric value is greater
				return general.CmpFloat64(p1Metric, p2Metric)
			}
		})
	}

	return cmpFuncs

}
