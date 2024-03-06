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
	"context"
	"fmt"
	"math"

	info "github.com/google/cadvisor/info/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	qrmutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func GetFullyDropCacheBytes(container *v1.Container) int64 {
	if container == nil {
		return 0
	}

	memoryLimit := container.Resources.Limits[v1.ResourceMemory]
	memoryReq := container.Resources.Requests[v1.ResourceMemory]
	fullyDropCacheBytes := memoryLimit.Value()

	if fullyDropCacheBytes == 0 {
		fullyDropCacheBytes = memoryReq.Value()
	}

	return fullyDropCacheBytes
}

// GetReservedMemory is used to spread total reserved memories into per-numa level.
// this reserve resource calculation logic should be kept in qrm, if advisor wants
// to get this info, it should depend on the returned checkpoint (through cpu-server)
func getReservedMemory(conf *config.Configuration, metaServer *metaserver.MetaServer, machineInfo *info.MachineInfo) (map[int]uint64, error) {
	if conf == nil {
		return nil, fmt.Errorf("nil conf")
	} else if metaServer == nil {
		return nil, fmt.Errorf("nil metaServer")
	} else if machineInfo == nil {
		return nil, fmt.Errorf("nil machineInfo")
	}

	numasCount := len(machineInfo.Topology)

	var reservedMemoryGB float64
	if conf.UseKubeletReservedConfig {
		klConfig, err := metaServer.GetKubeletConfig(context.TODO())
		if err != nil {
			return nil, fmt.Errorf("failed to get kubelet config: %v", err)
		}

		reservedQuantity, found, err := qrmutil.GetKubeletReservedQuantity(string(v1.ResourceMemory), klConfig)
		if err != nil {
			return nil, fmt.Errorf("GetKubeletReservedQuantity failed with error: %v", err)
		} else if found {
			unitGB := resource.MustParse("1Gi")
			reservedMemoryGB = float64(reservedQuantity.Value()) / float64(unitGB.Value())
			general.Infof("get reservedMemoryGB: %.2f from kubelet config", reservedMemoryGB)
		} else if !found {
			reservedMemoryGB = float64(conf.ReservedMemoryGB)
			general.Infof("reserved memory config isn't found in kubelet config, fallback to get reservedMemoryGB: %.2f from ReservedMemoryGB configuration",
				reservedMemoryGB)
		}
	} else {
		reservedMemoryGB = float64(conf.ReservedMemoryGB)
		general.Infof("get reservedMemoryGB: %.2f from ReservedMemoryGB configuration", reservedMemoryGB)
	}

	perNumaReservedGB := uint64(math.Ceil(reservedMemoryGB / float64(numasCount)))
	perNumaReservedQuantity := resource.MustParse(fmt.Sprintf("%dGi", perNumaReservedGB))
	ceilReservedMemoryGB := perNumaReservedGB * uint64(numasCount)

	general.Infof("reservedMemoryGB: %.2f, ceilReservedMemoryGB: %d, perNumaReservedGB: %d, numasCount: %d",
		reservedMemoryGB, ceilReservedMemoryGB, perNumaReservedGB, numasCount)

	reservedMemory := make(map[int]uint64)
	for _, node := range machineInfo.Topology {
		reservedMemory[node.Id] = uint64(perNumaReservedQuantity.Value())
	}
	return reservedMemory, nil
}
