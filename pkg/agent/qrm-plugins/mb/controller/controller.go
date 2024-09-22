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

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/allocator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	intervalMBController = time.Second * 1
)

type Controller struct {
	cancel context.CancelFunc

	podMBMonitor    monitor.MBMonitor
	mbPlanAllocator allocator.PlanAllocator

	domainManager policy.MBDomainManager
	policy        policy.PackageMBPolicy
}

func (c *Controller) Run() {
	general.Infof("mbm: main control loop Run started")

	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	wait.UntilWithContext(ctx, c.run, intervalMBController)

	general.Infof("mbm: main control loop Run exited")
}

func (c *Controller) run(ctx context.Context) {
	qosCCDMB, err := c.podMBMonitor.GetQoSMBs()
	if err != nil {
		general.Errorf("mbm: failed to get MB usages: %v", err)
	}

	general.InfofV(6, "mbm: mb usage summary: %v", qosCCDMB)

	for i, domain := range c.domainManager.Domains {
		mbAlloc := c.policy.GetPlan(domain.CCDs, qosCCDMB)
		general.InfofV(6, "mbm: domain %d mb alloc plan: %v", i, mbAlloc)

		if err := c.mbPlanAllocator.Allocate(mbAlloc); err != nil {
			general.Errorf("mbm: failed to allocate mb plan for domain %d", i)
		}
	}
}

func (c *Controller) Stop() error {
	c.cancel()
	return nil
}

func New(podMBMonitor monitor.MBMonitor, mbPlanAllocator allocator.PlanAllocator) (*Controller, error) {
	return &Controller{
		podMBMonitor:    podMBMonitor,
		mbPlanAllocator: mbPlanAllocator,
	}, nil
}
