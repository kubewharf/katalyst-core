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
	"fmt"
	"strconv"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/config"
	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
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
	metricsNameContainerMetric    = "container_metric_raw"
	metricsNamePodMetric          = "pod_metric_raw"

	metricsTagKeyEvictionScope  = "eviction_scope"
	metricsTagKeyDetectionLevel = "detection_level"
	metricsTagKeyNumaID         = "numa_id"
	metricsTagKeyAction         = "action"
	metricsTagKeyMetricName     = "metric_name"
	metricsTagKeyPodUID         = "pod_uid"
	metricsTagKeyContainerName  = "container_name"

	metricsTagValueDetectionLevelNuma          = "numa"
	metricsTagValueDetectionLevelSystem        = "system"
	metricsTagValueActionReclaimedEviction     = "reclaimed_eviction"
	metricsTagValueActionEviction              = "eviction"
	metricsTagValueNumaFreeBelowWatermarkTimes = "numa_free_below_watermark_times"
	metricsTagValueSystemKswapdDiff            = "system_kswapd_diff"
	metricsTagValueSystemKswapdRateExceedTimes = "system_kswapd_rate_exceed_times"
)

const (
	errMsgGetSystemMetrics          = "failed to get system metric, metric name: %s, err: %v"
	errMsgGetNumaMetrics            = "failed to get numa metric, metric name: %s, numa id: %d, err: %v"
	errMsgGetContainerNumaMetrics   = "failed to get container numa metric, metric name: %s, pod uid: %s, container name: %s, numa id: %d, err: %v"
	errMsgGetContainerSystemMetrics = "failed to get container system metric, metric name: %s, pod uid: %s, container name: %s, err: %v"
	errMsgCheckReclaimedPodFailed   = "failed to check reclaimed pod, pod: %s/%s, err: %v"
)

// EvictionHelper is a general tool collection for all memory eviction plugin
type EvictionHelper struct {
	emitter            metrics.MetricEmitter
	metaServer         *metaserver.MetaServer
	reclaimedPodFilter func(pod *v1.Pod) (bool, error)
}

func NewEvictionHelper(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer, conf *config.Configuration) *EvictionHelper {
	return &EvictionHelper{
		emitter:            emitter,
		metaServer:         metaServer,
		reclaimedPodFilter: conf.CheckReclaimedQoSForPod,
	}
}

// getWatermarkMetrics returns system-water mark related metrics (config)
// if numa node is specified, return config in this numa; otherwise return system-level config
func (e *EvictionHelper) getWatermarkMetrics(numaID int) (free, total, scaleFactor float64, err error) {
	var m metric.MetricData
	if numaID >= 0 {
		m, err = e.metaServer.GetNumaMetric(numaID, consts.MetricMemFreeNuma)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetNumaMetrics, consts.MetricMemFreeNuma, numaID, err)
		}

		free = m.Value
		_ = e.emitter.StoreFloat64(metricsNameNumaMetric, free, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID:     strconv.Itoa(numaID),
				metricsTagKeyMetricName: consts.MetricMemFreeNuma,
			})...)

		m, err = e.metaServer.GetNumaMetric(numaID, consts.MetricMemTotalNuma)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetNumaMetrics, consts.MetricMemTotalNuma, numaID, err)
		}

		total = m.Value
		_ = e.emitter.StoreFloat64(metricsNameNumaMetric, total, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID:     strconv.Itoa(numaID),
				metricsTagKeyMetricName: consts.MetricMemTotalNuma,
			})...)
	} else {
		m, err = e.metaServer.GetNodeMetric(consts.MetricMemFreeSystem)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemFreeSystem, err)
		}

		free = m.Value
		_ = e.emitter.StoreFloat64(metricsNameSystemMetric, free, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyMetricName: consts.MetricMemFreeSystem,
			})...)

		m, err = e.metaServer.GetNodeMetric(consts.MetricMemTotalSystem)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemTotalSystem, err)
		}

		total = m.Value
		_ = e.emitter.StoreFloat64(metricsNameSystemMetric, total, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyMetricName: consts.MetricMemTotalSystem,
			})...)
	}

	m, err = e.metaServer.GetNodeMetric(consts.MetricMemScaleFactorSystem)
	if err != nil {
		return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemScaleFactorSystem, err)
	}

	scaleFactor = m.Value
	_ = e.emitter.StoreFloat64(metricsNameSystemMetric, scaleFactor, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyMetricName: consts.MetricMemScaleFactorSystem,
		})...)

	return free, total, scaleFactor, nil
}

