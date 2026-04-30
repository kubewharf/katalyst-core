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

package cpu

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager"

	"github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	EvictionPluginNameSystemCPUPressure = "system-cpu-pressure-eviction-plugin"
	EvictionConditionSystemCPU          = "SystemCPU"
	FakeLabelMetricPrefix               = "label"
	FakeLabelMetricSeparator            = "."

	MetricNameSystemLoadUnderPressure  = "system_load_under_pressure"
	MetricNameSystemUsageUnderPressure = "system_usage_under_pressure"
	FetchMetricFailed                  = "fetch_metric_error"
)

// ThresholdBound is used to bind node and pod metrics with the upper and lower bounds for eviction
type ThresholdBound struct {
	UpperBoundRatio float64
	LowerBoundRatio float64
	NodeMetricName  string
	PodMetricName   string
}

type entries map[string]*cpuutil.MetricRing

// SystemPressureEvictionPlugin is the implementation of EvictionPlugin for system cpu pressure
type SystemPressureEvictionPlugin struct {
	pluginName string

	sync.Mutex
	*process.StopControl

	emitter     metrics.MetricEmitter
	metaServer  *metaserver.MetaServer
	dynamicConf *dynamic.DynamicAgentConfiguration
	conf        *config.Configuration

	podMetricsHistory  map[string]entries
	nodeMetricsHistory entries

	nodeCapacity     int
	overMetricName   string
	lastEvictionTime time.Time
	syncPeriod       time.Duration
}

// NewCPUSystemPressureEvictionPlugin returns a new SystemPressureEvictionPlugin
func NewCPUSystemPressureEvictionPlugin(
	_ *client.GenericClientSet,
	_ events.EventRecorder,
	metaServer *metaserver.MetaServer,
	emitter metrics.MetricEmitter,
	conf *config.Configuration,
) plugin.EvictionPlugin {
	p := &SystemPressureEvictionPlugin{
		StopControl:  process.NewStopControl(time.Time{}),
		pluginName:   EvictionPluginNameSystemCPUPressure,
		emitter:      emitter,
		metaServer:   metaServer,
		syncPeriod:   conf.EvictionManagerSyncPeriod,
		nodeCapacity: metaServer.NumCPUs,
		conf:         conf,
		dynamicConf:  conf.DynamicAgentConfiguration,
	}
	return p
}

func (s *SystemPressureEvictionPlugin) Start() {
	general.Infof("%s", s.Name())
	go wait.UntilWithContext(context.TODO(), s.collectMetrics, s.syncPeriod)
}

// Name returns the name of the plugin
func (s *SystemPressureEvictionPlugin) Name() string {
	return s.pluginName
}

// getMetricBoundaries generates a map of ThresholdBound from the dynamic configuration.
// It skips the metric if both the upper and lower bounds are 0, allowing independent switch for load and usage eviction.
func (s *SystemPressureEvictionPlugin) getMetricBoundaries(dynamicConfig *evictionconfig.CPUSystemPressureEvictionPluginConfiguration) map[string]ThresholdBound {
	boundaries := make(map[string]ThresholdBound)

	if dynamicConfig.SystemLoadUpperBoundRatio > 0 || dynamicConfig.SystemLoadLowerBoundRatio > 0 {
		boundaries[consts.MetricLoad1MinSystem] = ThresholdBound{
			UpperBoundRatio: dynamicConfig.SystemLoadUpperBoundRatio,
			LowerBoundRatio: dynamicConfig.SystemLoadLowerBoundRatio,
			NodeMetricName:  consts.MetricLoad1MinSystem,
			PodMetricName:   consts.MetricLoad1MinContainer,
		}
	}

	if dynamicConfig.SystemUsageUpperBoundRatio > 0 || dynamicConfig.SystemUsageLowerBoundRatio > 0 {
		boundaries[consts.MetricCPUUsageSystem] = ThresholdBound{
			UpperBoundRatio: dynamicConfig.SystemUsageUpperBoundRatio,
			LowerBoundRatio: dynamicConfig.SystemUsageLowerBoundRatio,
			NodeMetricName:  consts.MetricCPUUsageSystem,
			PodMetricName:   consts.MetricCPUUsageContainer,
		}
	}

	return boundaries
}

