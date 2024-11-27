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

package readmb

import (
	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

type ReadMBReader interface {
	GetMB(qosGroup string) (map[int]int, error)
}

type QoSGroupMBReader struct {
	ccds           []int
	monGroupReader resctrl.MonGroupReader
}

func (q *QoSGroupMBReader) GetMB(qosGroup string) (map[int]int, error) {
	ctrlGroupPath, err := task.GetResctrlCtrlGroupFolder(task.QoSGroup(qosGroup))
	if err != nil {
		return nil, err
	}

	return q.monGroupReader.ReadMB(ctrlGroupPath, q.ccds)
}

func newQoSGroupMBReader(ccds []int, monGroupReader resctrl.MonGroupReader) (ReadMBReader, error) {
	return &QoSGroupMBReader{
		monGroupReader: monGroupReader,
		ccds:           ccds,
	}, nil
}

func CreateQoSGroupMBReader(ccds []int, dataKeeper state.MBRawDataKeeper) (ReadMBReader, error) {
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

	return newQoSGroupMBReader(ccds, monGroupReader)
}
