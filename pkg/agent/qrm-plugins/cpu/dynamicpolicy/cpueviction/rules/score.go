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
	"fmt"
	"math"
	"sort"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	DeploymentEvictionFrequencyScorerName = "DeploymentEvictionFrequency"
	PriorityScorerName                    = "Priority"
	UsageGapScorerName                    = "Usage"
	score                                 = 10.0
	targetMetric                          = consts.MetricCPUUsageContainer
)

// ScoreFunc defines the function signature for scoring candidate pods
type ScoreFunc func(pod *CandidatePod, params interface{}) int

// Scorer manages a collection of scoring functions and their parameters
type Scorer struct {
	scorers      map[string]ScoreFunc
	scorerParams map[string]interface{}
	emitter      metrics.MetricEmitter
}

// allScores contains all available scoring functions that can be enabled
var allScores = map[string]ScoreFunc{
	DeploymentEvictionFrequencyScorerName: DeploymentEvictionFrequencyScorer,
	UsageGapScorerName:                    UsageGapScorer,
	PriorityScorerName:                    PriorityScorer,
}

func NewScorer(enabledScorers []string, emitter metrics.MetricEmitter, scorerParams map[string]interface{}) (*Scorer, error) {
	if scorerParams == nil {
		general.Warningf("scorerParams is nil, using empty parameters for all scorers")
		scorerParams = make(map[string]interface{})
	}
	enabled := sets.NewString(enabledScorers...)
	scorers := make(map[string]ScoreFunc)
	params := make(map[string]interface{})
	for name := range enabled {
		scorer, exists := allScores[name]
		if !exists {
			return nil, fmt.Errorf("scorer %s not exists", name)
		}
		scorers[name] = scorer
		params[name] = scorerParams[name]
	}
	general.Infof("initialized scorer with %d enabled scorers", len(scorers))
	return &Scorer{
		scorers:      scorers,
		scorerParams: params,
		emitter:      emitter,
	}, nil
}

// return: []*CandidatePod - Sorted list of pods by total score (ascending)
func (s *Scorer) Score(pods []*CandidatePod) []*CandidatePod {
	if pods == nil {
		general.Warningf("pods is nil, returning empty list")
		return []*CandidatePod{}
	}
	if len(s.scorers) == 0 {
		general.Warningf("no scorers enabled, returning pods without scoring")
		return pods
	}
	var validPods []*CandidatePod
	for _, pod := range pods {
		if pod == nil {
			general.Warningf("nil pod found in scoring list, skipping")
			continue
		}
		validPods = append(validPods, pod) // 仅保留非 nil Pod
	}
	pods = validPods

	for _, pod := range pods {
		if pod == nil {
			general.Warningf("nil pod found in scoring list, skipping")
			continue
		}
		pod.TotalScore = 0
		pod.Scores = make(map[string]int)
		for name, scorer := range s.scorers {
			score := scorer(pod, s.scorerParams[name])
			pod.Scores[name] = score
			pod.TotalScore += score
		}
		for name, score := range pod.Scores {
			_ = s.emitter.StoreFloat64("qrm_eviction_scorer_score_distribution", float64(score), metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "scorer_name", Val: name},
			)
			totalScore := pod.TotalScore
			contribution := float64(score) / float64(totalScore) * 100
			_ = s.emitter.StoreFloat64("qrm_eviction_scorer_weight_impact", contribution, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "scorer_name", Val: name},
				metrics.MetricTag{Key: "impact_type", Val: "score_contribution"},
			)
		}
	}
	sort.Slice(pods, func(i, j int) bool {
		return pods[i].TotalScore < pods[j].TotalScore
	})
	general.Infof("scored %d pods, bottom score: %s, %d", len(pods), pods[0].Pod.Name, pods[0].TotalScore)
	for scorerName, score := range pods[0].Scores {
		general.Infof("scorer name: %s, score: %d", scorerName, score)
	}
	return pods
}