func (s *SystemPressureEvictionPlugin) collectMetrics(ctx context.Context) {
	dynamicConfig := s.dynamicConf.GetDynamicConfiguration().EvictionConfiguration.CPUSystemPressureEvictionPluginConfiguration
	if !dynamicConfig.EnableCPUSystemEviction {
		return
	}

	timeout, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	podList, err := s.metaServer.GetPodList(timeout, native.PodIsActive)
	if err != nil {
		klog.Errorf("getPodList fail: %v", err)
		return
	}

	var (
		collectTime = time.Now().UnixNano()
		capacity    = s.nodeCapacity
	)

	var excludedPods []*v1.Pod
	if dynamicConfig.CheckCPUManager {
		cpuManagerOn, err := s.checkKubeletCPUManager()
		if err != nil {
			general.Errorf("checkKubeletCPUManager fail: %v", err)
			return
		}
		if cpuManagerOn {
			podList, excludedPods, capacity, err = filterGuaranteedPods(podList, capacity)
			if err != nil {
				general.Errorf("filterGuaranteedPods fail: %v", err)
				return
			}
		}
	}

	s.Lock()
	defer s.Unlock()

	s.clearExpiredMetricsHistory(podList)
	boundaries := s.getMetricBoundaries(dynamicConfig)
	for metricName, bound := range boundaries {
		general.Infof("system pressure eviction for metric %v is enabled, upperBoundRatio: %v, lowerBoundRatio: %v",
			metricName, bound.UpperBoundRatio, bound.LowerBoundRatio)
	}

	// Fetch Pod Metrics
	metricsToCollectForPods := sets.NewString()
	for _, bound := range boundaries {
		metricsToCollectForPods.Insert(bound.PodMetricName)
	}
	for _, metricName := range s.mergeCollectMetrics(dynamicConfig) {
		metricsToCollectForPods.Insert(metricName)
	}

	podAggregatedNodeMetrics := make(map[string]float64)
	filteredGuaranteedPodsMetrics := make(map[string]float64)

	// If we are in NodeMetric mode and there are excluded pods, we also need to collect metrics for the excluded guaranteed pods
	// so we can subtract them from the node metrics
	if dynamicConfig.SystemEvictionMetricMode == evictionconfig.NodeMetric && len(excludedPods) > 0 {
		for _, pod := range excludedPods {
			for _, bound := range boundaries {
				metricData := s.metaServer.AggregatePodMetric([]*v1.Pod{pod}, bound.PodMetricName, metric.AggregatorSum, metric.DefaultContainerMetricFilter)
				filteredGuaranteedPodsMetrics[bound.NodeMetricName] += metricData.Value
			}
		}
	}

	for _, pod := range podList {
		for _, metricName := range metricsToCollectForPods.UnsortedList() {
			metricData := s.metaServer.AggregatePodMetric([]*v1.Pod{pod}, metricName, metric.AggregatorSum, metric.DefaultContainerMetricFilter)

			snapshot := &cpuutil.MetricSnapshot{
				Info: cpuutil.MetricInfo{
					Name:  metricName,
					Value: metricData.Value,
				},
				Time: collectTime,
			}
			s.pushMetric(dynamicConfig, metricName, string(pod.UID), snapshot)

			if dynamicConfig.SystemEvictionMetricMode == evictionconfig.PodAggregatedMetric {
				// We need to map pod metric back to node metric for aggregation
				for _, bound := range boundaries {
					if bound.PodMetricName == metricName {
						podAggregatedNodeMetrics[bound.NodeMetricName] += metricData.Value
					}
				}
			}
		}
	}

	// Fetch Node Metrics
	for _, bound := range boundaries {
		var metricVal float64
		if dynamicConfig.SystemEvictionMetricMode == evictionconfig.NodeMetric {
			nodeMetric, err := s.metaServer.MetricsFetcher.GetNodeMetric(bound.NodeMetricName)
			if err != nil {
				general.Errorf("get metrics %v failed, err: %v", bound.NodeMetricName, err)
				_ = s.emitter.StoreInt64(FetchMetricFailed, 1, metrics.MetricTypeNameCount, metrics.MetricTag{Key: "metric", Val: bound.NodeMetricName})
				continue
			}
			metricVal = nodeMetric.Value
			// In NodeMetric mode, if there are excluded pods, we need to subtract Guaranteed Pods' metrics from Node metric
			if len(excludedPods) > 0 {
				metricVal -= filteredGuaranteedPodsMetrics[bound.NodeMetricName]
				if metricVal < 0 {
					metricVal = 0
				}
			}
		} else {
			metricVal = podAggregatedNodeMetrics[bound.NodeMetricName]
		}

		s.pushNodeMetric(dynamicConfig, capacity, bound, metricVal, collectTime)
		general.Infof("collectMetrics, metricName: %v, metricVal: %v, capacity: %v, ratio: %.3f",
			bound.NodeMetricName, metricVal, capacity, metricVal/float64(capacity))
	}
}

