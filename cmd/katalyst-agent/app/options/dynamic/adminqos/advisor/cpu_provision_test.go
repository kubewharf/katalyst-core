package advisor

import (
	"testing"

	"github.com/stretchr/testify/require"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadapi "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
)

func TestCPUProvisionOptions_parseRegionIndicatorTargetOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      map[string]string
		want    map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration
		wantErr bool
	}{
		{
			name: "empty",
			in:   map[string]string{},
			want: map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration{},
		},
		{
			name: "single_region_multi_indicators",
			in: map[string]string{
				string(configapi.QoSRegionTypeShare): "cpu_sched_wait=460/cpu_usage_ratio=0.8",
			},
			want: map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration{
				configapi.QoSRegionTypeShare: {
					{Name: workloadapi.ServiceSystemIndicatorNameCPUSchedWait, Target: 460},
					{Name: workloadapi.ServiceSystemIndicatorNameCPUUsageRatio, Target: 0.8},
				},
			},
		},
		{
			name: "multi_region",
			in: map[string]string{
				string(configapi.QoSRegionTypeShare):     "cpu_sched_wait=460",
				string(configapi.QoSRegionTypeDedicated): "cpi=1.4/cpu_usage_ratio=0.55",
				"dummy":                                  "cpu_usage_ratio=0.9",
			},
			want: map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration{
				configapi.QoSRegionTypeShare: {
					{Name: workloadapi.ServiceSystemIndicatorNameCPUSchedWait, Target: 460},
				},
				configapi.QoSRegionTypeDedicated: {
					{Name: workloadapi.ServiceSystemIndicatorNameCPI, Target: 1.4},
					{Name: workloadapi.ServiceSystemIndicatorNameCPUUsageRatio, Target: 0.55},
				},
				"dummy": {
					{Name: workloadapi.ServiceSystemIndicatorNameCPUUsageRatio, Target: 0.9},
				},
			},
		},
		{
			name: "invalid_item_format_no_equal",
			in: map[string]string{
				string(configapi.QoSRegionTypeShare): "cpu_sched_wait460",
			},
			want: map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration{
				configapi.QoSRegionTypeShare: {},
			},
			wantErr: true,
		},
		{
			name: "invalid_item_format_too_many_equals",
			in: map[string]string{
				string(configapi.QoSRegionTypeShare): "cpu_sched_wait=460=1",
			},
			want: map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration{
				configapi.QoSRegionTypeShare: {},
			},
			wantErr: true,
		},
		{
			name: "partial_invalid_items",
			in: map[string]string{
				string(configapi.QoSRegionTypeShare): "cpu_sched_wait=460/bad/cpu_usage_ratio=0.8",
			},
			want: map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration{
				configapi.QoSRegionTypeShare: {
					{Name: workloadapi.ServiceSystemIndicatorNameCPUSchedWait, Target: 460},
					{Name: workloadapi.ServiceSystemIndicatorNameCPUUsageRatio, Target: 0.8},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid_float",
			in: map[string]string{
				string(configapi.QoSRegionTypeShare): "cpu_sched_wait=abc",
			},
			want: map[configapi.QoSRegionType][]configapi.IndicatorTargetConfiguration{
				configapi.QoSRegionTypeShare: {},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			o := NewCPUProvisionOptions()
			got, err := o.parseRegionIndicatorTargetOptions(tc.in)

			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			// Compare per-region to avoid depending on map iteration order.
			require.Equal(t, len(tc.want), len(got))
			for regionType, wantTargets := range tc.want {
				gotTargets, ok := got[regionType]
				require.True(t, ok, "missing region %q", regionType)
				require.Equal(t, wantTargets, gotTargets)
			}
		})
	}
}
