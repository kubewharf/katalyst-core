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
	"testing"
	"time"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/stretchr/testify/assert"
)

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
			assert.Equal(t, tc.inputRecord.CurrentHealthy, info.Replicas)
			assert.Equal(t, tc.inputRecord.DisruptionsAllowed, info.Limit)

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
