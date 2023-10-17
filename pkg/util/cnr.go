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
	"sort"
	"strconv"

	info "github.com/google/cadvisor/info/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/pkg/apis/core/helper"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	CNRKind = "CustomNodeResource"
)

// those fields are used by in-tree reporter plugins to
// refer specific field names of CNR.
const (
	CNRFieldNameNodeResourceProperties = "NodeResourceProperties"
	CNRFieldNameTopologyZone           = "TopologyZone"
	CNRFieldNameResources              = "Resources"
	CNRFieldNameTopologyPolicy         = "TopologyPolicy"

	ResourceRDMA = "vke.volcengine.com/rdma"
)

var (
	CNRGroupVersionKind = metav1.GroupVersionKind{
		Group:   nodev1alpha1.SchemeGroupVersion.Group,
		Kind:    CNRKind,
		Version: nodev1alpha1.SchemeGroupVersion.Version,
	}
)

// GetCNRCondition extracts the provided condition from the given status and returns that.
// Returns nil and -1 if the condition is not present, and the index of the located condition.
func GetCNRCondition(status *apis.CustomNodeResourceStatus, conditionType apis.CNRConditionType) (int, *apis.CNRCondition) {
	if status == nil {
		return -1, nil
	}
	for i := range status.Conditions {
		if status.Conditions[i].Type == conditionType {
			return i, &status.Conditions[i]
		}
	}
	return -1, nil
}

// SetCNRCondition set specific cnr condition.
func SetCNRCondition(cnr *apis.CustomNodeResource, conditionType apis.CNRConditionType, status corev1.ConditionStatus, reason, message string, now metav1.Time) {
	i, currentTrueCondition := GetCNRCondition(&cnr.Status, conditionType)
	if currentTrueCondition == nil {
		observedCondition := apis.CNRCondition{
			Status:            status,
			LastHeartbeatTime: now,
			Type:              conditionType,
			Reason:            reason,
			Message:           message,
		}
		cnr.Status.Conditions = append(cnr.Status.Conditions, observedCondition)
	} else if !CheckCNRConditionMatched(currentTrueCondition, status, reason, message) {
		cnr.Status.Conditions[i].LastHeartbeatTime = now
		cnr.Status.Conditions[i].Reason = reason
		cnr.Status.Conditions[i].Status = status
		cnr.Status.Conditions[i].Message = message
	}
}

// CheckCNRConditionMatched checks if current CNR condition matches the update condition
func CheckCNRConditionMatched(curCondition *nodev1alpha1.CNRCondition, status corev1.ConditionStatus, reason, message string) bool {
	return curCondition.Status == status && curCondition.Reason == reason && curCondition.Message == message
}

// AddOrUpdateCNRTaint tries to add a taint to annotations list.
// Returns a new copy of updated CNR and true if something was updated false otherwise.
func AddOrUpdateCNRTaint(cnr *apis.CustomNodeResource, taint *apis.Taint) (*apis.CustomNodeResource, bool, error) {
	newCNR := cnr.DeepCopy()
	cTaints := newCNR.Spec.Taints

	var newTaints []*apis.Taint
	updated := false
	for i := range cTaints {
		if MatchCNRTaint(taint, cTaints[i]) {
			if helper.Semantic.DeepEqual(taint, cTaints[i]) {
				return newCNR, false, nil
			}
			newTaints = append(newTaints, taint)
			updated = true
			continue
		}

		newTaints = append(newTaints, cTaints[i])
	}

	if !updated {
		newTaints = append(newTaints, taint)
	}

	newCNR.Spec.Taints = newTaints
	return newCNR, true, nil
}

// RemoveCNRTaint tries to remove a taint cnr taints.
// Returns a new copy of updated CNR and true if something was updated false otherwise.
func RemoveCNRTaint(cnr *apis.CustomNodeResource, taint *apis.Taint) (*apis.CustomNodeResource, bool, error) {
	newCNR := cnr.DeepCopy()
	cTaints := newCNR.Spec.Taints
	if len(cTaints) == 0 {
		return newCNR, false, nil
	}

	if !CNRTaintExists(cTaints, taint) {
		return newCNR, false, nil
	}

	newTaints, _ := DeleteCNRTaint(cTaints, taint)
	newCNR.Spec.Taints = newTaints
	return newCNR, true, nil
}

