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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/apis/core/helper"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

const (
	CNRKind = "CustomNodeResource"
)

// those fields are used by in-tree reporter plugins to
// refer specific field names of CNR.
const (
	CNRFieldNameNodeResourceProperties = "NodeResourceProperties"
	CNRFieldNameTopologyStatus         = "TopologyStatus"
	CNRFieldNameResourceAllocatable    = "ResourceAllocatable"
	CNRFieldNameResourceCapacity       = "ResourceCapacity"
)

var (
	CNRGroupVersionKind = metav1.GroupVersionKind{
		Group:   nodev1alpha1.SchemeGroupVersion.Group,
		Kind:    CNRKind,
		Version: nodev1alpha1.SchemeGroupVersion.Version,
	}

	ReclaimedResourceNameToNativeResourceNameMap = map[corev1.ResourceName]corev1.ResourceName{
		consts.ReclaimedResourceMilliCPU: corev1.ResourceCPU,
		consts.ReclaimedResourceMemory:   corev1.ResourceMemory,
	}

	NoScheduleForReclaimedTasksTaint = apis.Taint{
		Key:    corev1.TaintNodeUnschedulable,
		Effect: apis.TaintEffectNoScheduleForReclaimedTasks,
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
			if helper.Semantic.DeepEqual(*taint, cTaints[i]) {
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
