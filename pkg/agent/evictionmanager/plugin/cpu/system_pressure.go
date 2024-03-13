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
	// EvictionPluginNameSystemCPUPressure is the plugin name of SystemPressureEvictionPlugin
	// which detects CPU load and usage, alleviates the problem of high CPU load by disabling scheduling and eviction
	// at node level; CPUPressureLoadEviction in dynamic CPU qrm plugin performs interference detection
	// based on Katalyst QoS resource pool.
	EvictionPluginNameSystemCPUPressure = "system-cpu-pressure-eviction-plugin"
	EvictionConditionSystemCPU          = "SystemCPU"
	FakeLabelMetricPrefix               = "label"
	FakeLabelMetricSeparator            = "."
)

var podMetrics = []string{consts.MetricLoad1MinContainer, consts.MetricCPUUsageContainer}

type entries map[string]*metric.MetricRing

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
		nodeCapacity: metaServer.NumCPUs, // get cpu capacity by NumCPUs, allocatable may be mutated by overcommit webhook
		conf:         conf,
		dynamicConf:  conf.DynamicAgentConfiguration,
	}

	return p
}

func (s *SystemPressureEvictionPlugin) Start() {
	general.Infof("%s", s.Name())
	go wait.UntilWithContext(context.TODO(), s.collectMetrics, s.syncPeriod)
}

func (s *SystemPressureEvictionPlugin) Name() string {
	return s.pluginName
}

func (s *SystemPressureEvictionPlugin) collectMetrics(_ context.Context) {
	timeout, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	podList, err := s.metaServer.GetPodList(timeout, native.PodIsActive)
	if err != nil {
		klog.Errorf("getPodList fail: %v", err)
		return
	}

	var (
		collectTime = time.Now().UnixNano()
		nodeMetric  = make(map[string]float64)
		capacity    = s.nodeCapacity
	)

	if s.dynamicConf.GetDynamicConfiguration().CheckCPUManager {
		cpuManagerOn, err := s.checkKubeletCPUManager()
		if err != nil {
			klog.Errorf("checkKubeletCPUManager fail: %v", err)
			return
		}
		if cpuManagerOn {
			// if cpuManagerOn, only burstable and besteffort pods will be collect and evicted
			podList, capacity, err = filterGuaranteedPods(podList, capacity)
			if err != nil {
				klog.Errorf("filterGuaranteedPods fail: %v", err)
				return
			}
		}
	}

	s.Lock()
	defer s.Unlock()

	// clear expired pod metrics
	s.clearExpiredMetricsHistory(podList)

	// store pod metrics for sort in ThresholdMet
	for _, pod := range podList {
		for _, metricName := range s.mergeCollectMetrics() {
			metricData := s.metaServer.AggregatePodMetric([]*v1.Pod{pod}, metricName, metric.AggregatorSum, metric.DefaultContainerMetricFilter)

			snapshot := &metric.MetricSnapshot{
				Info: metric.MetricInfo{
					Name:  metricName,
					Value: metricData.Value,
				},
				Time: collectTime,
			}
			s.pushMetric(metricName, string(pod.UID), snapshot)

			nodeMetric[metricName] += metricData.Value
		}
	}

	// store node metrics
	for _, metricName := range podMetrics {
		if metricVal, ok := nodeMetric[metricName]; ok {
			s.pushNodeMetric(s.dynamicConf.GetDynamicConfiguration(), capacity, metricName, metricVal, collectTime)
			klog.V(6).Infof("collectMetrics, metricName: %v, metricVal: %v, capacity: %v", metricName, metricVal, capacity)
		}
	}
}

