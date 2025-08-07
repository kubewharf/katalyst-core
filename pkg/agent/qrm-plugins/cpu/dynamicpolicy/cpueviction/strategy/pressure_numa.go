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

package strategy

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/history"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/rules"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const EvictionNameNumaCpuPressure = "numa-cpu-pressure-plugin"

const evictionConditionCPUUsagePressure = "NumaCPUPressure"

const (
	metricsNameNumaCollectMetricsCalled = "numa_cpu_pressure_usage_collect_metrics_called"
	// metricsNamePodRaw  = "numa_cpu_pressure_pod_raw"
	metricsNameNumaRaw = "numa_cpu_pressure_numa_raw"

	metricsNameNumaThresholdMet = "numa_cpu_pressure_threshold_met"
	metricsNameGetEvictPods     = "numa_cpu_pressure_get_evict_pods"

	metricNameNumaOverloadNumaCount = "numa_cpu_pressure_overload_numa_count"
	metricNameNumaOverloadRatio     = "numa_cpu_pressure_overload_ratio"
	metricNameNumaOverloadTargetPod = "numa_cpu_pressure_overload_target_pod"

	metricNameMetricNoThreshold = "numa_cpu_pressure_metric_no_threshold"

	metricTagMetricName = "metric_name"
	metricTagNuma       = "numa"
	// metricTagPod        = "pod"
	metricTagIsOverload = "is_overload"
)

var (
	targetMetric = consts.MetricCPUUsageContainer
	// targetMetrics      = []string{consts.MetricCPUUsageContainer, consts.MetricLoad1MinContainer}
	targetMetrics = []string{consts.MetricCPUUsageContainer}
)

type NumaCPUPressureEviction struct {
	sync.RWMutex
	state      state.ReadonlyState
	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer

	conf               *config.Configuration
	numaPressureConfig *NumaPressureConfig

	syncPeriod time.Duration

	thresholds map[string]float64

	metricsHistory    *history.NumaMetricHistory
	overloadNumaCount int

	enabled bool

	numaOverStats []rules.NumaOverStat
	filterer      *rules.Filterer
	scorer        *rules.Scorer
}

func NewCPUPressureUsageEviction(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, state state.ReadonlyState,
) (CPUPressureEviction, error) {
	numaPressureConfig := getNumaPressureConfig(conf.GetDynamicConfiguration())

	return &NumaCPUPressureEviction{
		state:              state,
		emitter:            emitter,
		metaServer:         metaServer,
		conf:               conf,
		numaPressureConfig: numaPressureConfig,
		metricsHistory:     history.NewMetricHistory(numaPressureConfig.MetricRingSize),
		syncPeriod:         15 * time.Second,
	}, nil
}

func (p *NumaCPUPressureEviction) initFilterAndScorer() error {
	// filterParams
	enabledFilters := p.numaPressureConfig.EnabledFilters
	general.Infof("enabledFilters: %v", enabledFilters)
	filterParams := map[string]interface{}{
		rules.OwnerRefFilterName:      p.numaPressureConfig.SkippedPodKinds, // OwnerRefFilter params
		rules.OverRatioNumaFilterName: p.numaOverStats,
	}
	filterer, err := rules.NewFilter(enabledFilters, p.emitter, filterParams)
	if err != nil {
		return fmt.Errorf("failed to create filterer: %v", err)
	}
	general.Infof("create filterer success: %v", filterer)

	// scorerParams
	enabledScorers := p.numaPressureConfig.EnabledScorers
	general.Infof("enabledScorers: %v", enabledScorers)
	scorerParams := map[string]interface{}{
		rules.DeploymentEvictionFrequencyScorerName: nil,
		rules.UsageGapScorerName:                    p.numaOverStats,
	}
	scorer, err := rules.NewScorer(enabledScorers, p.emitter, scorerParams)
	if err != nil {
		return fmt.Errorf("failed to create scorer: %v", err)
	}
	general.Infof("create scorer success: %v", scorer)
	p.filterer = filterer
	p.scorer = scorer
	return nil
}

