package policy

import (
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func Test_constraintDomainMBPolicy_getQosMBGroups(t *testing.T) {
	t.Parallel()

	testQoSMBGroups := map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
		"shared-50": {
			CCDMB: map[int]*monitor.MBData{
				1:  {TotalMB: 16_000},
				4:  {TotalMB: 12_000},
				12: {TotalMB: 21_000},
			},
		},
		"dedicated": {
			CCDMB: map[int]*monitor.MBData{
				0:  {TotalMB: 10_000},
				3:  {TotalMB: 9_000},
				15: {TotalMB: 22_000},
			},
		},
	}

	type fields struct {
		qos map[qosgroup.QoSGroup]*monitor.MBQoSGroup
	}
	tests := []struct {
		name   string
		fields fields
		want   map[qosgroup.QoSGroup]*monitor.MBQoSGroup
	}{
		{
			name: "happy path",
			fields: fields{
				qos: testQoSMBGroups,
			},
			want: testQoSMBGroups,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &constraintDomainMBPolicy{
				qos: tt.fields.qos,
			}
			got := c.getQosMBGroups()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getQosMBGroups() = %v, want %v", got, tt.want)
			}
		})
	}
}
