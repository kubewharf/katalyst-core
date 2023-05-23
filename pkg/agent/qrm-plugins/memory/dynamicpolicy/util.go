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
	"math"

	info "github.com/google/cadvisor/info/v1"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"k8s.io/apimachinery/pkg/api/resource"
)

// GetReservedMemory is used to spread total reserved memories into per-numa level.
// this reserve resource calculation logic should be kept in qrm, if advisor wants
// to get this info, it should depend on the returned checkpoint (through cpu-server)
func getReservedMemory(machineInfo *info.MachineInfo, reservedMemoryGB uint64) (map[int]uint64, error) {
	if machineInfo == nil {
		return nil, fmt.Errorf("getReservedMemory got nil machineInfo")
	}

	numasCount := len(machineInfo.Topology)
	perNumaReservedGB := uint64(math.Ceil(float64(reservedMemoryGB) / float64(numasCount)))
	perNumaReservedQuantity := resource.MustParse(fmt.Sprintf("%dGi", perNumaReservedGB))
	ceilReservedMemoryGB := perNumaReservedGB * uint64(numasCount)

	general.Infof("reservedMemoryGB: %d, ceilReservedMemoryGB: %d, perNumaReservedGB: %d, numasCount: %d",
		reservedMemoryGB, ceilReservedMemoryGB, perNumaReservedGB, numasCount)

	reservedMemory := make(map[int]uint64)
	for _, node := range machineInfo.Topology {
		reservedMemory[node.Id] = uint64(perNumaReservedQuantity.Value())
	}
	return reservedMemory, nil
}
