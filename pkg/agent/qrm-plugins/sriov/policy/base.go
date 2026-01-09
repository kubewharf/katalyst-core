/*
copyright 2022 The Katalyst Authors.

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

package policy

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/cri/remote"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/handler"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	rdmaDevicePrefix = "/dev/infiniband"
	rdmaCmPath       = "/dev/infiniband/rdma_cm"
)

// ResourceName is the resource name for sriov nic,
// we define it as a public variable so that it could be customized.
var ResourceName = string(apiconsts.ResourceSriovNic)

type basePolicy struct {
	state                 state.State
	stateReconciler       *handler.StateReconciler
	agentCtx              *agent.GenericContext
	dryRun                bool
	qosConfig             *generic.QoSConfiguration
	podAnnotationKeptKeys []string
	podLabelKeptKeys      []string
	allocationConfig      qrm.SriovAllocationConfig
	bondingHostNetwork    bool
}

func newBasePolicy(agentCtx *agent.GenericContext, conf *config.Configuration, emitter metrics.MetricEmitter) (*basePolicy, error) {
	stateImpl, err := state.NewCheckpointState(conf.QRMPluginsConfiguration, conf.MachineInfoConfiguration,
		conf.StateDirectoryConfiguration, consts.SriovPluginStateFileName, consts.SriovResourcePluginPolicyNameStatic,
		conf.SkipSriovStateCorruption, emitter)
	if err != nil {
		return nil, fmt.Errorf("NewCheckpointState failed with error: %v", err)
	}

	runtimeClient, err := remote.NewRemoteRuntimeService(conf.BaseConfiguration.RuntimeEndpoint, 2*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("create remote runtime service failed %s", err)
	}

	stateReconciler := handler.NewStateReconciler(stateImpl, conf.SriovAllocationConfig.PCIAnnotation,
		ResourceName, agentCtx.Client.KubeClient, runtimeClient)

	bondingHostNetwork, err := machine.IsHostNetworkBonding()
	if err != nil {
		return nil, fmt.Errorf("IsHostNetworkBonding failed with error: %v", err)
	}
	general.Infof("detect host network bonding=%t", bondingHostNetwork)

	return &basePolicy{
		state:                 stateImpl,
		stateReconciler:       stateReconciler,
		agentCtx:              agentCtx,
		dryRun:                conf.SriovDryRun,
		allocationConfig:      conf.SriovAllocationConfig,
		qosConfig:             conf.QoSConfiguration,
		podAnnotationKeptKeys: conf.PodAnnotationKeptKeys,
		podLabelKeptKeys:      conf.PodLabelKeptKeys,
		bondingHostNetwork:    bondingHostNetwork,
	}, nil
}

func (p *basePolicy) validateRequestQuantity(req *pluginapi.ResourceRequest) error {
	if req == nil {
		return fmt.Errorf("got nil req")
	}

	quantity, exists := req.ResourceRequests[ResourceName]
	if !exists {
		return fmt.Errorf("request quantity for resource %s not found", ResourceName)
	}

	if quantity != 1 {
		return fmt.Errorf("not support request exceeding 1 sriov nic: %f", quantity)
	}

	return nil
}

func (p *basePolicy) packResourceAllocationInfo(allocationInfo *state.AllocationInfo) (*pluginapi.ResourceAllocationInfo, error) {
	annotations := general.DeepCopyMap(p.allocationConfig.ExtraAnnotations)
	pciAnnotationValue, err := json.Marshal([]types.PCIDevice{
		{
			Address: allocationInfo.VFInfo.PCIAddr,
			RepName: allocationInfo.VFInfo.RepName,
			VFName:  allocationInfo.VFInfo.Name,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal pci device: %v", err)
	}
	annotations[p.allocationConfig.PCIAnnotation] = string(pciAnnotationValue)

	var devices []*pluginapi.DeviceSpec
	if ibDevices := allocationInfo.VFInfo.IBDevices; len(ibDevices) > 0 {
		for _, device := range ibDevices {
			rdmaPath := filepath.Join(rdmaDevicePrefix, device)
			devices = append(devices, &pluginapi.DeviceSpec{
				HostPath:      rdmaPath,
				ContainerPath: rdmaPath,
				Permissions:   "rwm",
			})
		}
		devices = append(devices, &pluginapi.DeviceSpec{
			HostPath:      rdmaCmPath,
			ContainerPath: rdmaCmPath,
			Permissions:   "rw",
		})
	}

	return &pluginapi.ResourceAllocationInfo{
		IsNodeResource:    true,
		IsScalarResource:  true,
		AllocatedQuantity: 1,
		Annotations:       annotations,
		Devices:           devices,
	}, nil
}

func (p *basePolicy) packAllocationResponse(req *pluginapi.ResourceRequest,
	allocationInfo *state.AllocationInfo) (*pluginapi.ResourceAllocationResponse, error) {
	resourceAllocationInfo, err := p.packResourceAllocationInfo(allocationInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to pack resource allocation info: %v", err)
	}

	return &pluginapi.ResourceAllocationResponse{
		PodUid:         req.PodUid,
		PodNamespace:   req.PodNamespace,
		PodName:        req.PodName,
		ContainerName:  req.ContainerName,
		ContainerType:  req.ContainerType,
		ContainerIndex: req.ContainerIndex,
		PodRole:        req.PodRole,
		PodType:        req.PodType,
		ResourceName:   req.ResourceName,
		AllocationResult: &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				ResourceName: resourceAllocationInfo,
			},
		},
		Labels:      general.DeepCopyMap(req.Labels),
		Annotations: general.DeepCopyMap(req.Annotations),
	}, nil
}
