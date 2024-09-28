/*
Copyright 2022 The Katalyst Authors.

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

package task

import (
	"fmt"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/state"
)

type TaskMBReader interface {
	GetMB(task *Task) (map[int]int, error)
}

type taskMBReader struct {
	monGroupReader resctrl.MonGroupReader
}

func (t taskMBReader) GetMB(task *Task) (map[int]int, error) {
	taskMonGroup, err := task.GetResctrlMonGroup()
	if err != nil {
		return nil, errors.Wrap(err, fmt.Sprintf("failed to locate resctrl mon group for qos level %s task %s", task.QoSGroup, task.PodUID))
	}

	return t.monGroupReader.ReadMB(taskMonGroup, task.CCDs)
}

func newTaskMBReader(monGroupReader resctrl.MonGroupReader) (TaskMBReader, error) {
	return &taskMBReader{
		monGroupReader: monGroupReader,
	}, nil
}

func CreateTaskMBReader(dataKeeper state.MBRawDataKeeper) (TaskMBReader, error) {
	ccdMBCalc, err := resctrl.NewCCDMBCalculator(dataKeeper)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create ccd mb calculator")
	}

	ccdMBReader, err := resctrl.NewCCDMBReader(ccdMBCalc)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create resctrl ccd mb reader")
	}

	monGroupReader, err := resctrl.NewMonGroupReader(ccdMBReader)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create resctrl mon group mb reader")
	}

	return newTaskMBReader(monGroupReader)
}
