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

package rules

import (
	"math"
	"sort"
	"strconv"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	DeploymentEvictionFrequencyScorerName  = "DeploymentEvictionFrequency"
	UsageGapScorerName                     = "Usage"
	targetMetric                           = consts.MetricCPUUsageContainer
	metricNameEvictPodScore                = "pressure_numa_evict_pod_score"
	metricNameEvictPodLimitRatio           = "pressure_numa_evict_pod_limit_ratio"
	metricNameEvictDeploymentEvictionRatio = "pressure_numa_evict_deployment_eviction_ratio"
	metricTagKeyDeploymentName             = "name"
	metricTagKeyDeploymentNamespace        = "deployment"

	score       = 100.0
	removeScore = -1000.0

	deploymentEvictionFrequencyScoreWeight       = 1.0
	deploymentEvictionFrequencyLimitLowRatio     = 0.15
	deploymentEvictionFrequencyLimitMidRatio     = 0.3
	deploymentEvictionFrequencyLimitHighRatio    = 0.5
	deploymentEvictionFrequencyScorePartition    = 0.9
	deploymentEvictionFrequencyRemoveScoreWeight = 10.0

	usageGapScoreWeight = 0.6
	usageGapLimit       = 0.3
	lowUsageLimitWeight = 0.4
)

var DefaultEnabledScorers = []string{
	UsageGapScorerName,
}

// ScoreFunc defines the function signature for scoring candidate pods
type ScoreFunc func(pod *CandidatePod, evictRules EvictRules) int

// Scorer manages a collection of scoring functions and their parameters
type Scorer struct {
	scorerFuncs  map[string]ScoreFunc
	scorerParams EvictRules
	emitter      metrics.MetricEmitter
}

// registeredScorers contains all available scoring functions that can be enabled
var registeredScorers = map[string]ScoreFunc{
	DeploymentEvictionFrequencyScorerName: DeploymentEvictionFrequencyScorer,
	UsageGapScorerName:                    UsageGapScorer,
}

func NewScorer(emitter metrics.MetricEmitter, evictRules EvictRules, enabledScorers []string) (*Scorer, error) {
	enabled := sets.NewString(enabledScorers...)
	scorers := make(map[string]ScoreFunc)

	for name := range enabled {
		if scorer, ok := registeredScorers[name]; ok {
			scorers[name] = scorer
		} else {
			general.Warningf("scorer %q is enabled but not found", name)
		}
	}
	general.Infof("initialized scorer with %d enabled scorers: %v", len(scorers), enabled)
	return &Scorer{
		scorerFuncs:  scorers,
		scorerParams: evictRules,
		emitter:      emitter,
	}, nil
}

