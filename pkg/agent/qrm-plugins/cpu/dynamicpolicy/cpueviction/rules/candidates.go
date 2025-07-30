package rules

import (
	"context"
	"encoding/json"

	v1 "k8s.io/api/core/v1"

	"time"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const evictionInfoStr = `{
	  "pdbStatus": {
	    "expired": false,
	    "buckets": {
	      "list": [
	        {"startTime": 1620000000, "count": 5},
	        {"startTime": 1620003600, "count": 3},
			{"startTime": 1620007200, "count": 2},
			{"startTime": 1620010800, "count": 1},
			{"startTime": 1620014400, "count": 0}
	      ]
	    },
	    "disruptionsAllowed": 10,
	    "currentHealthy": 8,
	    "desiredHealthy": 10,
	    "expectedPods": 10
	  }
	}`

const (
	workloadEvictionInfoAnnotation = "evictionInfo"
	timeWindow1                    = 1800
	timeWindow2                    = 3600
	timeWindow3                    = 7200
	workloadName                   = "deployment"
)

// PrepareCandidatePods converts a list of v1.Pod to a list of *CandidatePod and populates
// all the necessary information for the Filter and Score stages.
func PrepareCandidatePods(_ context.Context, request *pluginapi.GetEvictPodsRequest) ([]*CandidatePod, error) {
	workloadInfos, err := getWorkloadEvictionInfo(request)
	if err != nil {
		general.Warningf("get workload eviction info failed: %v", err)
	}
	pods := request.ActivePods
	var candidates []*CandidatePod
	for _, pod := range pods {

		candidates = append(candidates, &CandidatePod{
			Pod:                   pod,
			Scores:                make(map[string]int),
			TotalScore:            0,
			WorkloadsEvictionInfo: workloadInfos,
			UsageRatio:            0,
		})
	}

	return candidates, nil
}

// getWorkloadEvictionInfo extracts WorkloadEvictionInfo from the request.
func getWorkloadEvictionInfo(req *pluginapi.GetEvictPodsRequest) (map[string]*WorkloadEvictionInfo, error) {
	workloadsEvictionInfo := make(map[string]*WorkloadEvictionInfo)
	// if req == nil || req.Annotations == nil {
	// 	general.Warningf("no annotations in request")
	// }
	// evictionInfoStr, ok := req.Annotations[workloadEvictionInfoAnnotation]
	// if !ok {
	// 	general.Warningf("annotation %s not found", workloadEvictionInfoAnnotation)
	// }

	var evictionResp FetchEvictionRecordResp
	if err := json.Unmarshal([]byte(evictionInfoStr), &evictionResp); err != nil {
		general.Errorf("failed to unmarshal eviction data: %v, raw data : %s", err, evictionInfoStr)
	}

	timeWindows := []int64{timeWindow1, timeWindow2, timeWindow3}
	statsByWindow, lastEvictionTime := calculateEvictionStatsByWindows(time.Now(), evictionResp, timeWindows)
	workloadsEvictionInfo[workloadName] = &WorkloadEvictionInfo{
		WorkloadName:     workloadName,
		StatsByWindow:    statsByWindow,
		Replicas:         evictionResp.CurrentHealthy,
		LastEvictionTime: lastEvictionTime,
		Limit:            0,
	}

	return workloadsEvictionInfo, nil
}

func calculateEvictionStatsByWindows(currentTime time.Time, evictionResp FetchEvictionRecordResp, windows []int64) (map[float64]*EvictionStats, int64) {
	buckets := evictionResp.Buckets.List
	currentHealthy := evictionResp.CurrentHealthy
	statsByWindow := make(map[float64]*EvictionStats)
	if len(buckets) == 0 {
		general.Warningf("no buckets in eviction info")
		return statsByWindow, 0
	}

	currentTimestamp := currentTime.Unix()
	lastEvictionTime := buckets[0].Time
	for _, windowSec := range windows {
		windowHour := float64(windowSec) / 3600

		windowStart := currentTimestamp - windowSec
		var totalCount int64
		for _, bucket := range buckets {
			if bucket.Time >= windowStart {
				totalCount += bucket.Count
			}
			if bucket.Time > lastEvictionTime {
				lastEvictionTime = bucket.Time
			}
		}
		statsByWindow[windowHour] = &EvictionStats{
			EvictionCount: totalCount,
			EvictionRatio: float64(totalCount) / float64(currentHealthy),
		}
	}

	return statsByWindow, lastEvictionTime

}

func ConvertCandidatesToPods(candidates []*CandidatePod) []*v1.Pod {
	pods := make([]*v1.Pod, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate != nil && candidate.Pod != nil {
			pods = append(pods, candidate.Pod)
		}
	}
	return pods
}

func FilterCandidatePods(candidates []*CandidatePod, podsToRemove []*v1.Pod) []*CandidatePod {
	podUIDs := make(map[string]struct{})
	for _, pod := range podsToRemove {
		if pod != nil {
			podUIDs[string(pod.UID)] = struct{}{}
		}
	}

	var filtered []*CandidatePod
	for _, candidate := range candidates {
		if candidate == nil || candidate.Pod == nil {
			continue
		}
		if _, exists := podUIDs[string(candidate.Pod.UID)]; exists {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}
