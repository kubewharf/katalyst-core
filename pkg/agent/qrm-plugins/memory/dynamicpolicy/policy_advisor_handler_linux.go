//go:build linux
// +build linux

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

package dynamicpolicy

import (
	"fmt"
	"strconv"

	"github.com/opencontainers/runc/libcontainer/cgroups"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func (p *DynamicPolicy) handleAdvisorMemoryHigh(
	_ *config.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	entryName, subEntryName string,
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries,
) error {
	memoryHighStr := calculationInfo.CalculationResult.Values[string(memoryadvisor.ControlKnobKeyMemoryHigh)]
	memoryHigh, err := strconv.ParseInt(memoryHighStr, 10, 64)
	if err != nil {
		return fmt.Errorf("parse %s: %s failed with error: %v", memoryadvisor.ControlKnobKeyMemoryHigh, memoryHighStr, err)
	}

	if !cgroups.IsCgroup2UnifiedMode() {
		general.Infof("memory.high is not supported in cgroupv1 mode")
		return nil
	}

	if calculationInfo.CgroupPath != "" {
		if err = cgroupmgr.ApplyMemoryWithRelativePath(calculationInfo.CgroupPath, &common.MemoryData{
			HighInBytes: memoryHigh,
		}); err != nil {
			return fmt.Errorf("apply memory.high failed with error: %v", err)
		}

		_ = emitter.StoreInt64(util.MetricNameMemoryHandleAdvisorMemoryHigh, memoryHigh,
			metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(map[string]string{
				"cgroupPath": calculationInfo.CgroupPath,
			})...)
		return nil
	}

	return nil
}
