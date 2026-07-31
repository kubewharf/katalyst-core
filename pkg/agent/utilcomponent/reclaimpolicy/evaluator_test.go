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

package reclaimpolicy

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
)

type fakePodReclaimProfilingProvider struct {
	level       spd.PerformanceLevel
	levelErr    error
	baseline    bool
	baselineErr error

	levelCalls    int
	baselineCalls int
}

func (f *fakePodReclaimProfilingProvider) ServiceBusinessPerformanceLevel(_ context.Context, _ metav1.ObjectMeta) (spd.PerformanceLevel, error) {
	f.levelCalls++
	return f.level, f.levelErr
}

func (f *fakePodReclaimProfilingProvider) ServiceBaseline(_ context.Context, _ metav1.ObjectMeta) (bool, error) {
	f.baselineCalls++
	return f.baseline, f.baselineErr
}

func TestEvaluatePodReclaimPolicy(t *testing.T) {
	t.Parallel()

	podMeta := metav1.ObjectMeta{
		Namespace: "default",
		Name:      "test-pod",
	}
	spdNotFoundErr := errors.NewNotFound(schema.GroupResource{
		Group:    "workload.katalyst.kubewharf.io",
		Resource: "serviceprofiledescriptors",
	}, "test-spd")

	tests := []struct {
		name                  string
		nodeEnableReclaim     bool
		provider              *fakePodReclaimProfilingProvider
		want                  bool
		wantErr               bool
		wantLevelCalls        int
		wantBaselineCalls     int
		wantErrMessageContain string
	}{
		{
			name:              "node disabled returns false without querying spd",
			nodeEnableReclaim: false,
			want:              false,
		},
		{
			name:                  "nil provider returns error",
			nodeEnableReclaim:     true,
			wantErr:               true,
			wantErrMessageContain: "pod reclaim profiling provider is nil",
		},
		{
			name:              "spd not found from performance level defaults true",
			nodeEnableReclaim: true,
			provider: &fakePodReclaimProfilingProvider{
				levelErr: spdNotFoundErr,
			},
			want:           true,
			wantLevelCalls: 1,
		},
		{
			name:              "poor performance returns false",
			nodeEnableReclaim: true,
			provider: &fakePodReclaimProfilingProvider{
				level: spd.PerformanceLevelPoor,
			},
			want:           false,
			wantLevelCalls: 1,
		},
		{
			name:              "performance level error returns error",
			nodeEnableReclaim: true,
			provider: &fakePodReclaimProfilingProvider{
				levelErr: fmt.Errorf("get performance failed"),
			},
			wantErr:               true,
			wantLevelCalls:        1,
			wantErrMessageContain: "get performance failed",
		},
		{
			name:              "spd not found from baseline defaults true",
			nodeEnableReclaim: true,
			provider: &fakePodReclaimProfilingProvider{
				level:       spd.PerformanceLevelGood,
				baselineErr: spdNotFoundErr,
			},
			want:              true,
			wantLevelCalls:    1,
			wantBaselineCalls: 1,
		},
		{
			name:              "service baseline returns false",
			nodeEnableReclaim: true,
			provider: &fakePodReclaimProfilingProvider{
				level:    spd.PerformanceLevelGood,
				baseline: true,
			},
			want:              false,
			wantLevelCalls:    1,
			wantBaselineCalls: 1,
		},
		{
			name:              "baseline error returns error",
			nodeEnableReclaim: true,
			provider: &fakePodReclaimProfilingProvider{
				level:       spd.PerformanceLevelGood,
				baselineErr: fmt.Errorf("get baseline failed"),
			},
			wantErr:               true,
			wantLevelCalls:        1,
			wantBaselineCalls:     1,
			wantErrMessageContain: "get baseline failed",
		},
		{
			name:              "eligible pod returns true",
			nodeEnableReclaim: true,
			provider: &fakePodReclaimProfilingProvider{
				level:    spd.PerformanceLevelGood,
				baseline: false,
			},
			want:              true,
			wantLevelCalls:    1,
			wantBaselineCalls: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var provider PodReclaimProfilingProvider
			if tt.provider != nil {
				provider = tt.provider
			}
			got, err := EvaluatePodReclaimPolicy(context.Background(), provider, podMeta, tt.nodeEnableReclaim)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMessageContain)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
			if tt.provider != nil {
				assert.Equal(t, tt.wantLevelCalls, tt.provider.levelCalls)
				assert.Equal(t, tt.wantBaselineCalls, tt.provider.baselineCalls)
			}
		})
	}
}
