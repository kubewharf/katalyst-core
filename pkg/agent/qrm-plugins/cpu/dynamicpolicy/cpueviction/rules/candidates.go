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
	"context"
	"fmt"
	"time"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	v1 "k8s.io/api/core/v1"
)

func getEvictionRecords() []*EvictionRecord {
	now := time.Now()
	return []*EvictionRecord{
		{
			UID:    "pod-uid-12345",
			HasPDB: true,
			Buckets: Buckets{
				List: []Bucket{
					{Time: now.Add(-120 * time.Minute).Unix(), Duration: 1800, Count: 4},
					{Time: now.Add(-60 * time.Minute).Unix(), Duration: 1200, Count: 4},
					{Time: now.Add(-30 * time.Minute).Unix(), Duration: 600, Count: 2},
					{Time: now.Add(-20 * time.Minute).Unix(), Duration: 600, Count: 1},
					{Time: now.Add(-10 * time.Minute).Unix(), Duration: 600, Count: 3},
				},
			},
			DisruptionsAllowed: 2, // 允许中断的 Pod 数量
			CurrentHealthy:     5, // 当前健康的 Pod 数量
			DesiredHealthy:     3, // 期望保持的最小健康 Pod 数量
			ExpectedPods:       5, // 预期的总 Pod 数量
		},
	}
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

	// to delete
	for _, pod := range pods {
		workloadInfos, err := getWorkloadEvictionInfo(getEvictionRecords()[0])
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
	if evictionRecord == nil {
		general.Warningf("no eviction record")
		return nil, fmt.Errorf("no eviction record")
	}

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
	if evictionRecord == nil || evictionRecord.Buckets.List == nil {
		general.Warningf("no buckets in eviction info")
		return nil, 0
	}

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
