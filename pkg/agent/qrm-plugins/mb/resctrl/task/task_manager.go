package task

import (
	"github.com/pkg/errors"
	"github.com/spf13/afero"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/file"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

type TaskManager struct {
	fs afero.Fs
}

func New(fs afero.Fs) *TaskManager {
	return &TaskManager{
		fs: fs,
	}
}

func (t *TaskManager) GetQoSGroupedTask(qos qosgroup.QoSGroup) ([]*task.Task, error) {
	monGroupPaths, err := file.GetResctrlSubMonGroups(t.fs, string(qos))
	if err != nil {
		return nil, errors.Wrap(err, "failed to get sub mon group paths")
	}

	result := make([]*task.Task, 0)
	for _, path := range monGroupPaths {
		_, podUID, err := file.ParseMonGroup(path)
		if err != nil {
			return nil, errors.Wrap(err, "failed to parse mon group path")
		}
		result = append(result, task.NewTask(qos, podUID))
	}
	return result, nil
}