func (p *NumaCPUPressureEviction) SetEvictionFilter(key string, filter rules.FilterFunc) {
	p.filterer.SetFilter(key, filter)
}
func (p *NumaCPUPressureEviction) SetEvictionFilterParam(key string, value interface{}) {
	p.filterer.SetFilterParam(key, value)
}

func (p *NumaCPUPressureEviction) SetEvictionScorer(key string, scorer rules.ScoreFunc) {
	p.scorer.SetScorer(key, scorer)
}
func (p *NumaCPUPressureEviction) SetEvictionScorerParam(key string, value interface{}) {
	p.scorer.SetScorerParam(key, value)
}

func (p *NumaCPUPressureEviction) Start(ctx context.Context) (err error) {
	general.Infof("%s", p.Name())
	if err := p.initFilterAndScorer(); err != nil {
		general.Errorf("init filter and scorer failed: %v", err)
		return err
	}
	go wait.UntilWithContext(ctx, p.update, p.syncPeriod)
	go wait.UntilWithContext(ctx, p.pullThresholds, p.syncPeriod)
	return
}

func (p *NumaCPUPressureEviction) Name() string { return EvictionNameNumaCpuPressure }

// todo may change to GetTopEvictionPods?
func (p *NumaCPUPressureEviction) GetEvictPods(_ context.Context, _ *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	general.Infof("GetEvictPods called")
	return &pluginapi.GetEvictPodsResponse{}, nil
}

func (p *NumaCPUPressureEviction) ThresholdMet(_ context.Context, req *pluginapi.GetThresholdMetRequest,
) (*pluginapi.ThresholdMetResponse, error) {
	p.RLock()
	defer p.RUnlock()

	if !p.enabled {
		general.Infof("numa cpu pressure eviction is disabled")
		return &pluginapi.ThresholdMetResponse{
			MetType: pluginapi.ThresholdMetType_NOT_MET,
		}, nil
	}

	if req == nil || req.ActivePods == nil {
		general.Warningf("no active pods in request")
		return &pluginapi.ThresholdMetResponse{
			MetType: pluginapi.ThresholdMetType_NOT_MET,
		}, nil
	}

	nodeOverload := p.isNodeOverload()
	overloadNumaCount := p.overloadNumaCount
	if overloadNumaCount == 0 {
		_ = p.emitter.StoreFloat64(metricsNameNumaThresholdMet, 0, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricTagMetricName: targetMetric,
			})...)
		return &pluginapi.ThresholdMetResponse{
			MetType: pluginapi.ThresholdMetType_NOT_MET,
		}, nil
	}

	activePods := req.ActivePods
	p.SetEvictionFilterParam(rules.OverRatioNumaFilterName, p.numaOverStats)
	filteredPods := p.filterer.Filter(activePods)

	if len(filteredPods) == 0 {
		general.Warningf("got empty active pods list after filter")
		return &pluginapi.ThresholdMetResponse{
			MetType: pluginapi.ThresholdMetType_NOT_MET,
		}, nil
	}

	general.Infof("filteredPods: %v", len(filteredPods))

	if nodeOverload {
		_ = p.emitter.StoreFloat64(metricsNameNumaThresholdMet, 0, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricTagMetricName: targetMetric,
			})...)
		general.Infof("all numa overload : %v", overloadNumaCount)
		return &pluginapi.ThresholdMetResponse{
			ThresholdValue:    1,
			ObservedValue:     1,
			ThresholdOperator: pluginapi.ThresholdOperator_GREATER_THAN,
			MetType:           pluginapi.ThresholdMetType_HARD_MET,
			EvictionScope:     targetMetric,
			Condition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionCPUUsagePressure,
				MetCondition:  true,
			},
			CandidatePods: filteredPods,
		}, nil
	}
	general.Infof("not all numa overload : %v", overloadNumaCount)
	_ = p.emitter.StoreFloat64(metricsNameNumaThresholdMet, 1, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricTagMetricName: targetMetric,
		})...)

	return &pluginapi.ThresholdMetResponse{
		ThresholdValue:    1,
		ObservedValue:     1,
		ThresholdOperator: pluginapi.ThresholdOperator_GREATER_THAN,
		MetType:           pluginapi.ThresholdMetType_HARD_MET,
		EvictionScope:     targetMetric,
		CandidatePods:     filteredPods,
	}, nil
}

