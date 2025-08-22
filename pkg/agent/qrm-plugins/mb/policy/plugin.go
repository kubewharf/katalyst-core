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

package policy

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/allocator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/domain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/reader"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	name       = "qrm_mb_plugin_generic_policy"
	metricName = name
	interval   = time.Second * 1
)

type MBPlugin struct {
	chStop  chan struct{}
	emitter metrics.MetricEmitter

	ccdToDomain map[int]int
	xDomGroups  sets.String
	domains     domain.Domains

	reader        reader.MBReader
	advisor       advisor.Advisor
	planAllocator allocator.PlanAllocator
}

func (m *MBPlugin) Name() string {
	return name
}

func (m *MBPlugin) Start() error {
	general.Infof("mbm: plugin started")
	m.chStop = make(chan struct{})
	go wait.Until(m.run, interval, m.chStop)
	return nil
}

func (m *MBPlugin) Stop() error {
	general.Infof("mbm: plugin stopped")
	close(m.chStop)
	return nil
}

func getGroupMonStat(mbData *reader.MBData) monitor.GroupMBStats {
	if mbData == nil {
		return nil
	}

	return mbData.MBBody
}

func (m *MBPlugin) run() {
	general.InfofV(6, "[mbm] plugin run start")

	mbData, err := m.reader.GetMBData()
	if err != nil {
		general.Errorf("[mbm] failed to get mb data: %v", err)
		return
	}

	statOutgoing := getGroupMonStat(mbData)
	if mbData == nil {
		general.Warningf("[mbm] got empty mb data")
		return
	}

	monData, err := monitor.NewDomainStats(statOutgoing, m.ccdToDomain, m.xDomGroups)
	if err != nil {
		general.Errorf("[mbm] failed to run fetching mb stats: %v", err)
		return
	}

	ctx := context.Background()
	var mbPlan *plan.MBPlan
	mbPlan, err = m.advisor.GetPlan(ctx, monData)
	if err != nil {
		general.Errorf("[mbm] failed to run getting plan: %v", err)
		return
	}
	if klog.V(6).Enabled() {
		general.Infof("[mbm] mb plan update: %v", mbPlan)
	}

	if err := m.planAllocator.Allocate(ctx, mbPlan); err != nil {
		general.Errorf("[mbm] failed to run allocating plan: %v", err)
		return
	}

	general.InfofV(6, "[mbm] plugin run end")
}

func newMBPlugin(ccdMinMB, ccdMaxMB int, defaultDomainCapacity int,
	domains domain.Domains, xDomGroups []string, groupNeverThrottles []string,
	groupCapacities map[string]int, metricFetcher metrictypes.MetricsFetcher,
	planAllocator allocator.PlanAllocator, emitPool metricspool.MetricsEmitterPool,
) skeleton.GenericPlugin {
	ccdMappings := domains.GetCCDMapping()
	emitter := emitPool.GetDefaultMetricsEmitter().WithTags(metricName)
	return &MBPlugin{
		emitter:     emitter,
		ccdToDomain: ccdMappings,
		xDomGroups:  sets.NewString(xDomGroups...),
		domains:     domains,
		reader:      reader.New(metricFetcher),
		advisor: advisor.New(ccdMinMB, ccdMaxMB, defaultDomainCapacity,
			xDomGroups, groupNeverThrottles, groupCapacities,
		),
		planAllocator: planAllocator,
	}
}
