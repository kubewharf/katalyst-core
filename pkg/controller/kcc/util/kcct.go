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
	"sort"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apisv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

func IsCNCUpdated(
	cnc *apisv1alpha1.CustomNodeConfig,
	gvr metav1.GroupVersionResource,
	targetResource util.KCCTargetResource,
	hash string,
) bool {
	for _, targetConfig := range cnc.Status.KatalystCustomConfigList {
		if targetConfig.ConfigType == gvr &&
			targetConfig.ConfigNamespace == targetResource.GetNamespace() &&
			targetConfig.ConfigName == targetResource.GetName() &&
			targetConfig.Hash == hash {
			return true
		}
	}
	return false
}

// ApplyKCCTargetConfigToCNC sets the hash value for the given configurations in CNC
func ApplyKCCTargetConfigToCNC(
	cnc *apisv1alpha1.CustomNodeConfig,
	gvr metav1.GroupVersionResource,
	targetResource util.KCCTargetResource,
	hash string,
) {
	idx := 0
	katalystCustomConfigList := cnc.Status.KatalystCustomConfigList
	// find target config
	for ; idx < len(katalystCustomConfigList); idx++ {
		if katalystCustomConfigList[idx].ConfigType == gvr {
			break
		}
	}

	// update target config if the gvr is already existed, otherwise append it and sort
	if idx < len(katalystCustomConfigList) {
		katalystCustomConfigList[idx] = apisv1alpha1.TargetConfig{
			ConfigType:      gvr,
			ConfigNamespace: targetResource.GetNamespace(),
			ConfigName:      targetResource.GetName(),
			Hash:            hash,
		}
	} else {
		katalystCustomConfigList = append(katalystCustomConfigList, apisv1alpha1.TargetConfig{
			ConfigType:      gvr,
			ConfigNamespace: targetResource.GetNamespace(),
			ConfigName:      targetResource.GetName(),
			Hash:            hash,
		})

		cnc.Status.KatalystCustomConfigList = katalystCustomConfigList
		sort.SliceStable(katalystCustomConfigList, func(i, j int) bool {
			return katalystCustomConfigList[i].ConfigType.String() < katalystCustomConfigList[j].ConfigType.String()
		})
	}
}

// FindMatchedKCCTargetConfigForNode finds the KCC target that should be applied to this CNC.
// The KCCTs passed in must be non-terminating and updated.
// The rule of cnc match to config is:
// 1. if there is only one matched nodeNames config, return it
// 2. if there is only one matched labelSelector config with the highest priority, return it
// 3. if there is only one global config (neither nodeNames nor labelSelector exists), return it
// 4. otherwise, return nil to keep current state no changed
func FindMatchedKCCTargetConfigForNode(
	cnc *apisv1alpha1.CustomNodeConfig,
	kccTargetList []util.KCCTargetResource,
) (*util.KCCTargetResource, error) {
	kccTargetList, err := findMatchedKCCTargetListForNode(cnc, kccTargetList)
	if err != nil {
		return nil, err
	}

	if len(kccTargetList) == 0 {
		return nil, fmt.Errorf("matched kcc target config not found")
	} else if len(kccTargetList) > 1 {
		sort.SliceStable(kccTargetList, func(i, j int) bool {
			return kccTargetList[i].GetPriority() > kccTargetList[j].GetPriority()
		})

		if kccTargetList[0].GetPriority() == kccTargetList[1].GetPriority() {
			return nil, fmt.Errorf("more than one kcc target config found with same priority")
		}
	}

	return &kccTargetList[0], nil
}

