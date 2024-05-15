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

package config

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	"k8s.io/kubernetes/pkg/features"

	"github.com/kubewharf/katalyst-api/pkg/consts"
)

// CheckFeatureGateEnable returns true if all the given features are enabled
func CheckFeatureGateEnable(kubeletConfig *kubeletconfigv1beta1.KubeletConfiguration, features ...string) (bool, error) {
	if kubeletConfig == nil {
		return false, fmt.Errorf("nil KubeletConfiguration")
	}
	for _, feature := range features {
		if enabled, ok := kubeletConfig.FeatureGates[feature]; !ok || !enabled {
			return false, nil
		}
	}
	return true, nil
}

// GetReservedQuantity the quantity for reserved resources defined in KubeletConfiguration
func GetReservedQuantity(kubeletConfig *kubeletconfigv1beta1.KubeletConfiguration, resourceName string) (resource.Quantity, bool, error) {
	if kubeletConfig == nil {
		return resource.MustParse("0"), false, fmt.Errorf("nil KubeletConfiguration")
	}

	found := false
	reservedQuantity := resource.NewQuantity(0, resource.DecimalSI)

	if kubeletConfig.KubeReserved != nil {
		kubeReserved, err := resource.ParseQuantity(kubeletConfig.KubeReserved[resourceName])
		if err != nil {
			return resource.MustParse("0"), false,
				fmt.Errorf("failed because parse cpu quantity for kube-reserved failed with error: %v", err)
		}
		reservedQuantity.Add(kubeReserved)
		found = true
	}
	if kubeletConfig.SystemReserved != nil {
		systemReserved, err := resource.ParseQuantity(kubeletConfig.SystemReserved[resourceName])
		if err != nil {
			return resource.MustParse("0"), false,
				fmt.Errorf("parse cpu quantity for system-reserved failed with error: %v", err)
		}
		reservedQuantity.Add(systemReserved)
		found = true
	}

	return *reservedQuantity, found, nil
}

// GetInTreeProviderPolicies returns a map containing the policy for in-tree
// topology-hint-provider, i.e. cpu-manager && memory-manager
func GetInTreeProviderPolicies(kubeletConfig *kubeletconfigv1beta1.KubeletConfiguration) (map[string]string, error) {
	if kubeletConfig == nil {
		return map[string]string{}, fmt.Errorf("nil KubeletConfiguration")
	}

	klog.V(5).Infof("GetProviderPolicies featureGates: %v, cpuManagerPolicy: %v, memoryManagerPolicy: %v",
		kubeletConfig.FeatureGates, features.CPUManager, features.MemoryManager)

	res := map[string]string{
		consts.KCNRAnnotationCPUManager:    string(consts.CPUManagerOff),
		consts.KCNRAnnotationMemoryManager: string(consts.MemoryManagerOff),
	}

	on, ok := kubeletConfig.FeatureGates[string(features.CPUManager)]
	if (ok && on) || (!ok) {
		if kubeletConfig.CPUManagerPolicy != "" {
			res[consts.KCNRAnnotationCPUManager] = kubeletConfig.CPUManagerPolicy
		}
	}

	on, ok = kubeletConfig.FeatureGates[string(features.MemoryManager)]
	if (ok && on) || (!ok) {
		if kubeletConfig.MemoryManagerPolicy != "" {
			res[consts.KCNRAnnotationMemoryManager] = kubeletConfig.MemoryManagerPolicy
		}
	}

	return res, nil
}
