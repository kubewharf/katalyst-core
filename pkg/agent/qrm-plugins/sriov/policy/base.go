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
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	metricTagDryRunTagKey = "dryRun"

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
	machineInfoConf       *global.MachineInfoConfiguration
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

	stateReconciler := handler.NewStateReconciler(stateImpl, conf.SriovAllocationConfig.PCIAnnotationKey,
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
		machineInfoConf:       conf.MachineInfoConfiguration,
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

func (p *basePolicy) packResourceHintsResponse(req *pluginapi.ResourceRequest, resourceName string,
	resourceHints map[string]*pluginapi.ListOfTopologyHints,
) *pluginapi.ResourceHintsResponse {
	return &pluginapi.ResourceHintsResponse{
		PodUid:         req.PodUid,
		PodNamespace:   req.PodNamespace,
		PodName:        req.PodName,
		ContainerName:  req.ContainerName,
		ContainerType:  req.ContainerType,
		ContainerIndex: req.ContainerIndex,
		PodRole:        req.PodRole,
		PodType:        req.PodType,
		ResourceName:   resourceName,
		ResourceHints:  resourceHints,
		Labels:         general.DeepCopyMap(req.Labels),
		Annotations:    general.DeepCopyMap(req.Annotations),
		NativeQosClass: req.NativeQosClass,
	}
}

func (p *basePolicy) generateResourceAllocationInfo(allocationInfo *state.AllocationInfo) (*pluginapi.ResourceAllocationInfo, error) {
	annotations := general.DeepCopyMap(p.allocationConfig.ExtraAnnotations)

	pciDevice := types.PCIDevice{
		Address: allocationInfo.VFInfo.PCIAddr,
		RepName: allocationInfo.VFInfo.RepName,
	}

	var devices []*pluginapi.DeviceSpec
	if allocationInfo.VFInfo.ExtraVFInfo != nil {
		pciDevice.VFName = allocationInfo.VFInfo.ExtraVFInfo.Name
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
	}

	pciAnnotationValue, err := json.Marshal([]types.PCIDevice{pciDevice})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal pci device: %v", err)
	}
	annotations[p.allocationConfig.PCIAnnotationKey] = string(pciAnnotationValue)

	if allocationInfo.VFInfo.NSName != machine.DefaultNICNamespace {
		annotations[p.allocationConfig.NetNsAnnotationKey] = filepath.Join(
			p.machineInfoConf.NetNSDirAbsPath, allocationInfo.VFInfo.NSName,
		)
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
	resourceAllocationInfo *pluginapi.ResourceAllocationInfo) *pluginapi.ResourceAllocationResponse {
	resp := &pluginapi.ResourceAllocationResponse{
		PodUid:         req.PodUid,
		PodNamespace:   req.PodNamespace,
		PodName:        req.PodName,
		ContainerName:  req.ContainerName,
		ContainerType:  req.ContainerType,
		ContainerIndex: req.ContainerIndex,
		PodRole:        req.PodRole,
		PodType:        req.PodType,
		ResourceName:   req.ResourceName,
		Labels:         general.DeepCopyMap(req.Labels),
		Annotations:    general.DeepCopyMap(req.Annotations),
	}

	if resourceAllocationInfo != nil {
		resp.AllocationResult = &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				ResourceName: resourceAllocationInfo,
			},
		}
	}

	return resp
}
