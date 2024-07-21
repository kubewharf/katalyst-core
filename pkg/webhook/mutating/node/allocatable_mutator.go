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

package node

import (
	"fmt"

	overcommitutil "github.com/kubewharf/katalyst-core/pkg/util/overcommit"

	admissionv1beta1 "k8s.io/api/admission/v1beta1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	nodeAllocatableMutatorName = "nodeAllocatableMutator"
)

// WebhookNodeAllocatableMutator mutate node allocatable according to overcommit annotation
type WebhookNodeAllocatableMutator struct{}

func NewWebhookNodeAllocatableMutator() *WebhookNodeAllocatableMutator {
	return &WebhookNodeAllocatableMutator{}
}

func (na *WebhookNodeAllocatableMutator) Name() string {
	return nodeAllocatableMutatorName
}

func (na *WebhookNodeAllocatableMutator) MutateNode(node *core.Node, admissionRequest *admissionv1beta1.AdmissionRequest) error {
	if admissionv1beta1.Update != admissionRequest.Operation || admissionRequest.SubResource != "status" {
		return nil
	}
	node.Annotations = updateNodeAnnotations(node.Annotations, node.Status.Capacity, node.Status.Allocatable)

	// process cpu
	if err := processResource(node, core.ResourceCPU, consts.NodeAnnotationCPUOvercommitRatioKey, consts.NodeAnnotationOvercommitCapacityCPUKey, consts.NodeAnnotationOvercommitAllocatableCPUKey); err != nil {
		klog.Error(err)
	}

	// process memory
	if err := processResource(node, core.ResourceMemory, consts.NodeAnnotationMemoryOvercommitRatioKey, consts.NodeAnnotationOvercommitCapacityMemoryKey, consts.NodeAnnotationOvercommitAllocatableMemoryKey); err != nil {
		klog.Error(err)
	}

	return nil
}

func updateNodeAnnotations(annotations map[string]string, capacity, allocatable core.ResourceList) map[string]string {
	if annotations == nil {
		annotations = make(map[string]string)
	}

	annotations[consts.NodeAnnotationOriginalCapacityCPUKey] = capacity.Cpu().String()
	annotations[consts.NodeAnnotationOriginalCapacityMemoryKey] = capacity.Memory().String()
	annotations[consts.NodeAnnotationOriginalAllocatableCPUKey] = allocatable.Cpu().String()
	annotations[consts.NodeAnnotationOriginalAllocatableMemoryKey] = allocatable.Memory().String()

	return annotations
}

// processResource Update the resource information of the node according to the specified key-value pair
func processResource(node *core.Node, rn core.ResourceName, ratioKey, capacityKey, allocatableKey string) error {
	ratioValue, ok := node.Annotations[ratioKey]
	if ok {
		var newAllocatable, newCapacity *resource.Quantity
		// get overcommit allocatable and capacity from annotation first
		capacity, ok := node.Annotations[capacityKey]
		if ok {
			quantity, err := resource.ParseQuantity(capacity)
			if err != nil {
				klog.Error(err)
			} else {
				newCapacity = &quantity
				klog.V(6).Infof("node %s %s capacity by annotation: %v", node.Name, newCapacity.String(), newCapacity.String())
			}
		}

		allocatable, ok := node.Annotations[allocatableKey]
		if ok {
			quantity, err := resource.ParseQuantity(allocatable)
			if err != nil {
				klog.Error(err)
			} else {
				newAllocatable = &quantity
				klog.V(6).Infof("node %s %s allocatable by annotation: %v", node.Name, newCapacity.String(), newAllocatable.String())

			}
		}

		// calculate allocatable and capacity by overcommit ratio
		if newAllocatable == nil || newCapacity == nil {
			ratio, err := validateOvercommitRatio(node.Annotations, ratioKey)
			if err != nil {
				return fmt.Errorf("node %s %s validate fail, value: %s, err: %v", node.Name, ratioKey, ratioValue, err)
			} else {
				if ratio > 1.0 {
					allocatable := node.Status.Allocatable[rn]
					capacity := node.Status.Capacity[rn]
					allocatableByOvercommit := native.MultiplyResourceQuantity(rn, allocatable, ratio)
					capacityByOvercommit := native.MultiplyResourceQuantity(rn, capacity, ratio)
					newAllocatable = &allocatableByOvercommit
					newCapacity = &capacityByOvercommit
					klog.V(6).Infof("node %s %s capacity: %v, allocatable: %v, newCapacity: %v, newAllocatable: %v",
						node.Name, newCapacity,
						capacity.String(), newCapacity.String(),
						allocatable.String(), newAllocatable.String())
				}
			}
		}

		if newAllocatable != nil && newCapacity != nil {
			node.Status.Allocatable[rn] = *newAllocatable
			node.Status.Capacity[rn] = *newCapacity
		}
	}
	return nil
}

func validateOvercommitRatio(nodeAnnotation map[string]string, ratioKey string) (float64, error) {
	var realtimeRatioKey string
	if ratioKey == consts.NodeAnnotationCPUOvercommitRatioKey {
		realtimeRatioKey = consts.NodeAnnotationRealtimeCPUOvercommitRatioKey
	} else if ratioKey == consts.NodeAnnotationMemoryOvercommitRatioKey {
		realtimeRatioKey = consts.NodeAnnotationRealtimeMemoryOvercommitRatioKey
	} else {
		return 0, fmt.Errorf("invalid realtimeRatioKey value")
	}
	return overcommitutil.OvercommitRatioValidate(
		nodeAnnotation,
		ratioKey,
		realtimeRatioKey,
	)
}
