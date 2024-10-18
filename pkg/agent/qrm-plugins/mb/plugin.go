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
	"time"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/allocator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type plugin struct {
	dieTopology        *machine.DieTopology
	incubationInterval time.Duration

	mbController *controller.Controller
}

func (c *plugin) Name() string {
	return "mb_plugin"
}

func (c *plugin) Start() error {
	general.InfofV(6, "mbm: plugin component starting ....")
	general.InfofV(6, "mbm: mb incubation interval %v", c.incubationInterval)
	general.InfofV(6, "mbm: numa-CCD-cpu topology: \n%s", c.dieTopology)

	// todo: NOT to return error (to crash explicitly); consider downgrade service
	if !c.dieTopology.FakeNUMAEnabled {
		return errors.New("mbm: not virtual numa; no need to dynamically manage the memory bandwidth")
	}

	domainManager := mbdomain.NewMBDomainManager(c.dieTopology, c.incubationInterval)

	var err error
	podMBMonitor, err := monitor.NewDefaultMBMonitor(c.dieTopology.DiesInNuma, c.dieTopology.CPUsInDie, domainManager)
	if err != nil {
		return errors.Wrap(err, "mbm: failed to create default mb monitor")
	}

	mbPlanAllocator, err := createMBPlanAllocator()
	if err != nil {
		return errors.Wrap(err, "mbm: failed to create mb plan allocator")
	}

	domainPolicy, err := policy.NewDefaultDomainMBPolicy(c.incubationInterval)
	if err != nil {
		return errors.Wrap(err, "mbm: failed to create domain manager")
	}

	c.mbController, err = controller.New(podMBMonitor, mbPlanAllocator, domainManager, domainPolicy)
	if err != nil {
		return errors.Wrap(err, "mbm: failed to create mb controller")
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

func (c *plugin) Stop() error {
	return c.mbController.Stop()
}

func NewComponent(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	mbController := &plugin{
		dieTopology:        agentCtx.DieTopology,
		incubationInterval: conf.IncubationInterval,
	}

	return true, &agent.PluginWrapper{GenericPlugin: mbController}, nil
}