// score return: []*CandidatePod - Sorted list of pods by total score (ascending)
func (s *Scorer) Score(pods []*CandidatePod) []*CandidatePod {
	if pods == nil {
		general.Warningf("pods is nil, returning empty list")
		return []*CandidatePod{}
	}
	if len(s.scorerFuncs) == 0 {
		general.Warningf("no scorers enabled, returning pods without scoring")
		return pods
	}
	var validPods []*CandidatePod
	for _, pod := range pods {
		if pod == nil {
			general.Warningf("nil pod found in scoring list, skipping")
			continue
		}
		validPods = append(validPods, pod)
	}
	if len(validPods) == 0 {
		general.Warningf("no valid pods found in scoring list, returning empty list")
		return []*CandidatePod{}
	}

	for _, pod := range validPods {
		if pod == nil {
			general.Warningf("nil pod found in scoring list, skipping")
			continue
		}
		pod.TotalScore = 0
		pod.Scores = make(map[string]int)
		for name, scorer := range s.scorerFuncs {
			score := scorer(pod, s.scorerParams)
			pod.Scores[name] = score
			pod.TotalScore += score
		}
	}
	sort.Slice(validPods, func(i, j int) bool {
		return validPods[i].TotalScore > validPods[j].TotalScore
	})
	deploymentName := validPods[0].Pod.Labels[metricTagKeyDeploymentName]
	if validPods[0].WorkloadsEvictionInfo != nil {
		evictionInfo, exists := validPods[0].WorkloadsEvictionInfo[workloadName]
		if exists {
			_ = s.emitter.StoreInt64(metricNameEvictPodScore, int64(validPods[0].TotalScore), metrics.MetricTypeNameRaw,
				metrics.MetricTag{
					Key: metricTagKeyDeploymentNamespace,
					Val: deploymentName,
				})
			limitRatio := float64(evictionInfo.Limit) / float64(evictionInfo.Replicas)
			_ = s.emitter.StoreFloat64(metricNameEvictPodLimitRatio, float64(limitRatio), metrics.MetricTypeNameRaw,
				metrics.MetricTag{
					Key: metricTagKeyDeploymentNamespace,
					Val: deploymentName,
				})

			for window, stats := range evictionInfo.StatsByWindow {
				_ = s.emitter.StoreFloat64(metricNameEvictDeploymentEvictionRatio, float64(stats.EvictionRatio)/float64(window), metrics.MetricTypeNameRaw,
					metrics.MetricTag{
						Key: metricTagKeyDeploymentNamespace,
						Val: deploymentName,
					},
				)
				break
			}
		}
	}
	general.Infof("scored %d pods, top score: %s, %d, bottom score: %s, %d", len(validPods), validPods[0].Pod.Name, validPods[0].TotalScore, validPods[len(validPods)-1].Pod.Name, validPods[len(validPods)-1].TotalScore)
	for scorerName, score := range validPods[0].Scores {
		general.Infof("scorer name: %s, score: %d", scorerName, score)
		_ = s.emitter.StoreInt64(metricNameEvictPodScore+"_"+scorerName, int64(score), metrics.MetricTypeNameRaw,
			metrics.MetricTag{
				Key: metricTagKeyDeploymentNamespace,
				Val: deploymentName,
			})
	}
	return validPods
}

func (s *Scorer) SetScorerParam(evictRules EvictRules) {
	s.scorerParams = evictRules
}

func (s *Scorer) SetScorer(key string, scorer ScoreFunc) {
	if key == "" {
		general.Warningf("scorer key is empty, will not set scorer")
		return
	}
	s.scorerFuncs[key] = scorer
}

func DeploymentEvictionFrequencyScorer(pod *CandidatePod, evictRules EvictRules) int {
	if len(pod.WorkloadsEvictionInfo) == 0 {
		return 0
	}
	defaultRatioLimit := []float64{deploymentEvictionFrequencyLimitLowRatio, deploymentEvictionFrequencyLimitMidRatio, deploymentEvictionFrequencyLimitHighRatio}
	deploymentEvictionFrequencyLimitStr := evictRules.DeploymentEvictionFrequencyLimitStr
	var deploymentEvictionFrequencyLimit []float64
	if len(deploymentEvictionFrequencyLimitStr) != len(defaultRatioLimit) {
		deploymentEvictionFrequencyLimit = defaultRatioLimit
	} else {
		for _, str := range deploymentEvictionFrequencyLimitStr {
			val, err := strconv.ParseFloat(str, 64)
			if err != nil {
				continue
			}
			deploymentEvictionFrequencyLimit = append(deploymentEvictionFrequencyLimit, val)
		}
	}
	if len(deploymentEvictionFrequencyLimit) != len(defaultRatioLimit) {
		deploymentEvictionFrequencyLimit = defaultRatioLimit
	}

	var totalScore float64
	var workloadCount int
	for _, workloadInfo := range pod.WorkloadsEvictionInfo {
		if len(workloadInfo.StatsByWindow) == 0 {
			continue
		}
		var windowScore float64
		var weightSum float64
		for window, stats := range workloadInfo.StatsByWindow {
			weight := 1.0 / window
			freqScore := normalizeDeploymentEvictionFrequencyScore(workloadInfo, window, stats, deploymentEvictionFrequencyLimit)
			windowContribution := freqScore * weight
			windowScore += windowContribution
			weightSum += weight
		}
		if weightSum > 0 {
			workloadScore := windowScore / weightSum
			totalScore += workloadScore
			workloadCount++
		}
	}
	if workloadCount == 0 {
		return 0
	}
	avgScore := totalScore / float64(workloadCount)
	general.Infof("DeploymentEvictionFrequencyScorer,  pod: %v, frequencyScore:  %v", pod.Pod.Name, avgScore)
	return int(math.Round(avgScore))
}

