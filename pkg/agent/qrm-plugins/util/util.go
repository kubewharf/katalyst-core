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

package util

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"sort"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// GetQuantityFromResourceReq parses resources quantity into value,
// since pods with reclaimed_cores and un-reclaimed_cores have different
// representations, we may to adapt to both cases.
func GetQuantityFromResourceReq(req *pluginapi.ResourceRequest) (int, error) {
	if len(req.ResourceRequests) != 1 {
		return 0, fmt.Errorf("invalid req.ResourceRequests length: %d", len(req.ResourceRequests))
	}

	for key := range req.ResourceRequests {
		switch key {
		case string(v1.ResourceCPU):
			return general.Max(int(math.Ceil(req.ResourceRequests[key])), 0), nil
		case string(consts.ReclaimedResourceMilliCPU):
			return general.Max(int(math.Ceil(req.ResourceRequests[key]/1000.0)), 0), nil
		case string(v1.ResourceMemory), string(consts.ReclaimedResourceMemory):
			return general.Max(int(math.Ceil(req.ResourceRequests[key])), 0), nil
		default:
			return 0, fmt.Errorf("invalid request resource name: %s", key)
		}
	}

	return 0, fmt.Errorf("unexpected end")
}

// GetKatalystQoSLevelFromResourceReq retrieves QoS Level for a given request
func GetKatalystQoSLevelFromResourceReq(qosConf *generic.QoSConfiguration, req *pluginapi.ResourceRequest) (qosLevel string, err error) {
	if req == nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq got nil resource request")
		return
	}

	var getErr error
	qosLevel, getErr = qosConf.GetQoSLevel(req.Annotations)
	if getErr != nil {
		err = fmt.Errorf("resource type mismatches: %v", getErr)
		return
	}

	// setting annotations and labels to only keep katalyst QoS related values
	if req.Annotations == nil {
		req.Annotations = make(map[string]string)
	}
	req.Annotations[consts.PodAnnotationQoSLevelKey] = qosLevel
	parsedAnnotations, err := qosConf.FilterQoSAndEnhancement(req.Annotations)
	if err != nil {
		err = fmt.Errorf("ParseKatalystAnnotations failed with error: %v", err)
		return
	}
	req.Annotations = parsedAnnotations

	if req.Labels == nil {
		req.Labels = make(map[string]string)
	}
	req.Labels[consts.PodAnnotationQoSLevelKey] = qosLevel
	req.Labels = qosConf.FilterQoSMap(req.Labels)
	return
}

// HintToIntArray transforms TopologyHint to int slices
func HintToIntArray(hint *pluginapi.TopologyHint) []int {
	if hint == nil {
		return []int{}
	}

	result := make([]int, 0, len(hint.Nodes))
	for _, node := range hint.Nodes {
		result = append(result, int(node))
	}

	return result
}

// GetTopologyAwareQuantityFromAssignments returns TopologyAwareQuantity based on assignments
func GetTopologyAwareQuantityFromAssignments(assignments map[int]machine.CPUSet) []*pluginapi.TopologyAwareQuantity {
	if assignments == nil {
		return nil
	}

	topologyAwareQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(assignments))

	numaNodes := make([]int, 0, len(assignments))
	for numaNode := range assignments {
		numaNodes = append(numaNodes, numaNode)
	}
	sort.Ints(numaNodes)

	for _, numaNode := range numaNodes {
		cpus := assignments[numaNode]
		topologyAwareQuantityList = append(topologyAwareQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(cpus.Size()),
			Node:          uint64(numaNode),
		})
	}

	return topologyAwareQuantityList
}

// GetTopologyAwareQuantityFromAssignmentsSize returns TopologyAwareQuantity based on assignments,
// and assignments will use resource size (instead of resource struct)
func GetTopologyAwareQuantityFromAssignmentsSize(assignments map[int]uint64) []*pluginapi.TopologyAwareQuantity {
	if assignments == nil {
		return nil
	}

	topologyAwareQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(assignments))

	numaNodes := make([]int, 0, len(assignments))
	for numaNode := range assignments {
		numaNodes = append(numaNodes, numaNode)
	}
	sort.Ints(numaNodes)

	for _, numaNode := range numaNodes {
		topologyAwareQuantityList = append(topologyAwareQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(assignments[numaNode]),
			Node:          uint64(numaNode),
		})
	}

	return topologyAwareQuantityList
}

// PackResourceHintsResponse returns the standard QRM ResourceHintsResponse
func PackResourceHintsResponse(req *pluginapi.ResourceRequest, resourceName string,
	resourceHints map[string]*pluginapi.ListOfTopologyHints) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("PackResourceHintsResponse got nil request")
	}

	return &pluginapi.ResourceHintsResponse{
		PodUid:         req.PodUid,
		PodNamespace:   req.PodNamespace,
		PodName:        req.PodName,
		ContainerName:  req.ContainerName,
		ContainerType:  req.ContainerType,
		ContainerIndex: req.ContainerIndex,
		PodRole:        req.PodRole,
		PodType:        req.PodType,
		ResourceName:   resourceName,
		ResourceHints:  resourceHints,
		Labels:         general.DeepCopyMap(req.Labels),
		Annotations:    general.DeepCopyMap(req.Annotations),
	}, nil
}

