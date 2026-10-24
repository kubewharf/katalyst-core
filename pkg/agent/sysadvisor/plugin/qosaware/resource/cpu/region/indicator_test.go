/*
Copyright 2026 The Katalyst Authors.

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

package region

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func TestQoSRegionBaseGetAverageCoreMetric(t *testing.T) {
	t.Parallel()

	now := time.Now()
	expired := now.Add(-2 * time.Minute)
	tests := []struct {
		name      string
		cpus      machine.CPUSet
		samples   map[int]utilmetric.MetricData
		want      float64
		wantError bool
	}{
		{
			name: "averages valid samples",
			cpus: machine.NewCPUSet(0, 1),
			samples: map[int]utilmetric.MetricData{
				0: {Value: 100, Time: &now},
				1: {Value: 300, Time: &now},
			},
			want: 200,
		},
		{
			name: "keeps a measured zero",
			cpus: machine.NewCPUSet(0),
			samples: map[int]utilmetric.MetricData{
				0: {Value: 0, Time: &now},
			},
			want: 0,
		},
		{
			name: "uses valid subset",
			cpus: machine.NewCPUSet(0, 1, 2),
			samples: map[int]utilmetric.MetricData{
				0: {Value: 100, Time: &now},
				2: {Value: 300, Time: &now},
			},
			want: 200,
		},
		{
			name: "ignores non-finite samples when a valid sample exists",
			cpus: machine.NewCPUSet(0, 1, 2, 3),
			samples: map[int]utilmetric.MetricData{
				0: {Value: 100, Time: &now},
				1: {Value: math.NaN(), Time: &now},
				2: {Value: math.Inf(1), Time: &now},
				3: {Value: math.Inf(-1), Time: &now},
			},
			want: 100,
		},
		{
			name: "ignores expired sample",
			cpus: machine.NewCPUSet(0, 1),
			samples: map[int]utilmetric.MetricData{
				0: {Value: 100, Time: &now},
				1: {Value: 900, Time: &expired},
			},
			want: 100,
		},
		{
			name:      "rejects empty set",
			cpus:      machine.NewCPUSet(),
			wantError: true,
		},
		{
			name:      "rejects no samples",
			cpus:      machine.NewCPUSet(0, 1),
			wantError: true,
		},
		{
			name: "rejects no valid samples",
			cpus: machine.NewCPUSet(0, 1),
			samples: map[int]utilmetric.MetricData{
				0: {Value: math.NaN(), Time: &now},
				1: {Value: 100, Time: &expired},
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDedicatedIndicatorFixture(t, 0, types.TopologyAwareAssignment{
				0: tt.cpus,
			})
			for cpu, sample := range tt.samples {
				f.fetcher.SetCPUMetric(cpu, consts.MetricCPUSchedwait, sample)
			}

			got, err := f.region.getAverageCoreMetric(tt.cpus, consts.MetricCPUSchedwait)
			if tt.wantError {
				require.ErrorIs(t, err, errIndicatorUnavailable)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestIndicatorValueValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		indicator workloadapis.ServiceSystemIndicatorName
		current   float64
		target    float64
		wantValid bool
	}{
		{"sched wait zero", workloadapis.ServiceSystemIndicatorNameCPUSchedWait, 0, 460, true},
		{"sched wait negative", workloadapis.ServiceSystemIndicatorNameCPUSchedWait, -1, 460, false},
		{"sched wait nan", workloadapis.ServiceSystemIndicatorNameCPUSchedWait, math.NaN(), 460, false},
		{"sched wait infinity", workloadapis.ServiceSystemIndicatorNameCPUSchedWait, math.Inf(1), 460, false},
		{"usage zero preserves old behavior", workloadapis.ServiceSystemIndicatorNameCPUUsageRatio, 0, 0.55, false},
		{"usage positive", workloadapis.ServiceSystemIndicatorNameCPUUsageRatio, 0.5, 0.55, true},
		{"target must be positive", workloadapis.ServiceSystemIndicatorNameCPUUsageRatio, 0.5, 0, false},
		{"target nan", workloadapis.ServiceSystemIndicatorNameCPUSchedWait, 100, math.NaN(), false},
		{"target infinity", workloadapis.ServiceSystemIndicatorNameCPUSchedWait, 100, math.Inf(1), false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isIndicatorValueValid(configapi.QoSRegionTypeDedicated, tt.indicator, types.IndicatorValue{
				Current: tt.current,
				Target:  tt.target,
			})
			require.Equal(t, tt.wantValid, got)
		})
	}
}
