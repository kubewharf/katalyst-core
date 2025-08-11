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
	"testing"
	"time"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPrepareCandidatePods(t *testing.T) {
	now := time.Now()
	pods := []*v1.Pod{
		{ObjectMeta: metav1.ObjectMeta{UID: "pod-uid-1", Name: "pod-1"}},
		{ObjectMeta: metav1.ObjectMeta{UID: "pod-uid-2", Name: "pod-2"}},
		{ObjectMeta: metav1.ObjectMeta{UID: "pod-uid-3", Name: "pod-3"}},
	}

	evictionRecords := []*pluginapi.EvictionRecord{
		{
			Uid:          "pod-uid-1",
			HasPdb:       true,
			ExpectedPods: 5,
			Buckets: &pluginapi.Buckets{List: []*pluginapi.Bucket{
				{Time: now.Add(-10 * time.Minute).Unix(), Duration: 600, Count: 1},
			}},
		},
		{
			Uid: "pod-uid-2",
		},
	}

	testCases := []struct {
		name          string
		request       *pluginapi.GetTopEvictionPodsRequest
		expectedErr   bool
		expectedCount int
	}{
		{
			name: "normal case",
			request: &pluginapi.GetTopEvictionPodsRequest{
				ActivePods:               pods,
				CandidateEvictionRecords: evictionRecords,
			},
			expectedErr:   false,
			expectedCount: 3,
		},
		{
			name: "no eviction records",
			request: &pluginapi.GetTopEvictionPodsRequest{
				ActivePods: pods,
			},
			expectedErr:   false,
			expectedCount: 3,
		},
		{
			name: "no active pods",
			request: &pluginapi.GetTopEvictionPodsRequest{
				CandidateEvictionRecords: evictionRecords,
			},
			expectedErr:   false,
			expectedCount: 0,
		},
		{
			name:          "nil request",
			request:       nil,
			expectedErr:   true,
			expectedCount: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			candidates, err := PrepareCandidatePods(context.Background(), tc.request)
			if tc.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, candidates, tc.expectedCount)

				if tc.expectedCount > 0 {
					// Check pod-1 which has full eviction details
					c1 := candidates[0]
					assert.Equal(t, "pod-uid-1", string(c1.Pod.UID))
					assert.NotNil(t, c1.WorkloadsEvictionInfo)

					if info1, ok := c1.WorkloadsEvictionInfo[workloadName]; ok {
						assert.Equal(t, int32(5), info1.Replicas)
						assert.NotZero(t, info1.LastEvictionTime)
					} else {
						t.Logf("pod-1 has no workload info for %s, skipping details check", workloadName)
					}

					// Check pod-2 which has an empty record
					c2 := candidates[1]
					assert.Equal(t, "pod-uid-2", string(c2.Pod.UID))
					assert.NotNil(t, c2.WorkloadsEvictionInfo)

					if info2, ok := c2.WorkloadsEvictionInfo[workloadName]; ok {
						assert.Zero(t, info2.LastEvictionTime)
					} else {
						t.Logf("pod-2 has no workload info for %s, skipping details check", workloadName)
					}
				}
			}
		})
	}
}

func TestGetWorkloadEvictionInfo(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name           string
		inputRecord    *pluginapi.EvictionRecord
		expectedErr    bool
		expectedWindow int64
		expectedCount  int64
	}{{
		name: "normal_case_with_multiple_buckets",
		inputRecord: &pluginapi.EvictionRecord{
			Uid:                "test-pod-1",
			HasPdb:             true,
			DisruptionsAllowed: 2,
			CurrentHealthy:     5,
			Buckets: &pluginapi.Buckets{List: []*pluginapi.Bucket{{
				Time:     now.Add(-30 * time.Minute).Unix(),
				Duration: 1800,
				Count:    3,
			}, {
				Time:     now.Add(-60 * time.Minute).Unix(),
				Duration: 1800,
				Count:    5,
			}}},
		},
		expectedErr:    false,
		expectedWindow: 1800,
		expectedCount:  3,
	}, {
		name: "empty_buckets_list",
		inputRecord: &pluginapi.EvictionRecord{
			Uid:                "test-pod-2",
			Buckets:            &pluginapi.Buckets{List: []*pluginapi.Bucket{}},
			CurrentHealthy:     5,
			DisruptionsAllowed: 1,
		},
		expectedErr:    false,
		expectedWindow: 1800,
		expectedCount:  0,
	}, {
		name:        "nil_eviction_record",
		inputRecord: nil,
		expectedErr: true,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := getWorkloadEvictionInfo(tc.inputRecord)

			if tc.expectedErr {
				assert.Error(t, err)
				assert.Nil(t, result)
				return
			}

			assert.NoError(t, err)
			assert.NotNil(t, result)
			assert.Contains(t, result, workloadName)

			info := result[workloadName]
			stats, ok := info.StatsByWindow[float64(tc.expectedWindow)/3600]
			if tc.expectedCount > 0 {
				assert.True(t, ok)
				assert.Equal(t, tc.expectedCount, stats.EvictionCount)
			} else {
				assert.False(t, ok)
			}
		})
	}
}

func TestCalculateEvictionStatsByWindows(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name           string
		currentTime    time.Time
		inputRecord    *pluginapi.EvictionRecord
		windows        []int64
		expectedStats  map[float64]*EvictionStats
		expectedLastEv int64
	}{{
		name:        "bucket_completely_outside_window",
		currentTime: now,
		inputRecord: &pluginapi.EvictionRecord{
			Buckets: &pluginapi.Buckets{
				List: []*pluginapi.Bucket{{
					Time:     now.Add(-120 * time.Minute).Unix(), // 7200s ago
					Duration: 1800,
					Count:    5,
				}},
			},
		},
		windows: []int64{3600}, // 60min window
		expectedStats: map[float64]*EvictionStats{
			1.0: {EvictionCount: 0, EvictionRatio: 0},
		},
		expectedLastEv: now.Add(-120 * time.Minute).Unix(),
	}, {
		name:        "empty_buckets_list",
		currentTime: now,
		inputRecord: &pluginapi.EvictionRecord{
			Buckets: &pluginapi.Buckets{List: []*pluginapi.Bucket{}},
		},
		windows: []int64{1800, 3600},
		expectedStats: map[float64]*EvictionStats{
			0.5: nil,
			1.0: nil,
		},
		expectedLastEv: 0,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stats, lastEv := calculateEvictionStatsByWindows(
				tc.currentTime,
				tc.inputRecord,
				tc.windows,
			)

			assert.Equal(t, tc.expectedLastEv, lastEv)

			for window, expected := range tc.expectedStats {
				actual, exists := stats[window]
				if expected == nil {
					assert.False(t, exists, "window %v should not exist", window)
					continue
				}

				assert.True(t, exists, "window %v not found", window)
				assert.Equal(t, expected.EvictionCount, actual.EvictionCount)
				assert.InDelta(t, expected.EvictionRatio, actual.EvictionRatio, 0.001)
			}
		})
	}
}
