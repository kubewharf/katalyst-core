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

package reclaim

import (
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func init() {
	RegisterFactory(GenericConsumerName, NewGenericConsumer)
}

// GenericConsumerName is the registry key used for the default GenericConsumer.
const GenericConsumerName = "generic"

// GenericConsumer is the default ReclaimedConsumer implementation. It holds
// only the scalar values it needs, so it does not couple the reclaim package
// to any wider agent configuration type.
type GenericConsumer struct {
	cgroupPath             string
	numaBindingCgroupPaths map[int]string
}

// NewGenericConsumer constructs a GenericConsumer from the agent config.
func NewGenericConsumer(conf *config.Configuration, machineInfo *machine.KatalystMachineInfo) ReclaimedConsumer {
	g := &GenericConsumer{cgroupPath: conf.BaseConfiguration.ReclaimRelativeRootCgroupPath}
	if g.cgroupPath == "" || machineInfo == nil || machineInfo.CPUTopology == nil {
		return g
	}
	numaIDs := machineInfo.CPUDetails.NUMANodes().ToSliceNoSortInt()
	g.numaBindingCgroupPaths = common.GetNUMABindingReclaimRelativeRootCgroupPaths(g.cgroupPath, numaIDs)
	return g
}

var _ ReclaimedConsumer = (*GenericConsumer)(nil)

func (g *GenericConsumer) GetCgroupPath() string {
	return g.cgroupPath
}

func (g *GenericConsumer) GetNumaBindingCgroupPaths() map[int]string {
	return g.numaBindingCgroupPaths
}

func (g *GenericConsumer) GetAllCgroupPaths() []string {
	paths := make([]string, 0, 1+len(g.numaBindingCgroupPaths))
	if g.cgroupPath != "" {
		paths = append(paths, g.cgroupPath)
	}
	for _, path := range g.numaBindingCgroupPaths {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}
