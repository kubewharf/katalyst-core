package rules

import (
	"fmt"
	"math"
	"sort"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	DeploymentEvictionFrequencyScorerName = "DeploymentEvictionFrequency"
	PriorityScorerName                    = "Priority"
	UsageGapScorerName                    = "Usage"
	RunningTimeScorerName                 = "RunningTime"
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
	PriorityScorerName:                    PriorityScorer,
	UsageGapScorerName:                    UsageGapScorer,
	RunningTimeScorerName:                 RunningTimeScore,
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
	if len(s.scorers) == 0 {
		general.Warningf("no scorers enabled, returning pods without scoring")
		return pods
	}
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
	general.Infof("scored %d pods, top score: %s, %d", len(pods), pods[0].Pod.Name, pods[0].TotalScore)
	for scorerName, score := range pods[0].Scores {
		general.Infof("%s score: %d", scorerName, score)
	}
	return pods
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
			perHourCount := int(float64(stats.EvictionCount) / window)
			countScore := normalizeCount(perHourCount, workloadInfo.Limit)
			ratioScore := stats.EvictionRatio * 10
			windowContribution := (countScore + ratioScore) * weight
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
	return int(avgScore)
}

func PriorityScorer(pod *CandidatePod, params interface{}) int {
	if pod == nil || pod.Pod == nil {
		general.Warningf("nil pod or pod spec passed to PriorityScorer")
		return 0
	}
	priority := 0
	if pod.Pod.Spec.Priority != nil {
		priority = int(*pod.Pod.Spec.Priority)
	}
	clampedPriority := priority
	if clampedPriority > 100 {
		clampedPriority = 100
	} else if clampedPriority < 0 {
		clampedPriority = 0
	}
	priorityScore := float64(clampedPriority) / 10.0
	general.Infof("PriorityScorer,  pod: %v, priorityScore:  %v", pod.Pod.Name, priorityScore)
	return int(priorityScore)
}

// params: numaOverStat []NumaOverStat
func UsageGapScorer(pod *CandidatePod, params interface{}) int {
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
		return 0
	}
	podUID := string(pod.Pod.UID)
	podHis, existMetric := numaHis[podUID]
	if !existMetric {
		general.Warningf("no metric history for pod %s on numa %d", podUID, numaID)
		return 0
	}
	metricRing, ok := podHis[targetMetric]
	if !ok {
		general.Warningf("no %s metric history for pod %s", targetMetric, podUID)
		return 0
	}
	avgUsageRatio := metricRing.Avg()
	pod.UsageRatio = avgUsageRatio
	gap := numaOverStats[0].Gap
	usageGapScore := math.Abs(avgUsageRatio-gap) * 100
	general.Infof("UsageGapScorer,  pod: %v, usageGapScore:  %v, numaGap: %v", pod.Pod.Name, usageGapScore, gap)
	return int(usageGapScore)
}

func RunningTimeScore(pod *CandidatePod, params interface{}) int {
	return 0
}

func normalizeCount(count int, limit int) float64 {
	if limit <= 0 {
		general.Warningf("invalid limit value %d, using default score 0", limit)
	}
	switch {
	case count >= limit:
		return score
	case count > 0:
		return float64(count / limit * score)
	default:
		return 0.0
	}
}
