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

package dynamicpolicy

import (
	"fmt"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/util"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

func addAllocationInfoToResponse(conf qrmconfig.SriovAllocationConfig, allocationInfo *state.AllocationInfo, resp *pluginapi.ResourceAllocationResponse) error {
	resourceAllocationInfo, err := util.PackResourceAllocationInfo(conf, allocationInfo)
	if err != nil {
		return fmt.Errorf("PackResourceAllocationInfo failed with error: %v", err)
	}
	resp.AllocationResult.ResourceAllocation[string(apiconsts.ResourceSriovNic)] = resourceAllocationInfo
	return nil
}

func getVFQueueCountAndExhaustionStrategy(conf qrmconfig.SriovDynamicPolicyConfig, request int) (int, bool) {
	switch {
	case request >= conf.LargeSizeVFCPUThreshold:
		return conf.LargeSizeVFQueueCount, conf.LargeSizeVFFailOnExhaustion
	case request >= conf.SmallSizeVFCPUThreshold:
		return conf.SmallSizeVFQueueCount, conf.SmallSizeVFFailOnExhaustion
	}
	return -1, false
}