func (p *NumaCPUPressureEviction) GetTopEvictionPods(ctx context.Context, request *pluginapi.GetTopEvictionPodsRequest,
) (*pluginapi.GetTopEvictionPodsResponse, error) {
	general.Infof("GetTopEvictionPods called")
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	} else if len(request.ActivePods) == 0 {
		general.Warningf("got empty active pods list")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	p.RLock()
	defer p.RUnlock()

	if !p.enabled {
		general.Infof("numa cpu pressure eviction is disabled")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	if p.overloadNumaCount == 0 {
		_ = p.emitter.StoreInt64(metricsNameGetEvictPods, 0, metrics.MetricTypeNameRaw)
		general.Infof("overloadNumaCount is 0")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	activePods := request.ActivePods
	// p.SetEvictionFilterParam(rules.OverRatioNumaFilterName, p.numaOverStats)
	p.SetEvictionScorerParam(rules.UsageGapScorerName, p.numaOverStats)
	general.Infof("activePods: %v", len(activePods))

	filteredPods := p.filterer.Filter(activePods)

	if len(filteredPods) == 0 {
		general.Warningf("got empty active pods list after filter")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	general.Infof("getTopEvictionPods filteredPods: %v", len(filteredPods))

	//1.get annotation of pods
	candidatePods, _ := rules.PrepareCandidatePods(ctx, request)
	general.Infof("candidatePods: %v", len(candidatePods))
	// to delete
	candidatePods = rules.FilterCandidatePods(candidatePods, filteredPods)
	general.Infof("candidatePods after filter: %v", len(candidatePods))

	candidatePods = p.scorer.Score(candidatePods)
	general.Infof("candidatePods after scorer: %v", candidatePods)
	// todo may pick multiple numas if overload
	if len(p.numaOverStats) == 0 {
		general.Warningf("no numa over stats")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}
	numaID := p.numaOverStats[0].NumaID
	numaUsageRatio := p.numaOverStats[0].AvgUsageRatio
	numaOverloadRatio := p.numaOverStats[0].OverloadRatio

	topPod := candidatePods[0].Pod
	general.InfoS("evict pod", "pod", topPod.Name, "podUsageRatio",
		candidatePods[0].UsageRatio, "numa", numaID, "numaOverloadRatio", numaOverloadRatio, "numaUsageRatio", numaUsageRatio)

	resp := &pluginapi.GetTopEvictionPodsResponse{
		TargetPods: []*v1.Pod{topPod},
	}

	if gracePeriod := p.numaPressureConfig.GracePeriod; gracePeriod >= 0 {
		deletionOptions := &pluginapi.DeletionOptions{
			GracePeriodSeconds: gracePeriod,
		}
		resp.DeletionOptions = deletionOptions
	}

	_ = p.emitter.StoreFloat64(metricsNameGetEvictPods, 1, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricTagMetricName: targetMetric,
			metricTagNuma:       strconv.Itoa(numaID),
		})...)

	return resp, nil
}

// 1 collect numa / pod level metrics
// 2 update node / numa overload status
func (p *NumaCPUPressureEviction) update(_ context.Context) {
	p.Lock()
	defer p.Unlock()

	if !p.enabled {
		general.Infof("numa cpu pressure eviction is disabled")
		return
	}

	_ = p.emitter.StoreInt64(metricsNameNumaCollectMetricsCalled, 1, metrics.MetricTypeNameRaw)

	general.Infof("start to collect numa pod metrics")
	machineState := p.state.GetMachineState()

	for _, metricName := range targetMetrics {
		// numa -> pod -> ring
		for numaID := 0; numaID < p.metaServer.NumNUMANodes; numaID++ {
			numaSize := p.metaServer.NUMAToCPUs.CPUSizeInNUMAs(numaID)
			snbEntries := machineState[numaID].PodEntries.GetFilteredPodEntries(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedNUMABinding))

			sum := 0.0
			for podUID, containerEntries := range snbEntries {
				for containerName := range containerEntries {
					val, err := p.metaServer.GetContainerMetric(podUID, containerName, metricName)
					general.Infof("get pod metric, podUID %v, containerName %v, metricName %v, numa %v, pod %v, metric %v, val %v",
						podUID, containerName, metricName, numaID, podUID, metricName, val)
					if err != nil {
						general.Warningf("failed to get pod metric, numa %v, pod %v, metric %v err: %v",
							numaID, podUID, metricName, err)
					}
					valRatio := val.Value / float64(numaSize)
					p.metricsHistory.Push(numaID, podUID, metricName, valRatio)
					general.InfoS("Push pod metric", "metric", metricName, "numa", numaID, "pod", podUID, "value", valRatio)

					sum += valRatio
				}
			}
			p.metricsHistory.PushNuma(numaID, metricName, sum)
			general.InfoS("Push numa metric", "metric", metricName, "numa", numaID, "value", sum)
			_ = p.emitter.StoreFloat64(metricsNameNumaRaw, sum, metrics.MetricTypeNameRaw,
				metrics.ConvertMapToTags(map[string]string{
					metricTagMetricName: metricName,
					metricTagNuma:       strconv.Itoa(numaID),
				})...)
		}
	}

	// update overload numa count and node overload
	p.overloadNumaCount = p.calOverloadNumaCount()
	general.Infof("overload numa count %v", p.overloadNumaCount)
	_, _, _, err := p.pickTopOverRatioNuma(targetMetric, p.thresholds)
	if err != nil {
		general.Warningf("failed to update numaOverStat: %v", err)
	}
}

