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

package manager

import (
	"fmt"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// AllocateDevicesUsingStrategy performs device allocation using the strategy framework
func AllocateDevicesUsingStrategy(
	resourceReq *pluginapi.ResourceRequest,
	deviceReq *pluginapi.DeviceRequest,
	deviceTopologyRegistry *machine.DeviceTopologyRegistry,
	gpuConfig *qrm.GPUQRMPluginConfig,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	machineState state.AllocationResourcesMap,
	qosLevel, resourceName, accompanyResourceName string,
) (*allocate.AllocationResult, error) {
	// Get hint nodes
	hintNodes, err := machine.NewCPUSetUint64(deviceReq.GetHint().GetNodes()...)
	if err != nil {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("failed to get hint nodes: %v", err),
		}, err
	}

	if resourceName == "" {
		resourceName = consts.GPUDeviceType
	}

	// Create allocation context
	ctx := &allocate.AllocationContext{
		ResourceReq:            resourceReq,
		DeviceReq:              deviceReq,
		DeviceTopologyRegistry: deviceTopologyRegistry,
		GPUQRMPluginConfig:     gpuConfig,
		Emitter:                emitter,
		MetaServer:             metaServer,
		MachineState:           machineState,
		QoSLevel:               qosLevel,
		HintNodes:              hintNodes,
		ResourceName:           resourceName,
		AccompanyResourceName:  accompanyResourceName,
	}

	// Get the global strategy manager and perform allocation
	manager := GetGlobalStrategyManager()
	return manager.AllocateUsingStrategy(ctx)
}