func (e *EvictionHelper) getSystemKswapdStealMetrics() (metric.MetricData, error) {
	m, err := e.metaServer.GetNodeMetric(consts.MetricMemKswapdstealSystem)
	if err != nil {
		return m, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemKswapdstealSystem, err)
	}

	if m.Time == nil {
		return m, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemKswapdstealSystem, fmt.Errorf("metric timestamp is nil"))
	}

	kswapdSteal := m.Value
	_ = e.emitter.StoreFloat64(metricsNameSystemMetric, kswapdSteal, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyMetricName: consts.MetricMemKswapdstealSystem,
		})...)

	return m, nil
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

	for _, metric := range rankingMetrics {
		currentMetric := metric
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
				p1Metric, p1Found := e.getPodMetric(p1, currentMetric, numaID)
				p2Metric, p2Found := e.getPodMetric(p2, currentMetric, numaID)
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

// getPodMetric returns the value of a pod-level metric.
// And the value of a pod-level metric is calculated by summing the metric values for all containers in that pod.
func (e *EvictionHelper) getPodMetric(pod *v1.Pod, metricName string, numaID int) (float64, bool) {
	if pod == nil {
		return 0, false
	}

	var m metric.MetricData
	var podMetricValue float64
	for _, container := range pod.Spec.Containers {
		var containerMetricValue float64
		var err error
		if numaID >= 0 {
			m, err = e.metaServer.GetContainerNumaMetric(string(pod.UID), container.Name, strconv.Itoa(numaID), metricName)
			if err != nil {
				general.Errorf(errMsgGetContainerNumaMetrics, metricName, pod.UID, container.Name, numaID, err)
				return 0, false
			}

			containerMetricValue = m.Value
			_ = e.emitter.StoreFloat64(metricsNameContainerMetric, containerMetricValue, metrics.MetricTypeNameRaw,
				metrics.ConvertMapToTags(map[string]string{
					metricsTagKeyPodUID:        string(pod.UID),
					metricsTagKeyContainerName: container.Name,
					metricsTagKeyNumaID:        strconv.Itoa(numaID),
					metricsTagKeyMetricName:    metricName,
				})...)
		} else {
			m, err = e.metaServer.GetContainerMetric(string(pod.UID), container.Name, metricName)
			if err != nil {
				general.Errorf(errMsgGetContainerSystemMetrics, metricName, pod.UID, container.Name, err)
				return 0, false
			}

			containerMetricValue = m.Value
			_ = e.emitter.StoreFloat64(metricsNameContainerMetric, containerMetricValue, metrics.MetricTypeNameRaw,
				metrics.ConvertMapToTags(map[string]string{
					metricsTagKeyPodUID:        string(pod.UID),
					metricsTagKeyContainerName: container.Name,
					metricsTagKeyNumaID:        strconv.Itoa(numaID),
					metricsTagKeyMetricName:    metricName,
				})...)
		}

		podMetricValue += containerMetricValue
	}

	_ = e.emitter.StoreFloat64(metricsNamePodMetric, podMetricValue, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyPodUID:     string(pod.UID),
			metricsTagKeyNumaID:     strconv.Itoa(numaID),
			metricsTagKeyMetricName: metricName,
		})...)

	return podMetricValue, true
}
