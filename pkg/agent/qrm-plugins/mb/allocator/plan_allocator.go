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

package allocator

import (
	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

type PlanAllocator interface {
	Allocate(alloc *plan.MBAlloc) error
}

type planAllocator struct {
	ctrlGroupMBSetter resctrl.CtrlGroupMBSetter
}

func (p planAllocator) Allocate(alloc *plan.MBAlloc) error {
	for qosLevel, ccdMB := range alloc.Plan {
		qosCtrlGroup, err := task.GetResctrlCtrlGroupFolder(qosLevel)
		if err != nil {
			return errors.Wrap(err, "unknown qos level")
		}
		if err := p.ctrlGroupMBSetter.SetMB(qosCtrlGroup, ccdMB); err != nil {
			return errors.Wrap(err, "failed to set MB allocation")
		}
	}
	return nil
}

func NewPlanAllocator(ctrlGroupMBSetter resctrl.CtrlGroupMBSetter) (PlanAllocator, error) {
	return &planAllocator{
		ctrlGroupMBSetter: ctrlGroupMBSetter,
	}, nil
}
