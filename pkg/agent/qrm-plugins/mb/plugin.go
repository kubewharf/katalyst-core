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

package mb

import (
	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/allocator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type plugin struct {
	dieTopology *machine.DieTopology

	mbController *controller.Controller
}

func (c *plugin) Name() string {
	return "mb_plugin"
}

func (c *plugin) Start() error {
	general.InfofV(6, "mbm: plugin component starting ....")
	general.InfofV(6, "mbm: numa-CCD-cpu topology: \n%s", c.dieTopology)

	var err error

	dataKeeper, err := state.NewMBRawDataKeeper()
	if err != nil {
		return errors.Wrap(err, "mbm: failed to create raw data state keeper")
	}

	taskManager, err := task.New(c.dieTopology.DiesInNuma, dataKeeper)
	if err != nil {
		return errors.Wrap(err, "mbm: failed to create task manager")
	}

	taskMBReader, err := createTaskMBReader(dataKeeper)
	if err != nil {
		return errors.Wrap(err, "mbm: failed to create task mb reader")
	}

	podMBMonitor, err := monitor.New(taskManager, taskMBReader)
	if err != nil {
		return err
	}

	mbPlanAllocator, err := createMBPlanAllocator()
	if err != nil {
		return err
	}

	c.mbController, err = controller.New(podMBMonitor, mbPlanAllocator)
	if err != nil {
		return err
	}

	go func() {
		defer func() {
			err := recover()
			if err != nil {
				general.Errorf("mbm: background run exited, due to error: %v", err)
			}
		}()

		c.mbController.Run()
	}()

	return nil
}

func createMBPlanAllocator() (allocator.PlanAllocator, error) {
	schemataUpdater, err := resctrl.NewSchemataUpdater()
	if err != nil {
		return nil, err
	}

	ctrlGroupMBSetter, err := resctrl.NewCtrlGroupSetter(schemataUpdater)
	if err != nil {
		return nil, err
	}

	return allocator.NewPlanAllocator(ctrlGroupMBSetter)
}

func createTaskMBReader(dataKeeper state.MBRawDataKeeper) (task.TaskMBReader, error) {
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

	return task.NewTaskMBReader(monGroupReader)
}

func (c *plugin) Stop() error {
	return c.mbController.Stop()
}

func NewComponent(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	mbController := &plugin{
		dieTopology: agentCtx.DieTopology,
	}

	return true, &agent.PluginWrapper{GenericPlugin: mbController}, nil
}
