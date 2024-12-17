package task

import (
	"fmt"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	resctrlconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/consts"
	"path"
)

type Task struct {
	QoSGroup qosgroup.QoSGroup

	// PodUID is the pod identifier that leverage host cgroup to locate the cpu/ccd/numa node info
	// including pod prefix and uid string, like "poda47c5c03-cf94-4a36-b52f-c1cb17dc1675"
	PodUID string
}

func (t Task) GetID() string {
	return t.PodUID
}

func (t Task) GetResctrlCtrlGroup() (string, error) {
	return path.Join(resctrlconsts.FsRoot, string(t.QoSGroup)), nil
}

func (t Task) GetResctrlMonGroup() (string, error) {
	taskCtrlGroup, err := t.GetResctrlCtrlGroup()
	if err != nil {
		return "", err
	}

	taskFolder := fmt.Sprintf(resctrlconsts.TmplTaskFolder, t.PodUID)
	return path.Join(taskCtrlGroup, resctrlconsts.SubGroupMonRoot, taskFolder), nil
}

func NewTask(group qosgroup.QoSGroup, podUID string) *Task {
	return &Task{
		QoSGroup: group,
		PodUID:   podUID,
	}
}
