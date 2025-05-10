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

package server

import (
	"encoding/json"

	"github.com/opencontainers/runc/libcontainer/configs"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

const (
	DefaultCFSCPUPeriod = 100000
)

func (cs *cpuServer) assembleCgroupConfig(advisorResp *types.InternalCPUCalculationResult) (extraEntries []*advisorsvc.CalculationInfo) {
	for poolName, entries := range advisorResp.PoolEntries {
		if poolName != commonstate.PoolNameReclaim {
			continue
		}

		// range from fakeNUMAID
		for numaID := -1; numaID < cs.metaServer.NumNUMANodes; numaID++ {
			quota := int64(-1)
			cpuResource, ok := entries[numaID]
			if ok && cpuResource.Quota > 0 {
				quota = int64(cpuResource.Quota * DefaultCFSCPUPeriod)
			}
			resourceConf := &configs.Resources{
				CpuQuota:  quota,
				CpuPeriod: DefaultCFSCPUPeriod,
			}
			bytes, err := json.Marshal(resourceConf)
			if err != nil {
				klog.ErrorS(err, "")
				continue
			}

			extraEntries = append(extraEntries, &advisorsvc.CalculationInfo{
				CgroupPath: common.GetReclaimRelativeRootCgroupPath(cs.reclaimRelativeRootCgroupPath, numaID),
				CalculationResult: &advisorsvc.CalculationResult{
					Values: map[string]string{
						string(cpuadvisor.ControlKnobKeyCgroupConfig): string(bytes),
					},
				},
			})
		}
	}
	return
}
