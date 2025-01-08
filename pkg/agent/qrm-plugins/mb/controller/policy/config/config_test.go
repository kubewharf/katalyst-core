package config

import (
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"testing"
	"time"
)

func TestMBPolicyConfig_String(t *testing.T) {
	t.Parallel()
	type fields struct {
		MBQRMPluginConfig qrm.MBQRMPluginConfig
		CCDMBMax          int
		DomainMBMax       int
		DomainMBMin       int
		ZombieCCDMB       int
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "happy path",
			fields: fields{
				MBQRMPluginConfig: qrm.MBQRMPluginConfig{
					CPUSetPoolToSharedSubgroup: map[string]int{
						"batch": 30,
						"flink": 50,
					},
					MinMBPerCCD:          4_000,
					DomainMBCapacity:     122_000,
					MBRemoteLimit:        20_000,
					IncubationInterval:   time.Minute * 1,
					LeafThrottleType:     "half-throttle",
					LeafEaseType:         "quarter-ease",
					MBPressureThreshold:  6_000,
					MBEaseThreshold:      9_000,
					CCDMBDistributorType: "logarithm-distributor",
					SourcerType:          "crbs",
				},
				CCDMBMax:    35_000,
				DomainMBMax: 122_000,
				DomainMBMin: 4_000,
				ZombieCCDMB: 100,
			},
			want: "mb policy config {CCDMBMax:35000,DomainMBMax:122000,DomainMBMin:4000,ZombieCCDMB:100,MinMBPerCCD:4000,MBRemoteLimit:20000,MBPressureThreshold:6000,MBEaseThreshold:9000,DomainMBCapacity:122000,IncubationInterval:1m0s,LeafThrottleType:half-throttle,LeafEaseType:quarter-ease,CCDMBDistributorType:logarithm-distributor,SourcerType:crbs,CPUSetPoolToSharedSubgroup:map[batch:30 flink:50]}",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := MBPolicyConfig{
				MBQRMPluginConfig: tt.fields.MBQRMPluginConfig,
				CCDMBMax:          tt.fields.CCDMBMax,
				DomainMBMax:       tt.fields.DomainMBMax,
				DomainMBMin:       tt.fields.DomainMBMin,
				ZombieCCDMB:       tt.fields.ZombieCCDMB,
			}
			if got := m.String(); got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}
