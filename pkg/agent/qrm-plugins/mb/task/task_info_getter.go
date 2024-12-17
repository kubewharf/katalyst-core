package task

import (
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task/cgcpuset"
)

type tasksInfoLocator struct {
	cgCPUSet *cgcpuset.CPUSet
	tasks    []*Task
}

func (t *tasksInfoLocator) GetAssignedNumaNodes() sets.Int {
	results := make(sets.Int)

	for _, task := range t.tasks {
		nodes, err := t.cgCPUSet.GetNumaNodes(task.PodUID, task.QoSGroup)
		if err != nil {
			panic(err)
		}
		for _, node := range nodes {
			results.Insert(node)
		}
	}

	return results
}

func NewInfoGetter(cgCPUSet *cgcpuset.CPUSet, tasks []*Task) qosgroup.QoSGroupInfoGetter {
	return &tasksInfoLocator{
		cgCPUSet: cgCPUSet,
		tasks:    tasks,
	}
}
