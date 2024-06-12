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

package helper

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/util/errors"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func GetAvailableNUMAsAndReclaimedCores(conf *config.Configuration, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer) (machine.CPUSet, []*types.ContainerInfo, error) {
	var errList []error

	availNUMAs := metaServer.CPUDetails.NUMANodes()
	reclaimedCoresContainers := make([]*types.ContainerInfo, 0)

	metaReader.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool {
		if reclaimedContainersFilter(containerInfo) {
			reclaimedCoresContainers = append(reclaimedCoresContainers, containerInfo)
			return true
		}

		nodeReclaim := conf.GetDynamicConfiguration().EnableReclaim
		reclaimEnable, err := PodEnableReclaim(context.Background(), metaServer, podUID, nodeReclaim)
		if err != nil {
			errList = append(errList, err)
			return true
		}

		if containerInfo.IsDedicatedNumaExclusive() && !reclaimEnable {
			memset := machine.GetCPUAssignmentNUMAs(containerInfo.TopologyAwareAssignments)
			if memset.IsEmpty() {
				errList = append(errList, fmt.Errorf("container(%v/%v) TopologyAwareAssignments is empty", containerInfo.PodName, containerName))
				return true
			}
			availNUMAs = availNUMAs.Difference(memset)
		}
		return true
	})

	err := errors.NewAggregate(errList)
	if err != nil {
		return machine.CPUSet{}, nil, err
	}

	return availNUMAs, reclaimedCoresContainers, nil
}

func reclaimedContainersFilter(ci *types.ContainerInfo) bool {
	return ci != nil && ci.QoSLevel == apiconsts.PodAnnotationQoSLevelReclaimedCores
}