// CNRTaintExists checks if the given taint exists in list of taints. Returns true if exists false otherwise.
func CNRTaintExists(taints []*apis.Taint, taintToFind *apis.Taint) bool {
	for _, taint := range taints {
		if MatchCNRTaint(taint, taintToFind) {
			return true
		}
	}
	return false
}

// DeleteCNRTaint removes all the taints that have the same key and effect to given taintToDelete.
func DeleteCNRTaint(taints []*apis.Taint, taintToDelete *apis.Taint) ([]*apis.Taint, bool) {
	var newTaints []*apis.Taint
	deleted := false
	for i := range taints {
		if MatchCNRTaint(taints[i], taintToDelete) {
			deleted = true
			continue
		}
		newTaints = append(newTaints, taints[i])
	}
	return newTaints, deleted
}

// MatchCNRTaint checks if the taint matches taintToMatch. Taints are unique by key:effect,
// if the two taints have same key:effect, regard as they match.
func MatchCNRTaint(taintToMatch, taint *apis.Taint) bool {
	return taint.Key == taintToMatch.Key && taint.Effect == taintToMatch.Effect
}

// MergeResources merges two resources, returns the merged result.
func MergeResources(dst, src apis.Resources) apis.Resources {
	dst.Capacity = native.MergeResources(dst.Capacity, src.Capacity)
	dst.Allocatable = native.MergeResources(dst.Allocatable, src.Allocatable)
	return dst
}

// MergeAttributes merges two attributes, returns the merged result.
// If the same attribute exists in both dst and src, the one in dst
// will be kept.
func MergeAttributes(dst, src []apis.Attribute) []apis.Attribute {
	if dst == nil && src == nil {
		return nil
	}

	attrMap := make(map[string]*apis.Attribute, len(dst))
	for _, attr := range dst {
		attrMap[attr.Name] = attr.DeepCopy()
	}

	for _, attr := range src {
		if _, ok := attrMap[attr.Name]; !ok {
			attrMap[attr.Name] = attr.DeepCopy()
		}
	}

	attrs := make([]apis.Attribute, 0, len(attrMap))
	for _, attr := range attrMap {
		attrs = append(attrs, *attr)
	}

	sort.SliceStable(attrs, func(i, j int) bool {
		return attrs[i].Name < attrs[j].Value
	})

	return attrs
}

// MergeAllocations merges two allocations, returns the merged result.
// If the same allocation exists in both dst and src, the one in dst
// will be kept.
func MergeAllocations(dst, src []*apis.Allocation) []*apis.Allocation {
	if dst == nil && src == nil {
		return nil
	}

	allocationMap := make(map[string]*apis.Allocation, len(dst))
	for _, allocation := range dst {
		if allocation == nil {
			continue
		}
		allocationMap[allocation.Consumer] = allocation.DeepCopy()
	}

	for _, allocation := range src {
		if allocation == nil {
			continue
		}

		if _, ok := allocationMap[allocation.Consumer]; !ok {
			allocationMap[allocation.Consumer] = allocation.DeepCopy()
			continue
		}

		allocationMap[allocation.Consumer].Requests =
			native.MergeResources(allocationMap[allocation.Consumer].Requests, allocation.Requests)
	}

	allocations := make([]*apis.Allocation, 0, len(allocationMap))
	for _, allocation := range allocationMap {
		allocations = append(allocations, allocation)
	}

	sort.SliceStable(allocations, func(i, j int) bool {
		return allocations[i].Consumer < allocations[j].Consumer
	})

	return allocations
}

