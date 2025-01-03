package controller

import (
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

type MBStatKeeper struct {
	lock         sync.RWMutex
	currQoSCCDMB map[qosgroup.QoSGroup]*stat.MBQoSGroup
}

func (m *MBStatKeeper) update(qosCCDMB map[qosgroup.QoSGroup]*stat.MBQoSGroup) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.currQoSCCDMB = qosCCDMB
}

func (m *MBStatKeeper) get() map[qosgroup.QoSGroup]*stat.MBQoSGroup {
	m.lock.RLock()
	defer m.lock.RUnlock()
	return m.currQoSCCDMB
}

func (m *MBStatKeeper) getByQoSGroup(qos qosgroup.QoSGroup) *stat.MBQoSGroup {
	m.lock.RLock()
	defer m.lock.RUnlock()
	ccdMB, ok := m.currQoSCCDMB[qos]
	if !ok {
		return nil
	}
	return ccdMB
}

func NewMBStatKeeper(stat map[qosgroup.QoSGroup]*stat.MBQoSGroup) *MBStatKeeper {
	return &MBStatKeeper{
		currQoSCCDMB: stat,
	}
}