func (s *SystemPressureEvictionPlugin) ThresholdMet(_ context.Context, _ *v1alpha1.GetThresholdMetRequest) (*v1alpha1.ThresholdMetResponse, error) {
	dynamicConfig := s.dynamicConf.GetDynamicConfiguration().EvictionConfiguration.CPUSystemPressureEvictionPluginConfiguration
	if !dynamicConfig.EnableCPUSystemEviction {
		return &v1alpha1.ThresholdMetResponse{
			MetType: v1alpha1.ThresholdMetType_NOT_MET,
		}, nil
	}

	var mostSevereResp *v1alpha1.ThresholdMetResponse
	boundaries := s.getMetricBoundaries(dynamicConfig)
	for metricName, bound := range boundaries {
		general.Infof("system pressure eviction for metric %v is enabled, upperBoundRatio: %v, lowerBoundRatio: %v",
			metricName, bound.UpperBoundRatio, bound.LowerBoundRatio)
	}

	s.Lock()
	defer s.Unlock()

	for _, bound := range boundaries {
		metricCache, ok := s.nodeMetricsHistory[bound.NodeMetricName]
		if !ok {
			general.Warningf("metric %v history not found", bound.NodeMetricName)
			continue
		}

		softOverCount, hardOverCount := 0, 0
		metricCache.RLock()
		for _, snapshot := range metricCache.Queue {
			if snapshot == nil {
				continue
			}
			if bound.LowerBoundRatio > 0 && snapshot.Info.Value > snapshot.Info.LowerBound {
				softOverCount++
			}
			if bound.UpperBoundRatio > 0 && snapshot.Info.Value > snapshot.Info.UpperBound {
				hardOverCount++
			}
		}
		metricCache.RUnlock()

		softOverRatio := float64(softOverCount) / float64(metricCache.MaxLen)
		hardOverRatio := float64(hardOverCount) / float64(metricCache.MaxLen)

		softOver := softOverRatio >= dynamicConfig.ThresholdMetPercentage
		hardOver := hardOverRatio >= dynamicConfig.ThresholdMetPercentage

		general.Infof("cpu system eviction, metric: %v, softOverCount: %v,"+
			"hardOverCount: %v, softOverRatio: %v, hardOverRatio: %v, "+
			"threshold: %v", bound.NodeMetricName, softOverCount, hardOverCount, softOverRatio, hardOverRatio,
			dynamicConfig.ThresholdMetPercentage)

		if hardOver || softOver {
			metricUnderPressureName := MetricNameSystemLoadUnderPressure
			if bound.NodeMetricName == consts.MetricCPUUsageSystem {
				metricUnderPressureName = MetricNameSystemUsageUnderPressure
			}
			_ = s.emitter.StoreInt64(metricUnderPressureName, 1, metrics.MetricTypeNameCount)
		}

		var currentResp *v1alpha1.ThresholdMetResponse
		if hardOver {
			currentResp = &v1alpha1.ThresholdMetResponse{
				ThresholdValue:    hardOverRatio,
				ObservedValue:     dynamicConfig.ThresholdMetPercentage,
				ThresholdOperator: v1alpha1.ThresholdOperator_GREATER_THAN,
				MetType:           v1alpha1.ThresholdMetType_HARD_MET,
				EvictionScope:     bound.PodMetricName,
				Condition: &v1alpha1.Condition{
					ConditionType: v1alpha1.ConditionType_NODE_CONDITION,
					Effects:       []string{string(v1.TaintEffectNoSchedule)},
					ConditionName: EvictionConditionSystemCPU,
					MetCondition:  true,
				},
			}
		} else if softOver {
			currentResp = &v1alpha1.ThresholdMetResponse{
				ThresholdValue:    softOverRatio,
				ObservedValue:     dynamicConfig.ThresholdMetPercentage,
				ThresholdOperator: v1alpha1.ThresholdOperator_GREATER_THAN,
				MetType:           v1alpha1.ThresholdMetType_SOFT_MET,
				EvictionScope:     bound.PodMetricName,
				Condition: &v1alpha1.Condition{
					ConditionType: v1alpha1.ConditionType_NODE_CONDITION,
					Effects:       []string{string(v1.TaintEffectNoSchedule)},
					ConditionName: EvictionConditionSystemCPU,
					MetCondition:  true,
				},
			}
		}

		if currentResp != nil {
			if mostSevereResp == nil {
				mostSevereResp = currentResp
			} else {
				if currentResp.MetType == v1alpha1.ThresholdMetType_HARD_MET && mostSevereResp.MetType == v1alpha1.ThresholdMetType_SOFT_MET {
					mostSevereResp = currentResp
				} else if currentResp.MetType == mostSevereResp.MetType && currentResp.ThresholdValue > mostSevereResp.ThresholdValue {
					mostSevereResp = currentResp
				}
			}
		}
	}

	if mostSevereResp != nil {
		s.overMetricName = mostSevereResp.EvictionScope
		return mostSevereResp, nil
	}

	s.overMetricName = ""
	return &v1alpha1.ThresholdMetResponse{
		MetType: v1alpha1.ThresholdMetType_NOT_MET,
	}, nil
}

