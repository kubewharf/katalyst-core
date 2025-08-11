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
	"math"
	"time"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	v1 "k8s.io/api/core/v1"
)

const (
	workloadEvictionInfoAnnotation = "evictionInfo"
	timeWindow1                    = 1800
	timeWindow2                    = 3600
	timeWindow3                    = 7200
	workloadName                   = "deployment"
)

// func PrepareCandidatePods(_ context.Context, request *pluginapi.GetTopEvictionPodsRequest) ([]*CandidatePod, error) {
// 	if request == nil {
// 		general.Warningf("no request in PrepareCandidatePods")
// 		return nil, fmt.Errorf("no request in PrepareCandidatePods")
// 	}

// 	if request.CandidateEvictionRecords == nil {
// 		general.Warningf("no candidateEvictionRecords in request")
// 	}

// 	var evictionRecords []*pluginapi.EvictionRecord
// 	for _, record := range request.CandidateEvictionRecords {
// 		evictionRecords = append(evictionRecords, record)
// 	}
// 	pods := request.ActivePods
// 	var candidates []*CandidatePod
// 	for _, pod := range pods {
// 		for _, record := range evictionRecords {
// 			if record.Uid == string(pod.UID) {
// 				if record.Buckets != nil {
// 					general.Infof("get eviction record for pod %s, record: %v", pod.Name, record)
// 				}
// 				workloadInfos, err := getWorkloadEvictionInfo(record)
// 				if err != nil {
// 					general.Warningf("get workload eviction info failed: %v", err)
// 				}
// 				candidates = append(candidates, &CandidatePod{
// 					Pod:                   pod,
// 					Scores:                make(map[string]int),
// 					TotalScore:            0,
// 					WorkloadsEvictionInfo: workloadInfos,
// 					UsageRatio:            0,
// 				})

// 			}
// 		}
// 	}

// 	return candidates, nil
// }

// PrepareCandidatePods converts a list of v1.Pod to a list of *CandidatePod and populates
// all the necessary information for the Filter and Score stages.
func PrepareCandidatePods(_ context.Context, request *pluginapi.GetTopEvictionPodsRequest) ([]*CandidatePod, error) {
	if request == nil {
		general.Warningf("no request in PrepareCandidatePods")
		return nil, fmt.Errorf("no request in PrepareCandidatePods")
	}

	if request.CandidateEvictionRecords == nil {
		general.Warningf("no candidateEvictionRecords in request")
	}

	recordsMap := make(map[string]*pluginapi.EvictionRecord)
	for _, record := range request.CandidateEvictionRecords {
		if record != nil {
			recordsMap[record.Uid] = record
		}
	}

	pods := request.ActivePods
	var candidates []*CandidatePod
	for _, pod := range pods {
		workloadInfos := make(map[string]*WorkloadEvictionInfo)
		var err error

		if record, ok := recordsMap[string(pod.UID)]; ok {
			if record.Buckets != nil {
				general.Infof("get eviction record for pod %s, record: %v", pod.Name, record)
			}
			workloadInfos, err = getWorkloadEvictionInfo(record)
			if err != nil {
				general.Warningf("get workload eviction info for pod %s failed: %v", pod.Name, err)
			}
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
func getWorkloadEvictionInfo(evictionRecord *pluginapi.EvictionRecord) (map[string]*WorkloadEvictionInfo, error) {
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
		Replicas:         evictionRecord.ExpectedPods,
		LastEvictionTime: lastEvictionTime,
		Limit:            evictionRecord.DisruptionsAllowed,
	}

	return workloadsEvictionInfo, nil
}

func calculateEvictionStatsByWindows(currentTime time.Time, evictionRecord *pluginapi.EvictionRecord, windows []int64) (map[float64]*EvictionStats, int64) {
	if evictionRecord == nil || evictionRecord.Buckets == nil || evictionRecord.Buckets.List == nil {
		general.Warningf("no buckets in eviction info")
		return nil, 0
	}

	buckets := evictionRecord.Buckets.List
	// currentHealthy := evictionRecord.CurrentHealthy
	expectedPods := evictionRecord.ExpectedPods

	statsByWindow := make(map[float64]*EvictionStats)
	if len(buckets) == 0 {
		general.Warningf("no buckets in eviction info")
		return statsByWindow, 0
	}

	currentTimestamp := currentTime.Unix()
	lastEvictionTime := buckets[0].Time
	for _, windowSec := range windows {
		if windowSec <= 0 {
			general.Warningf("invalid windowSec %d (must be positive), skipping", windowSec)
			continue
		}
		windowHour := float64(windowSec) / 3600

		windowStart := currentTimestamp - windowSec
		var totalCount int64
		for _, bucket := range buckets {
			if bucket.Time >= windowStart {
				totalCount += bucket.Count
			} else if bucket.Time < windowStart && windowStart <= bucket.Time+bucket.Duration {
				ratio := float64(bucket.Time+bucket.Duration-windowStart) / float64(bucket.Duration)
				partialCount := int64(math.Round(float64(bucket.Count) * ratio))
				totalCount += partialCount
			}
			if bucket.Time > lastEvictionTime {
				lastEvictionTime = bucket.Time
			}
		}
		evictionRatio := 0.0
		if expectedPods > 0 {
			evictionRatio = float64(totalCount) / float64(expectedPods)
		} else {
			general.Warningf("expectedPods is zero, cannot calculate eviction ratio")
		}
		statsByWindow[windowHour] = &EvictionStats{
			EvictionCount: totalCount,
			EvictionRatio: evictionRatio,
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
