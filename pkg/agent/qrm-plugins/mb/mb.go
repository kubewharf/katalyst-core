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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/podadmit"
	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/allocator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

const defaultSharedSubgroup = 50

// todo: replace global vars with a better mechanism to facilitate dynamic memory policy
// below global var is used by dynamic memory policy to
// 1. preempting numa nodes on pod admission;
// 2. advising shared_xx for pod in request
var PodAdmitter *podadmit.PodAdmitter

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
func NewComponent(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	plugin := &plugin{
		qosConfig:          conf.QoSConfiguration,
		dieTopology:        agentCtx.DieTopology,
		incubationInterval: conf.IncubationInterval,
	}

	domainManager := mbdomain.NewMBDomainManager(plugin.dieTopology, plugin.incubationInterval)

	var err error

	dataKeeper, err := state.NewMBRawDataKeeper()
	if err != nil {
		return false, nil, errors.Wrap(err, "failed to create raw data state keeper")
	}

	taskManager, err := task.NewManager(plugin.dieTopology.DiesInNuma, plugin.dieTopology.CPUsInDie, dataKeeper, domainManager)
	if err != nil {
		return false, nil, errors.Wrap(err, "failed to create task manager")
	}

	podMBMonitor, err := monitor.NewDefaultMBMonitor(plugin.dieTopology.CPUsInDie, dataKeeper, taskManager, domainManager)
	if err != nil {
		return false, nil, errors.Wrap(err, "mbm: failed to create default mb monitor")
	}

	mbPlanAllocator, err := createMBPlanAllocator()
	if err != nil {
		return false, nil, errors.Wrap(err, "mbm: failed to create mb plan allocator")
	}

	domainPolicy, err := policy.NewDefaultDomainMBPolicy()
	if err != nil {
		return false, nil, errors.Wrap(err, "mbm: failed to create domain manager")
	}

	plugin.mbController, err = controller.New(podMBMonitor, mbPlanAllocator, domainManager, domainPolicy)
	if err != nil {
		return false, nil, errors.Wrap(err, "mbm: failed to create mb controller")
	}

	defaultSubgroup, ok := conf.CPUSetPoolToSharedSubgroup["share"]
	if !ok {
		defaultSubgroup = defaultSharedSubgroup
	}
	podSubgrouper := podadmit.NewPodGrouper(conf.CPUSetPoolToSharedSubgroup, defaultSubgroup)
	nodePreempter := podadmit.NewNodePreempter(domainManager, plugin.mbController, taskManager)
	PodAdmitter = podadmit.NewPodAdmitter(nodePreempter, podSubgrouper)

	return true, &agent.PluginWrapper{GenericPlugin: plugin}, nil
}