func (s *SystemPressureEvictionPlugin) GetTopEvictionPods(
	_ context.Context,
	request *v1alpha1.GetTopEvictionPodsRequest,
) (*v1alpha1.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}
	if len(request.ActivePods) == 0 {
		general.Warningf("cpu system eviction get empty active pods list")
		return &v1alpha1.GetTopEvictionPodsResponse{}, nil
	}

	s.Lock()
	defer s.Unlock()

	var (
		dynamicConfig = s.dynamicConf.GetDynamicConfiguration().EvictionConfiguration.CPUSystemPressureEvictionPluginConfiguration
		now           = time.Now()
	)
	if !dynamicConfig.EnableCPUSystemEviction {
		general.Warningf("EnableCPUSystemEviction off, return")
		return &v1alpha1.GetTopEvictionPodsResponse{}, nil
	}
	if s.overMetricName == "" {
		general.Warningf("cpu system eviction without over metric name, skip")
		return &v1alpha1.GetTopEvictionPodsResponse{}, nil
	}
	if !(s.lastEvictionTime.IsZero() || now.Sub(s.lastEvictionTime) >= dynamicConfig.EvictionCoolDownTime) {
		general.Infof("in eviction cool-down time, skip eviction. now: %s, lastEvictionTime: %s",
			now.String(), s.lastEvictionTime.String())
		return &v1alpha1.GetTopEvictionPodsResponse{}, nil
	}

	candidatePods := request.ActivePods
	if dynamicConfig.CheckCPUManager {
		cpuManagerOn, err := s.checkKubeletCPUManager()
		if err != nil {
			err = fmt.Errorf("checkKubeletCPUManager fail: %v", err)
			general.Errorf(err.Error())
			return nil, err
		}
		if cpuManagerOn {
			candidatePods = native.FilterPods(request.GetActivePods(), func(pod *v1.Pod) (bool, error) {
				if pod == nil {
					return false, fmt.Errorf("FilterPods got nil pod")
				}
				if native.PodGuaranteedCPUs(pod) == 0 {
					return true, nil
				}
				return false, nil
			})
		}
	}

	var (
		topN     = general.MinUInt64(request.TopN, uint64(len(candidatePods)))
		topNPods = make([]*v1.Pod, 0)
	)
	s.lastEvictionTime = now

	general.NewMultiSorter(s.getEvictionCmpFunc(dynamicConfig)...).Sort(native.NewPodSourceImpList(candidatePods))

	for i := 0; uint64(i) < topN; i++ {
		topNPods = append(topNPods, candidatePods[i])
	}

	resp := &v1alpha1.GetTopEvictionPodsResponse{
		TargetPods: topNPods,
	}
	if dynamicConfig.GracePeriod > 0 {
		resp.DeletionOptions = &v1alpha1.DeletionOptions{
			GracePeriodSeconds: dynamicConfig.GracePeriod,
		}
	}

	s.overMetricName = ""
	return resp, nil
}

func (s *SystemPressureEvictionPlugin) GetEvictPods(
	_ context.Context,
	_ *v1alpha1.GetEvictPodsRequest,
) (*v1alpha1.GetEvictPodsResponse, error) {
	return &v1alpha1.GetEvictPodsResponse{}, nil
}