// GetNUMANodesCountToFitCPUReq is used to calculate the amount of numa nodes
// we need if we try to allocate cpu cores among them, assuming that all numa nodes
// contain the same cpu capacity
func GetNUMANodesCountToFitCPUReq(cpuReq int, cpuTopology *machine.CPUTopology) (int, int, error) {
	if cpuTopology == nil {
		return 0, 0, fmt.Errorf("GetNumaNodesToFitCPUReq got nil cpuTopology")
	}

	numaCount := cpuTopology.CPUDetails.NUMANodes().Size()
	if numaCount == 0 {
		return 0, 0, fmt.Errorf("there is no NUMA in cpuTopology")
	}

	if cpuTopology.NumCPUs%numaCount != 0 {
		return 0, 0, fmt.Errorf("invalid NUMAs count: %d with CPUs count: %d", numaCount, cpuTopology.NumCPUs)
	}

	cpusPerNUMA := cpuTopology.NumCPUs / numaCount
	numaCountNeeded := int(math.Ceil(float64(cpuReq) / float64(cpusPerNUMA)))
	if numaCountNeeded > numaCount {
		return 0, 0, fmt.Errorf("invalid cpu req: %d in topology with NUMAs count: %d and CPUs count: %d", cpuReq, numaCount, cpuTopology.NumCPUs)
	}

	cpusCountNeededPerNUMA := int(math.Ceil(float64(cpuReq) / float64(numaCountNeeded)))
	return numaCountNeeded, cpusCountNeededPerNUMA, nil
}

// GetNUMANodesCountToFitMemoryReq is used to calculate the amount of numa nodes
// we need if we try to allocate memory among them, assuming that all numa nodes
// contain the same memory capacity
func GetNUMANodesCountToFitMemoryReq(memoryReq, bytesPerNUMA uint64, numaCount int) (int, uint64, error) {
	numaCountNeeded := int(math.Ceil(float64(memoryReq) / float64(bytesPerNUMA)))
	if numaCountNeeded > numaCount {
		return 0, 0, fmt.Errorf("invalid memory req: %d in topology with NUMAs count: %d and bytesPerNUMA: %d", memoryReq, numaCount, bytesPerNUMA)
	}

	bytesNeededPerNUMA := uint64(math.Ceil(float64(memoryReq) / float64(numaCountNeeded)))
	return numaCountNeeded, bytesNeededPerNUMA, nil
}

// GetHintsFromExtraStateFile
// if you want to specify cpuset.mems for specific pods (eg. for existing pods) when switching
// to katalyst the first time, you can provide an extra hints state file with content like below:
/*
{
	"memoryEntries": {
		"dp-18a916b04c-bdc9d5fd9-8m7vr-0": "0-1",
		"dp-18a916b04c-bdc9d5fd9-h9tgp-0": "5,7",
		"dp-47320a8d77-f46d6cbc7-5r27s-0": "2-3",
		"dp-d7e988f508-5f66655c5-8n2tf-0": "4,6"
	},
}
*/
func GetHintsFromExtraStateFile(podName, resourceName, extraHintsStateFileAbsPath string) (map[string]*pluginapi.ListOfTopologyHints, error) {
	if extraHintsStateFileAbsPath == "" {
		return nil, nil
	}

	fileBytes, err := ioutil.ReadFile(extraHintsStateFileAbsPath)
	if err != nil {
		return nil, fmt.Errorf("read extra hints state file failed with error: %v", err)
	}

	extraState := make(map[string]interface{})
	err = json.Unmarshal(fileBytes, &extraState)
	if err != nil {
		return nil, fmt.Errorf("unmarshal extra state file content failed with error: %v", err)
	}

	memoryEntries, typeOk := extraState["memoryEntries"].(map[string]interface{})
	if !typeOk {
		return nil, fmt.Errorf("memory entries with invalid type: %T", extraState["memoryEntries"])
	}

	extraPodName := fmt.Sprintf("%s-0", podName)
	if memoryEntries[extraPodName] == nil {
		return nil, fmt.Errorf("extra state file hasn't memory entry for pod: %s", extraPodName)
	}

	memoryEntry, typeOk := memoryEntries[extraPodName].(string)
	if !typeOk {
		return nil, fmt.Errorf("memory entry with invalid type: %T", memoryEntries[extraPodName])
	}

	numaSet, err := machine.Parse(memoryEntry)
	if err != nil {
		return nil, fmt.Errorf("parse memory entry: %s failed with error: %v", memoryEntry, err)
	}

	allocatedNumaNodes := numaSet.ToSliceUInt64()
	klog.InfoS("[GetHintsFromExtraStateFile] get hints from extra state file",
		"podName", podName,
		"resourceName", resourceName,
		"hint", allocatedNumaNodes)

	hints := map[string]*pluginapi.ListOfTopologyHints{
		resourceName: {
			Hints: []*pluginapi.TopologyHint{
				{
					Nodes:     allocatedNumaNodes,
					Preferred: true,
				},
			},
		},
	}
	return hints, nil
}
