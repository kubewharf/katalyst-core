package policy

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

type DomainMBPolicy interface {
	// GetPlan returns mb allocation plan for a specific domain
	GetPlan(totalMB int, domain *mbdomain.MBDomain, currQoSMB map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc

	// PreprocessQoSCCDMB lets policy have a sense of global view; essential for domains having cross-domain impacts
	PreprocessQoSCCDMB(qos map[qosgroup.QoSGroup]*stat.MBQoSGroup)
}

type noopDomainMBPolicy struct{}

func (n noopDomainMBPolicy) GetPlan(totalMB int, domain *mbdomain.MBDomain, currQoSMB map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc {
	return nil
}

func (n noopDomainMBPolicy) PreprocessQoSCCDMB(qos map[qosgroup.QoSGroup]*stat.MBQoSGroup) {
}

func NewNoopDomainMBPolicy() DomainMBPolicy {
	return &noopDomainMBPolicy{}
}
