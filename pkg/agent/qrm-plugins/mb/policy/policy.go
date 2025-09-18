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
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/allocator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/domain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/reader"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	vendorAMD     = "AuthenticAMD"
	minMBCapacity = 10_000
)

func isSupported(cpuVendorID string) bool {
	// only AMD is supported
	return cpuVendorID == vendorAMD
}

func NewGenericPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	general.Infof("[mbm] to create generic policy qrm_mb_plugin")

	cpuVendor := agentCtx.CPUVendorID
	if !isSupported(cpuVendor) {
		general.Infof("[mbm] unsupported cpu arch %s", cpuVendor)
		return false, nil, nil
	}

	defaultMBDomainCapacity := int(agentCtx.KatalystMachineInfo.SiblingNumaMBWAllocatable) / 1024 / 1024
	if defaultMBDomainCapacity < minMBCapacity {
		general.Infof("[mbm] invalid domain mb capacity %d as configured; not to enable mbm", defaultMBDomainCapacity)
		return false, nil, nil
	}

	if klog.V(6).Enabled() {
		// to print out numa siblings as they are critical to get proper mb domains
		numaSiblings := agentCtx.SiblingNumaMap
		for id, siblings := range numaSiblings {
			general.Infof("[mbm] numa %d, siblings %v", id, siblings)
		}
	}

	general.Infof("[mbm] config: default mb domain capacity %d MB (machineInfo SiblingNumaMBWAllocatable)",
		defaultMBDomainCapacity)
	general.Infof("[mbm] config: group customized capacities %v", conf.DomainGroupAwareCapacity)
	general.Infof("[mbm] config: min ccd mb %d MB", conf.MinCCDMB)
	general.Infof("[mbm] config: max ccd mb %d MB", conf.MaxCCDMB)
	general.Infof("[mbm] config: domain alient incoming mb limit %d MB", conf.MaxIncomingRemoteMB)
	general.Infof("[mbm] config: mb cap limit percent %d%%%%", conf.MBCapLimitPercent)
	general.Infof("[mbm] config: mb active traffic mb threshold %d MB", conf.ActiveTrafficMBThreshold)
	general.Infof("[mbm] config: no-throtlle groups %v", conf.NoThrottleGroups)
	general.Infof("[mbm] config: cross-domain groups %v", conf.CrossDomainGroups)
	general.Infof("[mbm] config: local is victim and total is all read %v", conf.LocalIsVictimAndTotalIsAllRead)

	ccdMinMB := conf.MinCCDMB
	ccdMaxMB := conf.MaxCCDMB
	maxIncomingRemoteMB := conf.MaxIncomingRemoteMB
	mbCapLimitPercent := conf.MBCapLimitPercent
	groupCapacities := conf.MBQRMPluginConfig.DomainGroupAwareCapacity
	groupNeverThrottles := conf.MBQRMPluginConfig.NoThrottleGroups
	xDomGroups := conf.CrossDomainGroups

	// todo: avoid package var
	monitor.MinActiveMB = conf.ActiveTrafficMBThreshold
	reader.LocalIsVictimAndTotalIsAllRead = conf.LocalIsVictimAndTotalIsAllRead

	domains, err := domain.NewDomainsByMachineInfo(agentCtx.KatalystMachineInfo, maxIncomingRemoteMB)
	if err != nil {
		general.Infof("[mbm] invalid config in machine info: %v", err)
		return false, nil, nil
	}

	metricsFetcher := agentCtx.MetaServer.MetricsFetcher
	planAllocator := allocator.New()
	mbPlugin := newMBPlugin(conf.ResetResctrlOnly,
		ccdMinMB, ccdMaxMB,
		defaultMBDomainCapacity, mbCapLimitPercent, domains,
		xDomGroups, groupNeverThrottles, groupCapacities,
		metricsFetcher, planAllocator, agentCtx.EmitterPool)
	return true, &agent.PluginWrapper{GenericPlugin: mbPlugin}, nil
}