func (s *SystemPressureEvictionPlugin) pushMetric(dynamicConfig *evictionconfig.CPUSystemPressureEvictionPluginConfiguration, metricName, podUID string, snapshot *cpuutil.MetricSnapshot) {
	if s.podMetricsHistory == nil {
		s.podMetricsHistory = map[string]entries{}
	}
	if _, ok := s.podMetricsHistory[metricName]; !ok {
		s.podMetricsHistory[metricName] = map[string]*cpuutil.MetricRing{}
	}
	if _, ok := s.podMetricsHistory[metricName][podUID]; !ok {
		s.podMetricsHistory[metricName][podUID] = cpuutil.CreateMetricRing(dynamicConfig.MetricRingSize)
	}

	s.podMetricsHistory[metricName][podUID].Push(snapshot)
}

func (s *SystemPressureEvictionPlugin) pushNodeMetric(dynamicConfig *evictionconfig.CPUSystemPressureEvictionPluginConfiguration, nodeCapacity int, bound ThresholdBound, value float64, collectTime int64) {
	if s.nodeMetricsHistory == nil {
		s.nodeMetricsHistory = map[string]*cpuutil.MetricRing{}
	}
	if _, ok := s.nodeMetricsHistory[bound.NodeMetricName]; !ok {
		s.nodeMetricsHistory[bound.NodeMetricName] = cpuutil.CreateMetricRing(dynamicConfig.MetricRingSize)
	}

	snapshot := &cpuutil.MetricSnapshot{
		Info: cpuutil.MetricInfo{
			Name:       bound.NodeMetricName,
			Value:      value,
			UpperBound: float64(nodeCapacity) * bound.UpperBoundRatio,
			LowerBound: float64(nodeCapacity) * bound.LowerBoundRatio,
		},
		Time: collectTime,
	}
	s.nodeMetricsHistory[bound.NodeMetricName].Push(snapshot)
}

func (s *SystemPressureEvictionPlugin) getEvictionCmpFunc(dynamicConfig *evictionconfig.CPUSystemPressureEvictionPluginConfiguration) []general.CmpFunc {
	cmpFuncs := make([]general.CmpFunc, 0)
	rankingMetrics := dynamicConfig.EvictionRankingMetrics

	if s.overMetricName != "" {
		if !sets.NewString(rankingMetrics...).Has(s.overMetricName) {
			rankingMetrics = append([]string{s.overMetricName}, rankingMetrics...)
		} else {
			// move overMetricName to front
			newRanking := []string{s.overMetricName}
			for _, m := range rankingMetrics {
				if m != s.overMetricName {
					newRanking = append(newRanking, m)
				}
			}
			rankingMetrics = newRanking
		}
	}

	for _, m := range rankingMetrics {
		currentMetric := m
		cmpFuncs = append(cmpFuncs, func(i1, i2 interface{}) int {
			p1, p2 := i1.(*v1.Pod), i2.(*v1.Pod)
			switch currentMetric {
			case evictionconfig.FakeMetricQoSLevel:
				return s.cmpKatalystQoS(p1, p2)
			case evictionconfig.FakeMetricPriority:
				return general.ReverseCmpFunc(native.PodPriorityCmpFunc)(p1, p2)
			default:
				if labelKey, ok := s.splitKeyFromFakeLabelMetric(currentMetric); ok {
					return s.cmpSpecifiedLabels(dynamicConfig, p1, p2, labelKey)
				}

				return s.cmpMetric(currentMetric, p1, p2)
			}
		})
	}

	return cmpFuncs
}

func (s *SystemPressureEvictionPlugin) cmpMetric(metricName string, p1, p2 *v1.Pod) int {
	return general.CmpFloat64(s.getPodMetricHistory(metricName, p1), s.getPodMetricHistory(metricName, p2))
}

func (s *SystemPressureEvictionPlugin) cmpKatalystQoS(p1, p2 *v1.Pod) int {
	p1Reclaim, err1 := s.conf.CheckReclaimedQoSForPod(p1)
	p2Reclaim, err2 := s.conf.CheckReclaimedQoSForPod(p2)
	if err1 != nil || err2 != nil {
		return general.CmpError(err1, err2)
	}

	return general.CmpBool(p1Reclaim, p2Reclaim)
}

