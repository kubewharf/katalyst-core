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
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var configTranslator = general.NewCommonSuffixTranslator(commonstate.NUMAPoolInfix)

func GetAvailableNUMAsAndReclaimedCores(conf *config.Configuration, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer) (machine.CPUSet, []*types.ContainerInfo, error) {
	var errList []error

	availNUMAs := metaServer.CPUDetails.NUMANodes()
	reclaimedCoresContainers := make([]*types.ContainerInfo, 0)

	dynamicConf := conf.GetDynamicConfiguration()

	metaReader.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool {
		ctx := context.Background()
		if reclaimedContainersFilter(containerInfo) {
			pod, err := metaServer.GetPod(ctx, podUID)
			if err != nil {
				errList = append(errList, err)
				return true
			}

			if native.PodIsActive(pod) {
				reclaimedCoresContainers = append(reclaimedCoresContainers, containerInfo)
			} else {
				general.InfoS("pod is in-active",
					"podUID", podUID,
					"name", containerInfo.PodName,
					"namespace", containerInfo.PodNamespace,
					"container", containerInfo.ContainerName,
					"qos_level", containerInfo.QoSLevel)
			}
			return true
		}

		nodeReclaim := dynamicConf.EnableReclaim
		reclaimEnable, err := PodEnableReclaim(ctx, metaServer, podUID, nodeReclaim)
		if err != nil {
			errList = append(errList, err)
			return true
		}

		if !reclaimEnable {
			memset := machine.GetCPUAssignmentNUMAs(containerInfo.TopologyAwareAssignments)
			if memset.IsEmpty() {
				errList = append(errList, fmt.Errorf("container(%v/%v) TopologyAwareAssignments is empty", containerInfo.PodName, containerName))
				return true
			}

			level := PodDisableReclaimLevel(ctx, metaServer, podUID)
			switch level {
			case v1alpha1.DisableReclaimLevelNUMA:
				availNUMAs = availNUMAs.Difference(memset)
			case v1alpha1.DisableReclaimLevelSocket:
				var sockets []int
				for _, numa := range memset.ToSliceNoSortInt() {
					sockets = append(sockets, metaServer.NUMANodeIDToSocketID[numa])
				}
				availNUMAs = availNUMAs.Difference(metaServer.CPUDetails.NUMANodesInSockets(sockets...))
			case v1alpha1.DisableReclaimLevelNode:
				availNUMAs = machine.NewCPUSet()
			case v1alpha1.DisableReclaimLevelPod:
				if containerInfo.IsDedicatedNumaExclusive() {
					availNUMAs = availNUMAs.Difference(memset)
				}
				// todo support pod level disable reclaim for other qos levels in the feature
			}
			general.InfoS("disable reclaim",
				"podUID", podUID,
				"name", containerInfo.PodName,
				"namespace", containerInfo.PodNamespace,
				"container", containerInfo.ContainerName,
				"qosLevel", containerInfo.QoSLevel,
				"memset", memset,
				"nodeReclaim", nodeReclaim,
				"disableReclaimLevel", level)
		}

		if containerInfo.IsSharedNumaBinding() &&
			sets.NewString(dynamicConf.DisableReclaimSharePools...).Has(configTranslator.Translate(containerInfo.OriginOwnerPoolName)) {
			bindingResult, err := containerInfo.GetActualNUMABindingResult()
			if err != nil {
				errList = append(errList, err)
				return true
			}

			if bindingResult == -1 {
				return true
			}

			// numa node -> socket
			socketID, ok := metaServer.NUMANodeIDToSocketID[bindingResult]
			if !ok {
				errList = append(errList, fmt.Errorf("numa node %v has no socket", bindingResult))
				return true
			}

			// disable reclaim for all the numa nodes in the socket which has disabled reclaim numa binding pool
			availNUMAs = availNUMAs.Difference(metaServer.CPUDetails.NUMANodesInSockets(socketID))
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

// GetActualNUMABindingNUMAsForReclaimedCores gets the actual numa binding numas according to the pod annotation
// if numa with numa binding result is not -1, it will be added to numa binding numas
func GetActualNUMABindingNUMAsForReclaimedCores(metaReader metacache.MetaReader) (machine.CPUSet, error) {
	// filter pods with numa binding result
	actualNUMABindingNUMAs := machine.NewCPUSet()
	var errList []error
	metaReader.RangeContainer(func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		if !reclaimedContainersFilter(ci) {
			return true
		}

		bindingResult, err := ci.GetActualNUMABindingResult()
		if err != nil {
			errList = append(errList, err)
			return false
		}

		if bindingResult == -1 {
			return true
		}

		actualNUMABindingNUMAs.Add(bindingResult)
		return true
	})

	if len(errList) > 0 {
		return machine.CPUSet{}, errors.NewAggregate(errList)
	}

	return actualNUMABindingNUMAs, nil
}