func (s *SystemPressureEvictionPlugin) ThresholdMet(_ context.Context) (*v1alpha1.ThresholdMetResponse, error) {
	dynamicConfig := s.dynamicConf.GetDynamicConfiguration()
	if !dynamicConfig.EnableCPUSystemEviction {
		return &v1alpha1.ThresholdMetResponse{
			MetType: v1alpha1.ThresholdMetType_NOT_MET,
		}, nil
	}

	var softOverResp *v1alpha1.ThresholdMetResponse

	s.Lock()
	defer s.Unlock()

	for _, metricName := range podMetrics {
		metricCache, ok := s.nodeMetricsHistory[metricName]
		if !ok {
			klog.Warningf("metric %v history not found", metricName)
			continue
		}

		softOverCount, hardOverCount := metricCache.Count()

		softOverRatio := float64(softOverCount) / float64(metricCache.MaxLen)
		hardOverRatio := float64(hardOverCount) / float64(metricCache.MaxLen)

		softOver := softOverRatio >= dynamicConfig.CPUSystemPressureEvictionPluginConfiguration.ThresholdMetPercentage
		hardOver := hardOverRatio >= dynamicConfig.CPUSystemPressureEvictionPluginConfiguration.ThresholdMetPercentage

		klog.V(6).Infof("cpu system eviction, metric: %v, softOverCount: %v,"+
			"hardOverCount: %v, softOverRatio: %v, hardOverRatio: %v, "+
			"threshold: %v", metricName, softOverCount, hardOverCount, softOverRatio, hardOverRatio,
			dynamicConfig.CPUSystemPressureEvictionPluginConfiguration.ThresholdMetPercentage)

		if hardOver {
			s.overMetricName = metricName
			return &v1alpha1.ThresholdMetResponse{
				ThresholdValue:    hardOverRatio,
				ObservedValue:     dynamicConfig.CPUSystemPressureEvictionPluginConfiguration.ThresholdMetPercentage,
				ThresholdOperator: v1alpha1.ThresholdOperator_GREATER_THAN,
				MetType:           v1alpha1.ThresholdMetType_HARD_MET,
				EvictionScope:     metricName,
				Condition: &v1alpha1.Condition{
					ConditionType: v1alpha1.ConditionType_NODE_CONDITION,
					Effects:       []string{string(v1.TaintEffectNoSchedule)},
					ConditionName: EvictionConditionSystemCPU,
					MetCondition:  true,
				},
			}, nil
		}

		if softOver && softOverResp == nil {
			softOverResp = &v1alpha1.ThresholdMetResponse{
				ThresholdValue:    softOverRatio,
				ObservedValue:     dynamicConfig.CPUSystemPressureEvictionPluginConfiguration.ThresholdMetPercentage,
				ThresholdOperator: v1alpha1.ThresholdOperator_GREATER_THAN,
				MetType:           v1alpha1.ThresholdMetType_SOFT_MET,
				EvictionScope:     metricName,
				Condition: &v1alpha1.Condition{
					ConditionType: v1alpha1.ConditionType_NODE_CONDITION,
					Effects:       []string{string(v1.TaintEffectNoSchedule)},
					ConditionName: EvictionConditionSystemCPU,
					MetCondition:  true,
				},
			}
		}
	}
	s.overMetricName = ""
	if softOverResp != nil {
		return softOverResp, nil
	}
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
		klog.Warningf("cpu system eviction get empty active pods list")
		return &v1alpha1.GetTopEvictionPodsResponse{}, nil
	}

	s.Lock()
	defer s.Unlock()

	var (
		dynamicConfig = s.dynamicConf.GetDynamicConfiguration()
		now           = time.Now()
	)
	if !dynamicConfig.EnableCPUSystemEviction {
		klog.Warningf("EnableCPUSystemEviction off, return")
		return &v1alpha1.GetTopEvictionPodsResponse{}, nil
	}
	if s.overMetricName == "" {
		klog.Warningf("cpu system eviction without over metric name, skip")
		return &v1alpha1.GetTopEvictionPodsResponse{}, nil
	}
	if !(s.lastEvictionTime.IsZero() || now.Sub(s.lastEvictionTime) >= dynamicConfig.EvictionCoolDownTime) {
		general.Infof("in eviction cool-down time, skip eviction. now: %s, lastEvictionTime: %s",
			now.String(), s.lastEvictionTime.String())
		return &v1alpha1.GetTopEvictionPodsResponse{}, nil
	}

	candidatePods := request.ActivePods
	if s.dynamicConf.GetDynamicConfiguration().CheckCPUManager {
		cpuManagerOn, err := s.checkKubeletCPUManager()
		if err != nil {
			err = fmt.Errorf("checkKubeletCPUManager fail: %v", err)
			klog.Error(err)
			return nil, err
		}
		if cpuManagerOn {
			// if cpuManagerOn, only burstable and besteffort pods will be collect and evicted
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

	// sort pod by eviction cmp functions
	general.NewMultiSorter(s.getEvictionCmpFunc()...).Sort(native.NewPodSourceImpList(candidatePods))

	// get topN from sorted podlist
	for i := 0; uint64(i) < topN; i++ {
		topNPods = append(topNPods, candidatePods[i])
	}

	resp := &v1alpha1.GetTopEvictionPodsResponse{
		TargetPods: topNPods,
	}
	if dynamicConfig.CPUSystemPressureEvictionPluginConfiguration.GracePeriod > 0 {
		resp.DeletionOptions = &v1alpha1.DeletionOptions{
			GracePeriodSeconds: dynamicConfig.CPUSystemPressureEvictionPluginConfiguration.GracePeriod,
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

func (s *SystemPressureEvictionPlugin) pushMetric(metricName, podUID string, snapshot *metric.MetricSnapshot) {
	if s.podMetricsHistory == nil {
		s.podMetricsHistory = map[string]entries{}
	}
	if _, ok := s.podMetricsHistory[metricName]; !ok {
		s.podMetricsHistory[metricName] = map[string]*metric.MetricRing{}
	}
	if _, ok := s.podMetricsHistory[metricName][podUID]; !ok {
		s.podMetricsHistory[metricName][podUID] = metric.CreateMetricRing(s.dynamicConf.GetDynamicConfiguration().MetricRingSize)
	}

	s.podMetricsHistory[metricName][podUID].Push(snapshot)
}

func (s *SystemPressureEvictionPlugin) pushNodeMetric(dynamicConfig *dynamic.Configuration, nodeCapacity int, metricName string, value float64, collectTime int64) {
	if s.nodeMetricsHistory == nil {
		s.nodeMetricsHistory = map[string]*metric.MetricRing{}
	}
	if _, ok := s.nodeMetricsHistory[metricName]; !ok {
		s.nodeMetricsHistory[metricName] = metric.CreateMetricRing(s.dynamicConf.GetDynamicConfiguration().MetricRingSize)
	}
	var upperBound, lowerBound float64
	switch metricName {
	case consts.MetricLoad1MinContainer:
		upperBound = dynamicConfig.CPUSystemPressureEvictionPluginConfiguration.SystemLoadUpperBoundRatio
		lowerBound = dynamicConfig.CPUSystemPressureEvictionPluginConfiguration.SystemLoadLowerBoundRatio
	case consts.MetricCPUUsageContainer:
		upperBound = dynamicConfig.CPUSystemPressureEvictionPluginConfiguration.SystemUsageUpperBoundRatio
		lowerBound = dynamicConfig.CPUSystemPressureEvictionPluginConfiguration.SystemUsageLowerBoundRatio
	default:
		klog.Errorf("unexpected metricName: %v", metricName)
		return
	}

	snapshot := &metric.MetricSnapshot{
		Info: metric.MetricInfo{
			Name:       metricName,
			Value:      value,
			UpperBound: float64(nodeCapacity) * upperBound,
			LowerBound: float64(nodeCapacity) * lowerBound,
		},
		Time: collectTime,
	}
	s.nodeMetricsHistory[metricName].Push(snapshot)
}

func (s *SystemPressureEvictionPlugin) getEvictionCmpFunc() []general.CmpFunc {
	dynamicConfig := s.dynamicConf.GetDynamicConfiguration()
	cmpFuncs := make([]general.CmpFunc, 0)
	rankingMetrics := dynamicConfig.EvictionRankingMetrics
	if !sets.NewString(rankingMetrics...).Has(s.overMetricName) {
		rankingMetrics = append(rankingMetrics, s.overMetricName)
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
			case evictionconfig.FakeMetricNativeQoSLevel:
				return native.PodQoSCmpFunc(p1, p2)
			case evictionconfig.FakeMetricOwnerLevel:
				return native.PodOwnerCmpFunc(p1, p2)
			default:
				if labelKey, ok := s.splitKeyFromFakeLabelMetric(currentMetric); ok {
					return s.cmpSpecifiedLabels(p1, p2, labelKey)
				} else {
					return s.cmpMetric(currentMetric, p1, p2)
				}
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

func (s *SystemPressureEvictionPlugin) cmpSpecifiedLabels(p1, p2 *v1.Pod, labelKey string) int {
	vals, ok := s.dynamicConf.GetDynamicConfiguration().RankingLabels[labelKey]
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

func (s *SystemPressureEvictionPlugin) splitKeyFromFakeLabelMetric(metricName string) (string, bool) {
	keys := strings.SplitN(metricName, FakeLabelMetricSeparator, 2)
	if len(keys) <= 1 {
		return "", false
	}

	if keys[0] != FakeLabelMetricPrefix {
		return "", false
	}

	return keys[1], true
}

func (s *SystemPressureEvictionPlugin) mergeCollectMetrics() []string {
	metrics := sets.NewString(podMetrics...)
	for _, metricName := range s.dynamicConf.GetDynamicConfiguration().EvictionRankingMetrics {
		switch metricName {
		case evictionconfig.FakeMetricOwnerLevel, evictionconfig.FakeMetricPriority, evictionconfig.FakeMetricQoSLevel, evictionconfig.FakeMetricNativeQoSLevel:
			continue
		default:
			if _, ok := s.splitKeyFromFakeLabelMetric(metricName); ok {
				continue
			}

			if !metrics.Has(metricName) {
				metrics.Insert(metricName)
			}
		}
	}
	return metrics.UnsortedList()
}

// get sum of pod metrics history in metric ring cache
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

// clear expired pod metrics without lock
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

// filterGuaranteedPods returns podList without guaranteed pods and capacity without guaranteed pods requests
func filterGuaranteedPods(podList []*v1.Pod, nodeCapacity int) ([]*v1.Pod, int, error) {
	var (
		res  = make([]*v1.Pod, 0)
		cpus = 0
	)
	for _, pod := range podList {
		guaranteedCPU := native.PodGuaranteedCPUs(pod)
		if guaranteedCPU == 0 {
			res = append(res, pod)
		} else {
			cpus += native.PodGuaranteedCPUs(pod)
		}
	}

	if cpus > nodeCapacity {
		return res, 0, fmt.Errorf("guaranteed pod cpu request is greater than node capacity, cpus: %v, capacity: %v",
			cpus, nodeCapacity)
	}

	return res, nodeCapacity - cpus, nil
}
