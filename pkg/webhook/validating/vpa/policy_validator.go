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

package vpa

import (
	"fmt"

	v1 "k8s.io/api/core/v1"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	katalystutil "github.com/kubewharf/katalyst-core/pkg/util/native"
)

// todo: make this configurable to support other resource types
var valuableControlledResourceMap = map[v1.ResourceName]bool{
	v1.ResourceMemory: true,
	v1.ResourceCPU:    true,
}

// WebhookVPAPolicyValidator validate:
// 1. if controlled resource are valid in vpa by checking if resource name in valuableControlledResourceMap
// 2. if MinAllowed <= MaxAllowed
type WebhookVPAPolicyValidator struct{}

func NewWebhookVPAPolicyValidator() *WebhookVPAPolicyValidator {
	return &WebhookVPAPolicyValidator{}
}

func (vp *WebhookVPAPolicyValidator) ValidateVPA(vpa *apis.KatalystVerticalPodAutoscaler) (valid bool, message string, err error) {
	if vpa == nil {
		err := fmt.Errorf("vpa is nil")
		return false, err.Error(), err
	}

	for _, containerPolicy := range vpa.Spec.ResourcePolicy.ContainerPolicies {
		for _, resource := range containerPolicy.ControlledResources {
			if !valuableControlledResourceMap[resource] {
				return false, fmt.Sprintf("%s is not a supported controlled resource", string(resource)), nil
			}
		}

		for validResource := range valuableControlledResourceMap {
			minAllowed, minExist := containerPolicy.MinAllowed[validResource]
			maxAllowed, maxExist := containerPolicy.MaxAllowed[validResource]
			if minExist && maxExist && katalystutil.IsResourceGreaterThan(minAllowed, maxAllowed) {
				return false, fmt.Sprintf("minAllowed > maxAllowed in container policy"), nil
			}
		}
	}

	return true, "", nil
}
