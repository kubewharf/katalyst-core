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

package controller

import (
	"context"
	"time"

	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/allocator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	resctrltask "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/task"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task/cgcpuset"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	intervalMBController = time.Second * 1
)

type Controller struct {
	cancel  context.CancelFunc
	chAdmit chan struct{}

	MBStat          *MBStatKeeper
	podMBMonitor    monitor.MBMonitor
	policy          policy.DomainMBPolicy
	mbPlanAllocator allocator.PlanAllocator

	TaskManager *resctrltask.TaskManager
	cgCPUSet    *cgcpuset.CPUSet

	// exposure it for testability
	DomainManager *mbdomain.MBDomainManager
}

func (c *Controller) GetDedicatedNodes() sets.Int {
	if tasks, err := c.TaskManager.GetQoSGroupedTask(qosgroup.QoSGroupDedicated); err == nil && len(tasks) > 0 {
		infoGetter := task.NewInfoGetter(c.cgCPUSet, tasks)
		dedicatedNodes := infoGetter.GetAssignedNumaNodes()
		general.InfofV(6, "mbm: identify dedicated pods numa nodes by cgroup mechanism: %v", dedicatedNodes)
		return dedicatedNodes
	}

	// fall back to educated guess by looking at the slots of active mb metrics
	return c.guessDedicatedNodesByCheckingActiveMBStat()
}

// guessDedicatedNodesByCheckingActiveMBStat identifies the nodes currently assigned to dedicated qos and having active traffic (including 0)
func (c *Controller) guessDedicatedNodesByCheckingActiveMBStat() sets.Int {
	dedicatedCCDMB := c.MBStat.getByQoSGroup(qosgroup.QoSGroupDedicated)
	if dedicatedCCDMB == nil {
		return nil
	}

	// identify the ccds having active traffic as dedicated groups
	dedicatedNodes := make(sets.Int)
	for ccd := range dedicatedCCDMB.CCDMB {
		node, err := c.DomainManager.GetNode(ccd)
		if err != nil {
			panic(err)
		}
		dedicatedNodes.Insert(node)
	}

	return dedicatedNodes
}

// ReqToAdjustMB requests controller to start a round of mb adjustment
func (c *Controller) ReqToAdjustMB() {
	select {
	case c.chAdmit <- struct{}{}:
	default:
	}
}

func (c *Controller) Run() {
	general.Infof("mbm: mb controller Run started")

	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	ticker := time.NewTicker(intervalMBController)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			general.Infof("mbm: mb controller Run loop had stopped by request")
			return
		case <-ticker.C:
			c.run(ctx)
		case <-c.chAdmit:
			general.InfofV(6, "mbm: mb controller to process pod admit request")
			c.process(ctx)
		}
	}
}

func (c *Controller) run(ctx context.Context) {
	qosCCDMB, err := c.podMBMonitor.GetMBQoSGroups()
	if err != nil {
		general.Errorf("mbm: failed to get MB usages: %v", err)
	}

	c.MBStat.update(qosCCDMB)
	general.InfofV(6, "mbm: controller: mb usage summary: %v", monitor.DisplayMBSummary(qosCCDMB))

	c.process(ctx)
}

func (c *Controller) process(ctx context.Context) {
	// policy does applicable customization based on current MB usages of all domains
	currQoSCCDMBStat := c.MBStat.get()
	c.policy.PreprocessQoSCCDMB(currQoSCCDMBStat)

	for i, domain := range c.DomainManager.Domains {
		// we only care about qosCCDMB manageable by the specific domain
		applicableQoSCCDMB := domain.GetApplicableQoSCCDMB(currQoSCCDMBStat)
		mbAlloc := c.policy.GetPlan(domain.MBQuota, domain, applicableQoSCCDMB)
		general.InfofV(6, "mbm: domain %d mb alloc plan: %v", i, mbAlloc)

		if err := c.mbPlanAllocator.Allocate(mbAlloc); err != nil {
			general.Errorf("mbm: failed to allocate mb plan for domain %d: %v", i, err)
		}
	}
}

func (c *Controller) Stop() error {
	if c.cancel == nil {
		return nil
	}

	c.cancel()
	return nil
}

func New(podMBMonitor monitor.MBMonitor, mbPlanAllocator allocator.PlanAllocator, domainManager *mbdomain.MBDomainManager, policy policy.DomainMBPolicy) (*Controller, error) {
	fs := afero.NewOsFs()
	return &Controller{
		podMBMonitor:    podMBMonitor,
		mbPlanAllocator: mbPlanAllocator,
		DomainManager:   domainManager,
		policy:          policy,
		chAdmit:         make(chan struct{}, 1),
		cgCPUSet:        cgcpuset.New(fs),
		TaskManager:     resctrltask.New(fs),
		MBStat:          NewMBStatKeeper(nil),
	}, nil
}
