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

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/allocator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/config"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	intervalMBController = time.Second * 1
)

type Controller struct {
	cancel context.CancelFunc

	podMBMonitor    monitor.MBMonitor
	mbPlanAllocator allocator.PlanAllocator

	domainManager *mbdomain.MBDomainManager
	policy        policy.DomainMBPolicy

	chAdmit      chan struct{}
	currQoSCCDMB map[task.QoSGroup]*monitor.MBQoSGroup
}

// ReqToAdjustMB requests controller to start a round of mb adjustment
func (c *Controller) ReqToAdjustMB() {
	select {
	case c.chAdmit <- struct{}{}:
	default:
	}
}

func (c *Controller) Run() {
	general.Infof("mbm: main control loop Run started")

	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	ticker := time.NewTicker(intervalMBController)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			general.Infof("mbm: main control loop Run exited")
			return
		case <-ticker.C:
			c.run(ctx)
		case <-c.chAdmit:
			general.InfofV(6, "mbm: process admit request")
			c.process(ctx)
		}
	}
}

func (c *Controller) run(ctx context.Context) {
	qosCCDMB, err := c.podMBMonitor.GetMBQoSGroups()
	if err != nil {
		general.Errorf("mbm: failed to get MB usages: %v", err)
	}
	c.currQoSCCDMB = qosCCDMB

	general.InfofV(6, "mbm: mb usage summary: %v", monitor.DisplayMBSummary(qosCCDMB))
	c.process(ctx)
}

func (c *Controller) process(ctx context.Context) {
	for i, domain := range c.domainManager.Domains {
		// we only care about qosCCDMB manageable by the domain
		applicableQoSCCDMB := getApplicableQoSCCDMB(domain, c.currQoSCCDMB)
		general.InfofV(6, "mbm: domain %d mb stat: %#v", i, applicableQoSCCDMB)

		mbAlloc := c.policy.GetPlan(config.DomainTotalMB, domain, applicableQoSCCDMB)
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
	return &Controller{
		podMBMonitor:    podMBMonitor,
		mbPlanAllocator: mbPlanAllocator,
		domainManager:   domainManager,
		policy:          policy,
		chAdmit:         make(chan struct{}, 1),
	}, nil
}

func getApplicableQoSCCDMB(domain *mbdomain.MBDomain, qosccdmb map[task.QoSGroup]*monitor.MBQoSGroup) map[task.QoSGroup]*monitor.MBQoSGroup {
	result := make(map[task.QoSGroup]*monitor.MBQoSGroup)

	for qos, mbQosGroup := range qosccdmb {
		for ccd, _ := range mbQosGroup.CCDs {
			if _, ok := mbQosGroup.CCDMB[ccd]; !ok {
				// no ccd-mb stat; skip it
				continue
			}
			if _, ok := domain.CCDNode[ccd]; ok {
				if _, ok := result[qos]; !ok {
					result[qos] = &monitor.MBQoSGroup{
						CCDs:  make(sets.Int),
						CCDMB: make(map[int]*monitor.MBData),
					}
				}
				result[qos].CCDs.Insert(ccd)
				result[qos].CCDMB[ccd] = qosccdmb[qos].CCDMB[ccd]
			}
		}
	}

	return result
}
