package rules

import (
	"context"

	v1 "k8s.io/api/core/v1"

	"time"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// 示例 evictionRecords 数据（包含两个不同 Pod 的驱逐记录）
var evictionRecords []*EvictionRecord = []*EvictionRecord{
	{
		UID:    "pod-uid-12345", // Pod 的唯一标识符
		HasPDB: true,            // 该 Pod 关联了 PodDisruptionBudget
		Buckets: Buckets{
			List: []Bucket{
				{Time: 1620000000, Count: 2}, // 时间戳: 2021-05-03 00:00:00, 驱逐数量: 2
				{Time: 1620086400, Count: 1}, // 时间戳: 2021-05-04 00:00:00, 驱逐数量: 1
				{Time: 1620172800, Count: 3}, // 时间戳: 2021-05-05 00:00:00, 驱逐数量: 3
			},
		},
		DisruptionsAllowed: 2, // 允许中断的 Pod 数量
		CurrentHealthy:     5, // 当前健康的 Pod 数量
		DesiredHealthy:     3, // 期望保持的最小健康 Pod 数量
		ExpectedPods:       5, // 预期的总 Pod 数量
	},
	{
		UID:    "pod-uid-67890", // 另一个 Pod 的唯一标识符
		HasPDB: false,           // 该 Pod 未关联 PDB
		Buckets: Buckets{
			List: []Bucket{
				{Time: 1619827200, Count: 1}, // 时间戳: 2021-05-01 00:00:00, 驱逐数量: 1
				{Time: 1619913600, Count: 0}, // 时间戳: 2021-05-02 00:00:00, 驱逐数量: 0
			},
		},
		DisruptionsAllowed: 0, // 不允许中断（可能是关键服务）
		CurrentHealthy:     1, // 当前健康的 Pod 数量
		DesiredHealthy:     1, // 期望保持的最小健康 Pod 数量
		ExpectedPods:       1, // 预期的总 Pod 数量
	},
}

const (
	workloadEvictionInfoAnnotation = "evictionInfo"
	timeWindow1                    = 1800
	timeWindow2                    = 3600
	timeWindow3                    = 7200
	workloadName                   = "deployment"
)

// PrepareCandidatePods converts a list of v1.Pod to a list of *CandidatePod and populates
// all the necessary information for the Filter and Score stages.
func PrepareCandidatePods(_ context.Context, request *pluginapi.GetTopEvictionPodsRequest) ([]*CandidatePod, error) {
	general.Infof("prepare candidate pods")
	// if request == nil || request.CandidateEvictionRecords == nil {
	// 	general.Warningf("no candidateEvictionRecords in request")
	// }
	// var evictionRecords []*EvictionRecord
	// for _, record := range request.CandidateEvictionRecords {
	// 	evictionRecords = append(evictionRecords, record)
	// }
	pods := request.ActivePods
	var candidates []*CandidatePod
	// for _, pod := range pods {
	// 	for _, record := range evictionRecords {
	// 		if record.UID == string(pod.UID) {
	// 			workloadInfos, err := getWorkloadEvictionInfo(record)
	// 			if err != nil {
	// 				general.Warningf("get workload eviction info failed: %v", err)
	// 			}
	// 			candidates = append(candidates, &CandidatePod{
	// 				Pod:                   pod,
	// 				Scores:                make(map[string]int),
	// 				TotalScore:            0,
	// 				WorkloadsEvictionInfo: workloadInfos,
	// 				UsageRatio:            0,
	// 			})

	// 		}
	// 	}
	// }
	for _, pod := range pods {
		workloadInfos, err := getWorkloadEvictionInfo(evictionRecords[0])
		if err != nil {
			general.Warningf("get workload eviction info failed: %v", err)
		}
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
func getWorkloadEvictionInfo(evictionRecord *EvictionRecord) (map[string]*WorkloadEvictionInfo, error) {
	workloadsEvictionInfo := make(map[string]*WorkloadEvictionInfo)
	timeWindows := []int64{timeWindow1, timeWindow2, timeWindow3}
	statsByWindow, lastEvictionTime := calculateEvictionStatsByWindows(time.Now(), evictionRecord, timeWindows)
	workloadsEvictionInfo[workloadName] = &WorkloadEvictionInfo{
		WorkloadName:     workloadName,
		StatsByWindow:    statsByWindow,
		Replicas:         evictionRecord.CurrentHealthy,
		LastEvictionTime: lastEvictionTime,
		Limit:            evictionRecord.DisruptionsAllowed,
	}

	return workloadsEvictionInfo, nil
}

func calculateEvictionStatsByWindows(currentTime time.Time, evictionRecord *EvictionRecord, windows []int64) (map[float64]*EvictionStats, int64) {
	buckets := evictionRecord.Buckets.List
	currentHealthy := evictionRecord.CurrentHealthy
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
			} else if bucket.Time < windowStart && windowStart <= bucket.Time+bucket.Duration {
				totalCount += bucket.Count * (bucket.Time + bucket.Duration - windowStart) / bucket.Duration
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

func FilterCandidatePods(candidates []*CandidatePod, podsToReserve []*v1.Pod) []*CandidatePod {
	podUIDs := make(map[string]struct{})
	for _, pod := range podsToReserve {
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
