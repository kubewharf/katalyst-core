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
	"context"
	"fmt"
	"reflect"
	"time"

	core "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
)

const (
	VPARecConditionReasonUpdated = "Updated"
	VPARecConditionReasonIllegal = "Illegal"
)

const (
	VPAConditionReasonUpdated           = "Updated"
	VPAConditionReasonCalculatedIllegal = "Illegal"
	VPAConditionReasonPodSpecUpdated    = "PodSpecUpdated"
	VPAConditionReasonPodSpecNoUpdate   = "PodSpecNoUpdate"
)

// SetVPAConditions is used to set conditions for vpa in local vpa
func SetVPAConditions(
	vpa *apis.KatalystVerticalPodAutoscaler,
	conditionType apis.VerticalPodAutoscalerConditionType,
	conditionStatus core.ConditionStatus,
	reason, message string,
) error {
	if vpa == nil {
		return fmt.Errorf("can't update condition of a nil vpa")
	}
	conditions := vpa.Status.Conditions

	var conditionIndex int
	for conditionIndex = 0; conditionIndex < len(conditions); conditionIndex++ {
		if conditions[conditionIndex].Type == conditionType {
			break
		}
	}
	if conditionIndex == len(conditions) {
		conditions = append(conditions, apis.VerticalPodAutoscalerCondition{
			Type: conditionType,
		})
	}

	condition := &conditions[conditionIndex]
	if condition.Status != conditionStatus || condition.Message != message ||
		condition.Reason != reason {
		condition.LastTransitionTime = metav1.NewTime(time.Now())
	}

	condition.Status = conditionStatus
	condition.Reason = reason
	condition.Message = message
	vpa.Status.Conditions = conditions
	return nil
}

// SetVPARecConditions is used to set conditions for vpaRec in local vpa
func SetVPARecConditions(
	vpaRec *apis.VerticalPodAutoscalerRecommendation,
	conditionType apis.VerticalPodAutoscalerRecommendationConditionType,
	conditionStatus core.ConditionStatus,
	reason, message string,
) error {
	if vpaRec == nil {
		return fmt.Errorf("can't update condition of a nil vpa")
	}
	conditions := vpaRec.Status.Conditions

	var conditionIndex int
	for conditionIndex = 0; conditionIndex < len(conditions); conditionIndex++ {
		if conditions[conditionIndex].Type == conditionType {
			break
		}
	}
	if conditionIndex == len(conditions) {
		conditions = append(conditions, apis.VerticalPodAutoscalerRecommendationCondition{
			Type: conditionType,
		})
	}

	condition := &conditions[conditionIndex]
	if condition.Status != conditionStatus || condition.Message != message ||
		condition.Reason != reason {
		condition.LastTransitionTime = metav1.NewTime(time.Now())
	}

	condition.Status = conditionStatus
	condition.Reason = reason
	condition.Message = message
	vpaRec.Status.Conditions = conditions
	return nil
}

// PatchVPAConditions is used to update conditions for vpa to APIServer
func PatchVPAConditions(
	ctx context.Context,
	vpaUpdater control.VPAUpdater,
	vpa *apis.KatalystVerticalPodAutoscaler,
	conditionType apis.VerticalPodAutoscalerConditionType,
	conditionStatus core.ConditionStatus,
	reason,
	message string,
) error {
	vpaNew := vpa.DeepCopy()
	if err := SetVPAConditions(vpaNew, conditionType, conditionStatus, reason, message); err != nil {
		return err
	}

	if apiequality.Semantic.DeepEqual(vpa.Status, vpaNew.Status) {
		return nil
	}

	if _, err := vpaUpdater.PatchVPAStatus(ctx, vpa, vpaNew); err != nil {
		return err
	}

	return nil
}

func UpdateVPAConditions(
	ctx context.Context,
	vpaUpdater control.VPAUpdater,
	vpa *apis.KatalystVerticalPodAutoscaler,
	conditionType apis.VerticalPodAutoscalerConditionType,
	conditionStatus core.ConditionStatus,
	reason,
	message string,
) error {
	vpaNew := vpa.DeepCopy()
	if err := SetVPAConditions(vpaNew, conditionType, conditionStatus, reason, message); err != nil {
		return err
	}

	if apiequality.Semantic.DeepEqual(vpa.Status, vpaNew.Status) {
		return nil
	}

	if _, err := vpaUpdater.UpdateVPAStatus(ctx, vpaNew, metav1.UpdateOptions{}); err != nil {
		return err
	}

	return nil
}

// PatchVPARecConditions is used to update conditions for vpaRec to APIServer
func PatchVPARecConditions(
	ctx context.Context,
	vpaRecUpdater control.VPARecommendationUpdater,
	vpaRec *apis.VerticalPodAutoscalerRecommendation,
	conditionType apis.VerticalPodAutoscalerRecommendationConditionType,
	conditionStatus core.ConditionStatus,
	reason,
	message string,
) error {
	vpaRecCopy := vpaRec.DeepCopy()
	if err := SetVPARecConditions(vpaRecCopy, conditionType, conditionStatus, reason, message); err != nil {
		return err
	}
	if apiequality.Semantic.DeepEqual(vpaRec.Status, vpaRecCopy.Status) {
		return nil
	}

	return vpaRecUpdater.PatchVPARecommendationStatus(ctx, vpaRec, vpaRecCopy)
}

// SetOwnerReferencesForVPA is used to parse from workload list, and set the
// runtime information into OwnerReference fields for vpa CR
func SetOwnerReferencesForVPA(vpa *apis.KatalystVerticalPodAutoscaler, workload runtime.Object) error {
	metadata, err := meta.Accessor(workload)
	if err != nil {
		return err
	}

	ownerRef := metav1.OwnerReference{
		Name:       metadata.GetName(),
		Kind:       workload.GetObjectKind().GroupVersionKind().Kind,
		APIVersion: workload.GetObjectKind().GroupVersionKind().GroupVersion().String(),
		UID:        metadata.GetUID(),
	}
	for _, ow := range vpa.GetOwnerReferences() {
		if reflect.DeepEqual(ow, ownerRef) {
			return nil
		}
	}

	vpa.OwnerReferences = append(vpa.OwnerReferences, ownerRef)
	return nil
}

// DeleteOwnerReferencesForVPA is used to parse from workload list, and clean up the
// runtime information from OwnerReference fields for vpa CR
func DeleteOwnerReferencesForVPA(vpa *apis.KatalystVerticalPodAutoscaler, workload runtime.Object) error {
	metadata, err := meta.Accessor(workload)
	if err != nil {
		return err
	}

	ownerRef := metav1.OwnerReference{
		Name:       metadata.GetName(),
		Kind:       workload.GetObjectKind().GroupVersionKind().Kind,
		APIVersion: workload.GetObjectKind().GroupVersionKind().GroupVersion().String(),
		UID:        metadata.GetUID(),
	}

	for i, ow := range vpa.GetOwnerReferences() {
		if reflect.DeepEqual(ow, ownerRef) {
			vpa.OwnerReferences = append(vpa.OwnerReferences[:i], vpa.OwnerReferences[i+1:]...)
			return nil
		}
	}

	return nil
}
