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
	"fmt"
	"sort"
	"strconv"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

// ZoneMeta is a struct that contains the type and name of a zone.
type ZoneMeta struct {
	Type nodev1alpha1.TopologyType
	Name string
}

// ZoneNode is a struct that contains the meta and an ID of a zone.
type ZoneNode struct {
	Meta ZoneMeta
}

// ZoneAttributes is list of attributes
type ZoneAttributes []nodev1alpha1.Attribute

// ZoneAllocations is list of allocations
type ZoneAllocations []*nodev1alpha1.Allocation

// ZoneSiblings is list of siblings
type ZoneSiblings []nodev1alpha1.Sibling

// ZoneTopology is a tree diagram of a zone
type ZoneTopology struct {
	Children map[ZoneNode]*ZoneTopology
}

func NewZoneTopology() *ZoneTopology {
	return &ZoneTopology{
		Children: make(map[ZoneNode]*ZoneTopology),
	}
}

// TopologyZoneGenerator is a struct that generates a tree diagram of zone,
// it uses AddNode to add new zone node into this tree according to its parent,
// and gets a list of TopologyZone by calling GenerateTopologyZoneStatus with
// the zone information map.
// TopologyZoneGenerator will be used by reporter plugin
type TopologyZoneGenerator struct {
	// rootZoneTopology is root topology of the zone tree
	rootZoneTopology *ZoneTopology

	// rootZoneTopology is children topology of all zoneNode,
	// it will be used as cache to construct zone tree
	subZoneTopology map[ZoneNode]*ZoneTopology
}

// NewZoneTopologyGenerator creates a new TopologyZoneGenerator
func NewZoneTopologyGenerator() *TopologyZoneGenerator {
	return &TopologyZoneGenerator{
		rootZoneTopology: NewZoneTopology(),
		subZoneTopology:  make(map[ZoneNode]*ZoneTopology),
	}
}

// AddNode adds a node to the zone tree,
//   - if parent is nil, it will be added to the root topology
//   - if parent is not nil, it will be added to the sub topology of the parent,
//     the parent must already add into this generator before
func (z *TopologyZoneGenerator) AddNode(parent *ZoneNode, current ZoneNode) error {
	if parent == nil {
		if _, ok := z.rootZoneTopology.Children[current]; !ok {
			newZoneTopology := NewZoneTopology()
			z.rootZoneTopology.Children[current] = newZoneTopology
			z.subZoneTopology[current] = newZoneTopology
		}
	} else {
		// if the zone node has been added into subZoneTopology just skip it,
		// and this requires that we won't add a ZoneNode node twice
		if _, ok := z.subZoneTopology[current]; ok {
			return nil
		}

		// try to get children topology of parent from subZoneTopology and add current zone node to it,
		// if not found parent in subZoneTopology just return error
		if top, ok := z.subZoneTopology[*parent]; ok {
			newZoneTopology := NewZoneTopology()
			top.Children[current] = newZoneTopology
			z.subZoneTopology[current] = newZoneTopology
		} else {
			return fmt.Errorf("zone node %v parent %v not found", current, parent)
		}
	}
	return nil
}

// GenerateTopologyZoneStatus generates topology zone status by allocations, resources and attributes
func (z *TopologyZoneGenerator) GenerateTopologyZoneStatus(
	allocationsMap map[ZoneNode]ZoneAllocations,
	resourcesMap map[ZoneNode]nodev1alpha1.Resources,
	attributesMap map[ZoneNode]ZoneAttributes,
	siblingsMap map[ZoneNode]ZoneSiblings,
) []*nodev1alpha1.TopologyZone {
	return generateTopologyZoneStatus(z.rootZoneTopology, allocationsMap, resourcesMap, attributesMap, siblingsMap)
}

