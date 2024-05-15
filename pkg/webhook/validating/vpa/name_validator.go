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

	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
)

// WebhookVPAPodNameContainerNameValidator validate if pod name and container name in vpa are not empty
type WebhookVPAPodNameContainerNameValidator struct{}

func NewWebhookVPAPodNameContainerNameValidator() *WebhookVPAPodNameContainerNameValidator {
	return &WebhookVPAPodNameContainerNameValidator{}
}

func (pc *WebhookVPAPodNameContainerNameValidator) ValidateVPA(vpa *apis.KatalystVerticalPodAutoscaler) (valid bool, message string, err error) {
	if vpa == nil {
		err := fmt.Errorf("vpa is nil")
		return false, err.Error(), err
	}

	for _, containerPolicy := range vpa.Spec.ResourcePolicy.ContainerPolicies {
		if containerPolicy.ContainerName == nil {
			klog.Info("container name in pod policy can't be nil")
			return false, "container name in pod policy can't be nil", nil
		}
	}

	return true, "", nil
}
