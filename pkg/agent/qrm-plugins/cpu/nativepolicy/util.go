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

package nativepolicy

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	coreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func generateMachineStateFromPodEntries(topology *machine.CPUTopology, podEntries state.PodEntries) (state.NUMANodeMap, error) {
	return state.GenerateMachineStateFromPodEntries(topology, podEntries, coreconsts.CPUResourcePluginPolicyNameNative)
}

func getKubeletReservedQuantity(klConfig *kubeletconfigv1beta1.KubeletConfiguration) (resource.Quantity, error) {
	reservedQuantity := resource.NewQuantity(0, resource.DecimalSI)
	if klConfig.KubeReserved != nil {
		kubeReserved, err := resource.ParseQuantity(klConfig.KubeReserved[string(v1.ResourceCPU)])
		if err != nil {
			return resource.MustParse("0"), fmt.Errorf("getKubeletReservedQuantity failed because parse cpu quantity for kube-reserved failed with error: %v", err)
		}
		reservedQuantity.Add(kubeReserved)
	}
	if klConfig.SystemReserved != nil {
		systemReserved, err := resource.ParseQuantity(klConfig.SystemReserved[string(v1.ResourceCPU)])
		if err != nil {
			return resource.MustParse("0"), fmt.Errorf("getKubeletReservedQuantity parse cpu quantity for system-reserved failed with error: %v", err)
		}
		reservedQuantity.Add(systemReserved)
	}

	return *reservedQuantity, nil
}