// generateTopologyZoneStatus generates topology zone status
func generateTopologyZoneStatus(
	zoneTopology *ZoneTopology,
	allocationsMap map[ZoneNode]ZoneAllocations,
	resourcesMap map[ZoneNode]nodev1alpha1.Resources,
	attributesMap map[ZoneNode]ZoneAttributes,
	siblingsMap map[ZoneNode]ZoneSiblings,
) []*nodev1alpha1.TopologyZone {
	if zoneTopology == nil {
		return nil
	}

	klog.Infof("[KFX]generateTopologyZoneStatus zoneTopology:%+v", *zoneTopology)
	klog.Infof("[KFX]generateTopologyZoneStatus allocationsMap:%+v", allocationsMap)
	klog.Infof("[KFX]generateTopologyZoneStatus resourcesMap:%+v", resourcesMap)
	klog.Infof("[KFX]generateTopologyZoneStatus attributesMap:%+v", attributesMap)
	klog.Infof("[KFX]generateTopologyZoneStatus siblingsMap:%+v", siblingsMap)
	var result []*nodev1alpha1.TopologyZone
	for zone, topology := range (*zoneTopology).Children {
		klog.Infof("[KFX]generateTopologyZoneStatus children zone:%+v", zone)
		topologyZone := &nodev1alpha1.TopologyZone{
			Type: zone.Meta.Type,
			Name: zone.Meta.Name,
		}

		if resources, ok := resourcesMap[zone]; ok {
			topologyZone.Resources = resources
		}

		if attributes, ok := attributesMap[zone]; ok {
			// merge attributes to make sure that the attributes are unique and sorted
			topologyZone.Attributes = MergeAttributes(topologyZone.Attributes, attributes)
		}

		if allocations, ok := allocationsMap[zone]; ok {
			// merge allocations to make sure that the allocations are unique and sorted
			topologyZone.Allocations = MergeAllocations(topologyZone.Allocations, allocations)
		}

		if siblings, ok := siblingsMap[zone]; ok {
			topologyZone.Siblings = MergeSiblings(topologyZone.Siblings, siblings)
		}

		if topology != nil {
			zoneChildren := generateTopologyZoneStatus(topology, allocationsMap, resourcesMap, attributesMap, siblingsMap)
			if len(zoneChildren) > 0 {
				topologyZone.Children = zoneChildren
			}
		}

		result = append(result, topologyZone)
	}

	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Type == result[j].Type {
			return result[i].Name < result[j].Name
		}
		return result[i].Type < result[j].Type
	})

	return result
}

// ValidateSharedCoresWithNumaBindingPod is to check whether zone requests of shared_cores with numa_binding pod is valid
func ValidateSharedCoresWithNumaBindingPod(qosConf *generic.QoSConfiguration, pod *v1.Pod, zoneRequests map[ZoneNode]*v1.ResourceList) (bool, error) {
	sharedQoS, err := qosConf.CheckSharedQoS(pod, map[string]string{})
	if err != nil {
		return false, err
	}

	if !sharedQoS || !qos.IsPodNumaBinding(qosConf, pod) {
		return false, nil
	}

	var bindingNumaNode *ZoneNode
	for zoneNode, resourceList := range zoneRequests {
		if zoneNode.Meta.Type != nodev1alpha1.TopologyTypeNuma {
			continue
		}

		if resourceList != nil &&
			(!resourceList.Cpu().IsZero() || !resourceList.Memory().IsZero()) {
			// check whether cpu or memory are bound to more than one numa node
			if bindingNumaNode != nil {
				return false, fmt.Errorf("shared_cores with numa binding pod cpu or memory " +
					"not support binding more than one numa node")
			}
			bindingNumaNode = &zoneNode
		}
	}

	if bindingNumaNode == nil {
		return false, fmt.Errorf("shared_cores with numa binding pod without binding numa")
	}

	return true, nil
}

func GetReclaimedCPUPerNUMA(topologyZones []*nodev1alpha1.TopologyZone) map[int]float64 {
	numaMap := make(map[int]float64)
	for _, topologyZone := range topologyZones {
		if topologyZone.Type != nodev1alpha1.TopologyTypeSocket {
			continue
		}

		for _, child := range topologyZone.Children {
			if child.Type != nodev1alpha1.TopologyTypeNuma {
				continue
			}

			numaID, err := strconv.Atoi(child.Name)
			if err != nil {
				klog.Errorf("invalid numa name: %v, %v", child.Name, err)
				continue
			}

			if child.Resources.Allocatable == nil {
				klog.Errorf("numa zone without allocatable resource: %d", numaID)
				continue
			}

			if reclaimedMilliCPU, ok := (*child.Resources.Allocatable)[consts.ReclaimedResourceMilliCPU]; ok {
				numaMap[numaID] = float64(reclaimedMilliCPU.Value()) / 1000
			}
		}
	}

	return numaMap
}