// findMatchedKCCTargetListForNode gets the matched kcc targets for the given cnc
func findMatchedKCCTargetListForNode(
	cnc *apisv1alpha1.CustomNodeConfig,
	kccTargetList []util.KCCTargetResource,
) ([]util.KCCTargetResource, error) {
	var matchedNodeNameConfigs, matchedLabelSelectorConfigs, matchedGlobalConfigs []util.KCCTargetResource
	for _, targetResource := range kccTargetList {
		nodeNames := targetResource.GetNodeNames()
		labelSelector := targetResource.GetLabelSelector()
		if len(nodeNames) > 0 {
			if sets.NewString(nodeNames...).Has(cnc.GetName()) {
				matchedNodeNameConfigs = append(matchedNodeNameConfigs, targetResource)
			}
			continue
		}

		if labelSelector != "" {
			selector, err := labels.Parse(labelSelector)
			if err != nil {
				// if some label selector config parse failed, we make all label selector config invalid
				matchedLabelSelectorConfigs = append(matchedLabelSelectorConfigs, targetResource)
				continue
			}

			cncLabels := cnc.GetLabels()
			if selector.Matches(labels.Set(cncLabels)) {
				matchedLabelSelectorConfigs = append(matchedLabelSelectorConfigs, targetResource)
			}
			continue
		}

		matchedGlobalConfigs = append(matchedGlobalConfigs, targetResource)
	}

	if len(matchedNodeNameConfigs) > 0 {
		return matchedNodeNameConfigs, nil
	} else if len(matchedLabelSelectorConfigs) > 0 {
		return matchedLabelSelectorConfigs, nil
	} else if len(matchedGlobalConfigs) > 0 {
		return matchedGlobalConfigs, nil
	}

	return nil, nil
}

// UpdateKCCTGenericConditions is used to update conditions for kcc
func UpdateKCCTGenericConditions(status *apisv1alpha1.GenericConfigStatus, conditionType apisv1alpha1.ConfigConditionType,
	conditionStatus v1.ConditionStatus, reason, message string,
) bool {
	var (
		updated bool
		found   bool
	)

	conditions := status.Conditions
	for idx := range conditions {
		if conditions[idx].Type == conditionType {
			if conditions[idx].Status != conditionStatus {
				conditions[idx].Status = conditionStatus
				conditions[idx].LastTransitionTime = metav1.NewTime(time.Now())
				updated = true
			}
			if conditions[idx].Reason != reason {
				conditions[idx].Reason = reason
				updated = true
			}
			if conditions[idx].Message != message {
				conditions[idx].Message = message
				updated = true
			}
			found = true
			break
		}
	}

	if !found {
		conditions = append(conditions, apisv1alpha1.GenericConfigCondition{
			Type:               conditionType,
			Status:             conditionStatus,
			LastTransitionTime: metav1.NewTime(time.Now()),
			Reason:             reason,
			Message:            message,
		})
		status.Conditions = conditions
		updated = true
	}

	return updated
}

// EnsureKCCTargetFinalizer is used to add finalizer in kcc target
// any component (that depends on kcc target) should add a specific finalizer in the target CR
func EnsureKCCTargetFinalizer(ctx context.Context, unstructuredControl control.UnstructuredControl, finalizerName string,
	gvr metav1.GroupVersionResource, target *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var err, getErr error
		if controllerutil.ContainsFinalizer(target, finalizerName) {
			return nil
		}

		controllerutil.AddFinalizer(target, finalizerName)
		newTarget, err := unstructuredControl.UpdateUnstructured(ctx, gvr, target, metav1.UpdateOptions{})
		if errors.IsConflict(err) {
			newTarget, getErr = unstructuredControl.GetUnstructured(ctx, gvr, target.GetNamespace(), target.GetName(), metav1.GetOptions{ResourceVersion: "0"})
			if getErr != nil {
				return getErr
			}
		}

		target = newTarget
		return err
	})
	if err != nil {
		return nil, err
	}

	return target, nil
}

// RemoveKCCTargetFinalizers removes finalizer(s) in kcc target
func RemoveKCCTargetFinalizers(
	ctx context.Context,
	unstructuredControl control.UnstructuredControl,
	gvr metav1.GroupVersionResource,
	target *unstructured.Unstructured,
	finalizers ...string,
) error {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var err, getErr error

		oldFinalizers := target.GetFinalizers()
		newFinalizers := sets.NewString(oldFinalizers...).Difference(sets.NewString(finalizers...)).List()
		if len(oldFinalizers) == len(newFinalizers) {
			return nil
		}

		target.SetFinalizers(newFinalizers)
		newTarget, err := unstructuredControl.UpdateUnstructured(ctx, gvr, target, metav1.UpdateOptions{})
		if errors.IsConflict(err) {
			newTarget, getErr = unstructuredControl.GetUnstructured(ctx, gvr, target.GetNamespace(), target.GetName(), metav1.GetOptions{ResourceVersion: "0"})
			if getErr != nil {
				return getErr
			}
		}

		target = newTarget
		return err
	})

	return err
}