func (s *Scorer) SetScorerParam(key string, value interface{}) {
	if key == "" {
		general.Warningf("scorer key is empty, will not set scorer param")
		return
	}
	s.scorerParams[key] = value
}

func (s *Scorer) SetScorer(key string, scorer ScoreFunc) {
	if key == "" {
		general.Warningf("scorer key is empty, will not set scorer")
		return
	}
	s.scorers[key] = scorer
}

func DeploymentEvictionFrequencyScorer(pod *CandidatePod, params interface{}) int {
	if pod == nil || len(pod.WorkloadsEvictionInfo) == 0 {
		general.Warningf("no eviction info for pod %s", pod.Pod.Name)
		return 0
	}
	var totalScore float64
	var workloadCount int
	for _, workloadInfo := range pod.WorkloadsEvictionInfo {
		if workloadInfo == nil || len(workloadInfo.StatsByWindow) == 0 {
			general.Warningf("no eviction info for workload %s", workloadInfo.WorkloadName)
			continue
		}
		var windowScore float64
		var weightSum float64
		for window, stats := range workloadInfo.StatsByWindow {
			weight := 1.0 / window
			perHourCount := float64(stats.EvictionCount) / window
			general.Infof("limit: %v, perHourCount: %v", workloadInfo.Limit, perHourCount)
			countScore := normalizeCount(perHourCount, workloadInfo.Limit)
			windowContribution := countScore * stats.EvictionRatio * 10
			general.Infof("window: %v, countScore: %v, ratio: %v, windowContribution: %v", window, countScore, stats.EvictionRatio, windowContribution)
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
	// general.Infof("workloadCount: %d, frequency totalScore:  %v", workloadCount, totalScore)
	avgScore := totalScore / float64(workloadCount)
	general.Infof("DeploymentEvictionFrequencyScorer,  pod: %v, frequencyScore:  %v", pod.Pod.Name, avgScore)
	return int(avgScore)
}

// params: numaOverStat []NumaOverStat
func UsageGapScorer(pod *CandidatePod, params interface{}) int {
	if pod == nil || pod.Pod == nil {
		general.Warningf("nil pod or pod spec passed to UsageGapScorer")
		return 0
	}
	numaOverStats, ok := params.([]NumaOverStat)
	if !ok {
		general.Errorf("UsageGapScorer params type error, params: %v", params)
		return 0
	}
	if len(numaOverStats) == 0 {
		general.Errorf("UsageGapScorer received empty numaOverStats")
		return 0
	}
	numaID := numaOverStats[0].NumaID
	metricsHistory := numaOverStats[0].MetricsHistory
	numaHis, ok := metricsHistory.Inner[numaID]
	if !ok {
		general.Warningf("no metrics history for numa %d", numaID)
		return score
	}
	podUID := string(pod.Pod.UID)
	podHis, existMetric := numaHis[podUID]
	if !existMetric {
		general.Warningf("no metric history for pod %s on numa %d", podUID, numaID)
		return score
	}
	metricRing, ok := podHis[targetMetric]

	if !ok {
		general.Warningf("no %s metric history for pod %s", targetMetric, podUID)
		return score
	}
	avgUsageRatio := metricRing.Avg()

	pod.UsageRatio = avgUsageRatio
	gap := numaOverStats[0].Gap
	// choose low score
	usageGapScore := math.Abs(avgUsageRatio-math.Abs(gap)) * 100
	general.Infof("UsageGapScorer,  pod: %v, usageGapScore:  %v, numaGap: %v", pod.Pod.Name, usageGapScore, gap)
	return int(usageGapScore)
}

func PriorityScorer(pod *CandidatePod, params interface{}) int {
	return 0
}

func normalizeCount(perHourCount float64, limit int32) float64 {
	if limit <= 0 {
		general.Warningf("invalid limit value %d, using perHourCount", limit)
		return perHourCount
	} else {
		return perHourCount / float64(limit) * score
	}
}