func (p *NumaCPUPressureEviction) isNodeOverload() (nodeOverload bool) {
	numaCount := p.metaServer.NumNUMANodes
	overloadNumaCount := p.overloadNumaCount

	nodeOverload = overloadNumaCount == numaCount
	general.Infof("Update node overload %v", nodeOverload)

	return
}

func (p *NumaCPUPressureEviction) calOverloadNumaCount() (overloadNumaCount int) {
	thresholds := p.thresholds
	thresholdMetPercentage := p.numaPressureConfig.ThresholdMetPercentage
	metricsHistory := p.metricsHistory
	emitter := p.emitter

	for numaID, numaHis := range metricsHistory.Inner {
		numaHisInner := numaHis[history.FakePodUID]
		var numaOver bool
		for metricName, metricRing := range numaHisInner {
			threshold, exist := thresholds[metricName]
			if !exist {
				general.Warningf("no threshold for metric %v", metricName)
				_ = emitter.StoreFloat64(metricNameMetricNoThreshold, 1, metrics.MetricTypeNameRaw,
					metrics.ConvertMapToTags(map[string]string{
						metricTagMetricName: metricName,
					})...)
				continue
			}
			// per numa metric
			overCount := metricRing.OverCount(threshold)
			// whether current numa is overload
			overRatio := float64(overCount) / float64(metricRing.MaxLen)

			numaOver = overRatio >= thresholdMetPercentage

			_ = emitter.StoreFloat64(metricNameNumaOverloadRatio, overRatio, metrics.MetricTypeNameRaw,
				metrics.ConvertMapToTags(map[string]string{
					metricTagMetricName: metricName,
					metricTagNuma:       strconv.Itoa(numaID),
					metricTagIsOverload: strconv.FormatBool(numaOver),
				})...)

			if numaOver {
				general.InfoS("numa is overload", "numaID", numaID, "metric", metricName, "usageRatio", overRatio)
				break
			}
		}

		if numaOver {
			overloadNumaCount++
		}
	}
	_ = emitter.StoreInt64(metricNameNumaOverloadNumaCount, int64(overloadNumaCount), metrics.MetricTypeNameRaw)
	general.Infof("Update overload numa count %v", overloadNumaCount)
	return
}

