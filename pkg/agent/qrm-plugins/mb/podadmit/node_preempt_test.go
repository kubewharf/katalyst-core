package podadmit

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func TestNodePreempter_getNotInUseNodes(t *testing.T) {
	t.Parallel()

	testDomainManager := &mbdomain.MBDomainManager{
		Domains: map[int]*mbdomain.MBDomain{
			1: {
				ID:        1,
				NumaNodes: []int{0, 1, 2, 3},
				CCDNode:   nil,
				NodeCCDs: map[int][]int{
					0: {0, 1},
					1: {2, 3},
					2: {4, 5},
					3: {6, 7},
				},
				CCDs:          nil,
				PreemptyNodes: nil,
			},
		},
		CCDNode: map[int]int{
			0: 0,
			1: 0,
			2: 1,
			3: 1,
			4: 2,
			5: 2,
			6: 3,
			7: 3,
		},
	}

	type fields struct {
		domainManager *mbdomain.MBDomainManager
		mbController  *controller.Controller
	}
	type args struct {
		nodes []uint64
	}
	type wants struct {
		inUses    []int
		notInUses []int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   wants
	}{
		{
			name: "happy path of no current dedicated traffic",
			fields: fields{
				domainManager: testDomainManager,
				mbController: &controller.Controller{
					DomainManager: testDomainManager,
					CurrQoSCCDMB:  map[qosgroup.QoSGroup]*monitor.MBQoSGroup{},
				},
			},
			args: args{
				nodes: []uint64{1, 3},
			},
			want: wants{
				notInUses: []int{1, 3},
			},
		},
		{
			name: "happy path of current active zero dedicated traffic",
			fields: fields{
				domainManager: testDomainManager,
				mbController: &controller.Controller{
					DomainManager: testDomainManager,
					CurrQoSCCDMB: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
						qosgroup.QoSGroupDedicated: {
							CCDMB: map[int]*monitor.MBData{3: {
								TotalMB: 0,
							}},
						},
					},
				},
			},
			args: args{
				nodes: []uint64{1, 3},
			},
			want: wants{
				notInUses: []int{3},
				inUses:    []int{1},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := &NodePreempter{
				domainManager: tt.fields.domainManager,
				mbController:  tt.fields.mbController,
			}
			notInUses, inUses := n.splitDedicatedNodesToNotInAndInUses(tt.args.nodes)
			assert.Equalf(t, tt.want.notInUses, notInUses, "not in uses")
			assert.Equalf(t, tt.want.inUses, inUses, "not in uses")
		})
	}
}