// MergeTopologyZone merges two topology zones recursively, returns the merged result.
// If the same zone exists in both dst and src, the one in dst will be kept.
func MergeTopologyZone(dst, src []*apis.TopologyZone) []*apis.TopologyZone {
	if dst == nil && src == nil {
		return nil
	}

	zoneMap := make(map[ZoneNode]*apis.TopologyZone, len(dst))
	for _, zone := range dst {
		if zone == nil {
			continue
		}

		// in every level, the zone is unique by name and type
		node := ZoneNode{
			Meta: ZoneMeta{
				Type: zone.Type,
				Name: zone.Name,
			},
		}

		zoneMap[node] = zone.DeepCopy()
	}

	for _, zone := range src {
		if zone == nil {
			continue
		}

		// in every level, the zone is unique by name and type
		node := ZoneNode{
			Meta: ZoneMeta{
				Type: zone.Type,
				Name: zone.Name,
			},
		}

		if _, ok := zoneMap[node]; !ok {
			zoneMap[node] = zone.DeepCopy()
			continue
		}

		zoneMap[node].Resources = MergeResources(zoneMap[node].Resources, zone.Resources)
		zoneMap[node].Attributes = MergeAttributes(zoneMap[node].Attributes, zone.Attributes)
		zoneMap[node].Allocations = MergeAllocations(zoneMap[node].Allocations, zone.Allocations)
		zoneMap[node].Children = MergeTopologyZone(zoneMap[node].Children, zone.Children)
	}

	zones := make([]*apis.TopologyZone, 0, len(zoneMap))
	for _, zone := range zoneMap {
		zones = append(zones, zone)
	}

	sort.SliceStable(zones, func(i, j int) bool {
		if zones[i].Type == zones[j].Type {
			return zones[i].Name < zones[j].Name
		}
		return zones[i].Type < zones[j].Type
	})

	return zones
}

// NewNumaSocketTopologyZoneGenerator constructs topology generator by the numa zone node to socket zone node map
func NewNumaSocketTopologyZoneGenerator(numaSocketZoneNodeMap map[ZoneNode]ZoneNode) (*TopologyZoneGenerator, error) {
	var errList []error
	generator := NewZoneTopologyGenerator()
	for numaZoneNode, socketZoneNode := range numaSocketZoneNodeMap {
		// add socket zone node, which is no parent
		err := generator.AddNode(nil, socketZoneNode)
		if err != nil {
			errList = append(errList, err)
			continue
		}

		// add numa zone node
		err = generator.AddNode(&socketZoneNode, numaZoneNode)
		if err != nil {
			errList = append(errList, err)
			continue
		}
	}

	if len(errList) > 0 {
		return nil, errors.NewAggregate(errList)
	}

	return generator, nil
}

// GenerateNumaSocketZone parse numa info to get the map of numa zone node to socket zone node
func GenerateNumaSocketZone(nodes []info.Node) map[ZoneNode]ZoneNode {
	numaSocketZoneMap := make(map[ZoneNode]ZoneNode)
	for _, node := range nodes {
		// CAUTION: CNR design doesn't consider singer NUMA and multi sockets platform.
		// So here we think all cores in the same NUMA has the same socket ID.
		if len(node.Cores) > 0 {
			numaZoneNode := GenerateNumaZoneNode(node.Id)
			socketZoneNode := GenerateSocketZoneNode(node.Cores[0].SocketID)
			numaSocketZoneMap[numaZoneNode] = socketZoneNode
		}
	}

	return numaSocketZoneMap
}

// GenerateNumaZoneNode generates numa zone node by numa id, which must be unique
func GenerateNumaZoneNode(numaID int) ZoneNode {
	return ZoneNode{
		Meta: ZoneMeta{
			Type: nodev1alpha1.TopologyTypeNuma,
			Name: strconv.Itoa(numaID),
		},
	}
}

// GenerateSocketZoneNode generates socket zone node by socket id, which must be unique
func GenerateSocketZoneNode(socketID int) ZoneNode {
	return ZoneNode{
		Meta: ZoneMeta{
			Type: nodev1alpha1.TopologyTypeSocket,
			Name: strconv.Itoa(socketID),
		},
	}
}

// GenerateNICZoneNode generates nic zone node by socket id, which must be unique
func GenerateNICZoneNode(deviceId string) ZoneNode {
	return ZoneNode{
		Meta: ZoneMeta{
			Type: nodev1alpha1.TopologyTypeNIC,
			Name: deviceId,
		},
	}
}

func IsRDMA(resourceName string) bool {
	return ResourceRDMA == resourceName
}

// ParseDeviceID returns device ID parsed from the string as 64bit integer
func ParseDeviceID(deviceID string) (int64, error) {
	return strconv.ParseInt(deviceID, 16, 64)
}