func (p *NumaCPUPressureEviction) pickTopOverRatioNuma(metricName string, thresholds map[string]float64) (int, float64, float64, error) {
	var numaOverRatios []rules.NumaOverStat
	for numaID, numaHis := range p.metricsHistory.Inner {
		numaHisInner := numaHis[history.FakePodUID]
		metricRing, ok := numaHisInner[metricName]
		if !ok {
			continue
		}
		threshold := thresholds[metricName]
		overCount := metricRing.OverCount(threshold)
		overRatio := float64(overCount) / float64(metricRing.MaxLen)
		// threshold := p.thresholds[targetMetric] / p.numaPressureConfig.ExpandFactor
		numaUsageRatio := metricRing.Avg()
		gap := numaUsageRatio - (p.thresholds[targetMetric] / p.numaPressureConfig.ExpandFactor)
		numaOverRatios = append(numaOverRatios, rules.NumaOverStat{
			NumaID:         numaID,
			OverloadRatio:  overRatio,
			AvgUsageRatio:  numaUsageRatio,
			MetricsHistory: p.metricsHistory,
			Gap:            gap,
		})

	}

	sort.Slice(numaOverRatios, func(i, j int) bool {
		return numaOverRatios[i].OverloadRatio > numaOverRatios[j].OverloadRatio
	})
	p.numaOverStats = numaOverRatios
	for _, numa := range numaOverRatios {
		general.InfoS("Numa with overload ratio", "numa", numa.NumaID, "overloadRatio",
			numa.OverloadRatio, "avgUsageRatio", numa.AvgUsageRatio)
	}

	if len(numaOverRatios) == 0 {
		return -1, 0, 0, fmt.Errorf("no valid numa found")
	}
	top := numaOverRatios[0]
	return top.NumaID, top.OverloadRatio, top.AvgUsageRatio, nil
}

func (p *NumaCPUPressureEviction) pickTargetPod(numaID int, metricName string,
	activePods []*v1.Pod, numaUsageRatio float64,
) (*v1.Pod, float64, error) {
	var podWithUsages []PodWithUsage
	numaHis, ok := p.metricsHistory.Inner[numaID]
	if !ok {
		return nil, 0, fmt.Errorf("cannot find numa %v usage", numaID)
	}
	for _, pod := range activePods {
		podUID := string(pod.UID)
		podHis, existMetric := numaHis[podUID]
		if !existMetric {
			continue
		}
		metricRing, ok := podHis[metricName]
		if !ok {
			continue
		}
		avgUsageRatio := metricRing.Avg()
		podWithUsages = append(podWithUsages, PodWithUsage{
			pod:        pod,
			usageRatio: avgUsageRatio,
		})
	}

	if len(podWithUsages) <= 1 {
		return nil, 0, fmt.Errorf("cannot find enough pod for numa %v, pod count: %v", numaID, len(podWithUsages))
	}

	if len(podWithUsages) == 0 {
		return nil, 0, fmt.Errorf("cannot find any pod to be evicted")
	}

	// gap = current - original threshold
	threshold := p.thresholds[targetMetric] / p.numaPressureConfig.ExpandFactor
	gap := numaUsageRatio - threshold
	if gap <= 0 {
		return nil, 0, fmt.Errorf("no need to evict pod with avg usage %v", numaUsageRatio)
	}
	general.InfoS("Try to find candidate pods", "gap", gap, "numaID", numaID,
		"numaUsageRatio", numaUsageRatio, "candidateCount", p.numaPressureConfig.CandidateCount)

	// find candidate pods
	pods, err := findCandidatePods(podWithUsages, gap, p.numaPressureConfig.CandidateCount)
	if err != nil {
		return nil, 0, err
	}
	// select one randomly
	targetPod := selectPodRandomly(pods)

	_ = p.emitter.StoreFloat64(metricNameNumaOverloadTargetPod, targetPod.usageRatio, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricTagMetricName: metricName,
			metricTagNuma:       strconv.Itoa(numaID),
		})...)
	return targetPod.pod, targetPod.usageRatio, nil
}