// UsageGapScorer params: numaOverStat []NumaOverStat
func UsageGapScorer(pod *CandidatePod, evictRules EvictRules) int {
	if pod == nil || pod.Pod == nil {
		return 0
	}
	numaOverStats := evictRules.NumaOverStats
	if len(numaOverStats) == 0 {
		return 0
	}
	numaID := numaOverStats[0].NumaID
	metricsHistory := numaOverStats[0].MetricsHistory
	numaHis, ok := metricsHistory.Inner[numaID]
	if !ok {
		return 0
	}
	podUID := string(pod.Pod.UID)
	podHis, existMetric := numaHis[podUID]
	if !existMetric {
		return 0
	}
	metricRing, ok := podHis[targetMetric]

	if !ok {
		return 0
	}
	avgUsageRatio := metricRing.Avg()

	pod.UsageRatio = avgUsageRatio
	gap := numaOverStats[0].Gap
	usageGapScore := normalizeUsageGapScore(gap, avgUsageRatio)
	general.Infof("UsageGapScorer,  pod: %v, usageGapScore:  %v, numaGap: %v", pod.Pod.Name, usageGapScore, gap)
	return int(math.Round(usageGapScore))
}

func normalizeUsageGapScore(gap float64, avgUsageRatio float64) float64 {
	usageGap := math.Abs(avgUsageRatio - math.Abs(gap))
	if avgUsageRatio <= lowUsageLimitWeight*gap {
		return removeScore
	}
	if 0 <= usageGap && usageGap < usageGapLimit {
		return usageGapScoreWeight * score * (1 - usageGap/usageGapLimit)
	}
	return 0
}

func normalizeDeploymentEvictionFrequencyScore(_ *WorkloadEvictionInfo, window float64, stats *EvictionStats, deploymentEvictionFrequencyLimit []float64) float64 {
	normalizedPerHourEvictionRatio := stats.EvictionRatio / window
	lowRatio := deploymentEvictionFrequencyLimit[0]
	midRatio := deploymentEvictionFrequencyLimit[1]
	highRatio := deploymentEvictionFrequencyLimit[2]

	if normalizedPerHourEvictionRatio >= highRatio {
		return removeScore * deploymentEvictionFrequencyRemoveScoreWeight * normalizedPerHourEvictionRatio
	} else if normalizedPerHourEvictionRatio < highRatio && normalizedPerHourEvictionRatio >= midRatio {
		ratio := 1 - (normalizedPerHourEvictionRatio-midRatio)/
			(highRatio-midRatio)
		return deploymentEvictionFrequencyScoreWeight * ratio * (1 - deploymentEvictionFrequencyScorePartition) * score
	} else if normalizedPerHourEvictionRatio < midRatio && normalizedPerHourEvictionRatio >= lowRatio {
		ratio := 1 - (normalizedPerHourEvictionRatio-lowRatio)/
			(midRatio-lowRatio)
		middleScoreRatio := deploymentEvictionFrequencyScorePartition/2 + ratio*(1-deploymentEvictionFrequencyScorePartition)
		return deploymentEvictionFrequencyScoreWeight * middleScoreRatio * score
	} else if normalizedPerHourEvictionRatio < lowRatio {
		ratio := 1 - normalizedPerHourEvictionRatio/lowRatio
		return deploymentEvictionFrequencyScoreWeight * (deploymentEvictionFrequencyScorePartition + ratio*(1-deploymentEvictionFrequencyScorePartition)) * score
	}
	return 0
}

func RegisterScorer(scorerName string, scorer ScoreFunc) {
	registeredScorers[scorerName] = scorer
}