func (s *SystemPressureEvictionPlugin) cmpSpecifiedLabels(dynamicConfig *evictionconfig.CPUSystemPressureEvictionPluginConfiguration, p1, p2 *v1.Pod, labelKey string) int {
	vals, ok := dynamicConfig.RankingLabels[labelKey]
	if !ok {
		return 0
	}
	p1Val, ok := p1.Labels[labelKey]
	if !ok {
		return -1
	}
	p2Val, ok := p2.Labels[labelKey]
	if !ok {
		return 1
	}

	valsScoreMap := make(map[string]int32)
	for i, val := range vals {
		valsScoreMap[val] = int32(i + 1)
	}
	return general.CmpInt32(valsScoreMap[p1Val], valsScoreMap[p2Val])
}

// splitKeyFromFakeLabelMetric parses the ranking metric name to determine if it is a fake label metric.
// A valid fake label metric should be in the format of "label.<key>" (e.g. "label.katalyst.kubewharf.io/qos").
// It returns the real label key and true if it's valid, otherwise returns empty string and false.
func (s *SystemPressureEvictionPlugin) splitKeyFromFakeLabelMetric(metricName string) (string, bool) {
	// Instead of strings.SplitN which creates slice allocation, we can use strings.HasPrefix and strings.TrimPrefix for better performance.
	prefix := FakeLabelMetricPrefix + FakeLabelMetricSeparator
	if strings.HasPrefix(metricName, prefix) {
		return strings.TrimPrefix(metricName, prefix), true
	}
	return "", false
}

// mergeCollectMetrics filters out fake metrics (like QoS, Priority, and Label metrics)
// from EvictionRankingMetrics and returns the unique real metrics that need to be collected from MetaServer.
func (s *SystemPressureEvictionPlugin) mergeCollectMetrics(dynamicConfig *evictionconfig.CPUSystemPressureEvictionPluginConfiguration) []string {
	metrics := sets.NewString()
	for _, metricName := range dynamicConfig.EvictionRankingMetrics {
		switch metricName {
		case evictionconfig.FakeMetricPriority,
			evictionconfig.FakeMetricQoSLevel:
			// skip all built-in fake metrics
			continue
		default:
			// skip fake label metrics
			if _, isLabel := s.splitKeyFromFakeLabelMetric(metricName); isLabel {
				continue
			}

			// for real metrics, add them into the set
			metrics.Insert(metricName)
		}
	}
	return metrics.UnsortedList()
}

func (s *SystemPressureEvictionPlugin) getPodMetricHistory(metricName string, pod *v1.Pod) float64 {
	podUID := string(pod.UID)
	if _, ok := s.podMetricsHistory[metricName]; !ok {
		return 0
	}

	if _, ok := s.podMetricsHistory[metricName][podUID]; !ok {
		return 0
	}

	return s.podMetricsHistory[metricName][podUID].Sum()
}

func (s *SystemPressureEvictionPlugin) clearExpiredMetricsHistory(podList []*v1.Pod) {
	podSet := sets.NewString()
	for _, pod := range podList {
		podSet.Insert(string(pod.UID))
	}

	for metricName, entries := range s.podMetricsHistory {
		for podUID := range entries {
			if !podSet.Has(podUID) {
				delete(s.podMetricsHistory[metricName], podUID)
			}
		}
	}
}

func (s *SystemPressureEvictionPlugin) checkKubeletCPUManager() (bool, error) {
	timeout, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	kubeletConfig, err := s.metaServer.GetKubeletConfig(timeout)
	if err != nil {
		return false, err
	}

	featureGates := kubeletConfig.FeatureGates
	on, ok := featureGates[string(features.CPUManager)]
	cpuManagerPolicy := kubeletConfig.CPUManagerPolicy

	if (ok && !on) || (cpuManagerPolicy == string(cpumanager.PolicyNone)) {
		return false, nil
	}

	return true, nil
}

func filterGuaranteedPods(podList []*v1.Pod, nodeCapacity int) ([]*v1.Pod, []*v1.Pod, int, error) {
	var (
		res            = make([]*v1.Pod, 0)
		guaranteedPods = make([]*v1.Pod, 0)
		cpus           = 0
	)
	for _, pod := range podList {
		guaranteedCPU := native.PodGuaranteedCPUs(pod)
		if guaranteedCPU == 0 {
			res = append(res, pod)
		} else {
			guaranteedPods = append(guaranteedPods, pod)
			cpus += guaranteedCPU
		}
	}

	if cpus > nodeCapacity {
		general.Warningf("guaranteed pod cpu request is greater than node capacity, cpus: %v, capacity: %v",
			cpus, nodeCapacity)
		return res, guaranteedPods, 0, nil
	}

	return res, guaranteedPods, nodeCapacity - cpus, nil
}