func findCandidatePods(pods []PodWithUsage, gap float64, candidateCount int) ([]PodWithUsage, error) {
	if len(pods) < candidateCount {
		return nil, fmt.Errorf("pod slice must contain at least %v elements", candidateCount)
	}

	sort.Slice(pods, func(i, j int) bool {
		diffI := math.Abs(pods[i].usageRatio - gap)
		diffJ := math.Abs(pods[j].usageRatio - gap)
		return diffI < diffJ
	})

	for _, pod := range pods {
		general.InfoS("Show candidate pods", "podName", pod.pod.Name, "usageRatio", pod.usageRatio)
	}

	return pods[:candidateCount], nil
}

func selectPodRandomly(pods []PodWithUsage) *PodWithUsage {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	randomIndex := r.Intn(len(pods))
	target := &pods[randomIndex]
	general.InfoS("Select target pod", "podName", target.pod.Name, "usageRatio", target.usageRatio)
	return target
}

type PodWithUsage struct {
	pod        *v1.Pod
	usageRatio float64
}

type NumaPressureConfig struct {
	MetricRingSize         int
	ThresholdMetPercentage float64
	GracePeriod            int64
	ExpandFactor           float64
	CandidateCount         int
	SkippedPodKinds        []string
	EnabledFilters         []string
	EnabledScorers         []string
}

func getNumaPressureConfig(conf *dynamic.Configuration) *NumaPressureConfig {
	return &NumaPressureConfig{
		MetricRingSize:         conf.NumaCPUPressureEvictionConfiguration.MetricRingSize,
		ThresholdMetPercentage: conf.NumaCPUPressureEvictionConfiguration.ThresholdMetPercentage,
		GracePeriod:            conf.NumaCPUPressureEvictionConfiguration.GracePeriod,
		ExpandFactor:           conf.NumaCPUPressureEvictionConfiguration.ThresholdExpandFactor,
		CandidateCount:         conf.NumaCPUPressureEvictionConfiguration.CandidateCount,
		SkippedPodKinds:        conf.NumaCPUPressureEvictionConfiguration.SkippedPodKinds,
		EnabledFilters:         conf.NumaCPUPressureEvictionConfiguration.EnabledFilters,
		EnabledScorers:         conf.NumaCPUPressureEvictionConfiguration.EnabledScorers,
	}
}

func (p *NumaCPUPressureEviction) OverRatioNumaFilter(activePods []*v1.Pod, numaStats []rules.NumaOverStat) ([]*v1.Pod, error) {

	numaID := numaStats[0].NumaID
	numaHis, ok := p.metricsHistory.Inner[numaID]
	// _, existMetric := numaHis[string(pod.UID)]
	if !ok {
		return nil, fmt.Errorf("cannot find numa %v usage", numaID)
	}
	var filterdPods []*v1.Pod
	for _, pod := range activePods {
		podUID := string(pod.UID)
		_, existMetric := numaHis[podUID]
		if existMetric {
			filterdPods = append(filterdPods, pod)
		}
	}
	return filterdPods, nil
}
