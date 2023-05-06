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
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const (
	// NetworkResourcePluginPolicyNameDynamic is the policy name of dynamic network resource plugin
	NetworkResourcePluginPolicyNameDynamic = "dynamic"
	// ResourceNameNetwork is the resource name of network
	ResourceNameNetwork = "network"
)

// DynamicPolicy is the dynamic network policy
type DynamicPolicy struct {
	sync.RWMutex

	name       string
	stopCh     chan struct{}
	started    bool
	qosConfig  *generic.QoSConfiguration
	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer
	nics       []*NetworkInterface

	CgroupV2Env                   bool
	netClassMap                   map[string]uint32
	applyNetClassFunc             func(podUID, containerID string, data *common.NetClsData) error
	podLevelNetClassAnnoKey       string
	podLevelNetAttributesAnnoKeys []string
}

// NewDynamicPolicy returns a dynamic network policy
func NewDynamicPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string) (bool, agent.Component, error) {
	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: NetworkResourcePluginPolicyNameDynamic,
	})

	policyImplement := &DynamicPolicy{
		qosConfig:   conf.QoSConfiguration,
		emitter:     wrappedEmitter,
		metaServer:  agentCtx.MetaServer,
		stopCh:      make(chan struct{}),
		name:        fmt.Sprintf("%s_%s", agentName, NetworkResourcePluginPolicyNameDynamic),
		netClassMap: make(map[string]uint32),
	}

	if common.CheckCgroup2UnifiedMode() {
		policyImplement.CgroupV2Env = true
		policyImplement.applyNetClassFunc = agentCtx.MetaServer.ExternalManager.ApplyNetClass
	} else {
		policyImplement.CgroupV2Env = false
		policyImplement.applyNetClassFunc = cgroupcmutils.ApplyNetClsForContainer
	}

	policyImplement.ApplyConfig(conf.DynamicConfiguration)

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(
		policyImplement,
		conf.QRMPluginSocketDirs, nil)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy new plugin wrapper failed with error: %v", err)
	}

	return true, &agent.PluginWrapper{GenericPlugin: pluginWrapper}, nil
}

// ApplyConfig applies config to DynamicPolicy
func (p *DynamicPolicy) ApplyConfig(conf *config.DynamicConfiguration) {
	p.Lock()
	defer p.Unlock()

	p.netClassMap[apiconsts.PodAnnotationQoSLevelReclaimedCores] = conf.NetClass.ReclaimedCores
	p.netClassMap[apiconsts.PodAnnotationQoSLevelSharedCores] = conf.NetClass.SharedCores
	p.netClassMap[apiconsts.PodAnnotationQoSLevelDedicatedCores] = conf.NetClass.DedicatedCores
	p.netClassMap[apiconsts.PodAnnotationQoSLevelSystemCores] = conf.NetClass.SystemCores

	p.podLevelNetClassAnnoKey = conf.PodLevelNetClassAnnoKey
	p.podLevelNetAttributesAnnoKeys = strings.Split(conf.PodLevelNetAttributesAnnoKeys, ",")

	general.Infof("apply configs, "+
		"netClassMap: %+v, "+
		"podLevelNetClassAnnoKey: %s, "+
		"podLevelNetAttributesAnnoKeys: %+v",
		p.netClassMap,
		p.podLevelNetClassAnnoKey,
		p.podLevelNetAttributesAnnoKeys)
}

// Start starts this plugin
func (p *DynamicPolicy) Start() (err error) {
	general.Infof("called")

	p.Lock()
	defer func() {
		if err == nil {
			p.started = true
		}
		p.Unlock()
	}()

	if p.started {
		general.Infof("already started")
		return nil
	}
	p.stopCh = make(chan struct{})

	go wait.Until(func() {
		_ = p.emitter.StoreInt64(util.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
	}, time.Second*30, p.stopCh)
	go wait.Until(p.applyNetClass, 5*time.Second, p.stopCh)

	return nil
}

// Stop stops this plugin
func (p *DynamicPolicy) Stop() error {
	p.Lock()
	defer func() {
		p.started = false
		p.Unlock()
		general.Infof("stopped")
	}()

	if !p.started {
		general.Warningf("already stopped")
		return nil
	}
	close(p.stopCh)
	return nil
}

// Name returns the name of this plugin
func (p *DynamicPolicy) Name() string {
	return p.name
}

// ResourceName returns resource names managed by this plugin
func (p *DynamicPolicy) ResourceName() string {
	return ResourceNameNetwork
}

// GetTopologyHints returns hints of corresponding resources
func (p *DynamicPolicy) GetTopologyHints(_ context.Context,
	_ *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	return &pluginapi.ResourceHintsResponse{}, nil
}

func (p *DynamicPolicy) RemovePod(_ context.Context,
	req *pluginapi.RemovePodRequest) (*pluginapi.RemovePodResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}

	if p.CgroupV2Env {
		if err := p.removePod(req.PodUid); err != nil {
			general.ErrorS(err, "remove pod failed with error", "podUID", req.PodUid)
			return nil, err
		}
	}

	return &pluginapi.RemovePodResponse{}, nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *DynamicPolicy) GetResourcesAllocation(_ context.Context,
	_ *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error) {
	return &pluginapi.GetResourcesAllocationResponse{}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as topology aware format
func (p *DynamicPolicy) GetTopologyAwareResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareResourcesRequest) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	return &pluginapi.GetTopologyAwareResourcesResponse{}, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as topology aware format
func (p *DynamicPolicy) GetTopologyAwareAllocatableResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareAllocatableResourcesRequest) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{}, nil
}

// GetResourcePluginOptions returns options to be communicated with Resource Manager
func (p *DynamicPolicy) GetResourcePluginOptions(context.Context,
	*pluginapi.Empty) (*pluginapi.ResourcePluginOptions, error) {
	return &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: false,
		NeedReconcile:         false,
	}, nil
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
func (p *DynamicPolicy) Allocate(_ context.Context,
	req *pluginapi.ResourceRequest) (resp *pluginapi.ResourceAllocationResponse, respErr error) {
	return &pluginapi.ResourceAllocationResponse{
		PodUid:         req.PodUid,
		PodNamespace:   req.PodNamespace,
		PodName:        req.PodName,
		ContainerName:  req.ContainerName,
		ContainerType:  req.ContainerType,
		ContainerIndex: req.ContainerIndex,
		PodRole:        req.PodRole,
		PodType:        req.PodType,
		ResourceName:   ResourceNameNetwork,
		AllocationResult: &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				ResourceNameNetwork: {},
			},
		},
		Labels:      general.DeepCopyMap(req.Labels),
		Annotations: general.DeepCopyMap(req.Annotations),
	}, nil
}

// PreStartContainer is called, if indicated by resource plugin during registration phase,
// before each container start. Resource plugin can run resource specific operations
// such as resetting the resource before making resources available to the container
func (p *DynamicPolicy) PreStartContainer(context.Context,
	*pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}

func (p *DynamicPolicy) applyNetClass() {
	if p.metaServer == nil {
		general.Errorf("nil metaServer")
		return
	}

	podList, err := p.metaServer.GetPodList(context.Background(), nil)
	if err != nil {
		general.Errorf("get pod list failed, err: %v", err)
		return
	}

	for _, pod := range podList {
		classID, err := p.getNetClassID(pod, p.podLevelNetClassAnnoKey)
		if err != nil {
			general.Errorf("get net class id failed, pod: %s, err: %s", native.GenerateUniqObjectNameKey(pod), err)
			continue
		}
		netClsData := &common.NetClsData{
			ClassID:    classID,
			Attributes: native.FilterPodAnnotations(p.podLevelNetAttributesAnnoKeys, pod),
		}

		for _, container := range pod.Spec.Containers {
			go func(podUID, containerName string, netClsData *common.NetClsData) {
				containerID, err := p.metaServer.GetContainerID(podUID, containerName)
				if err != nil {
					general.Errorf("get container id failed, pod: %s, container: %s(%s), err: %v",
						podUID, containerName, containerID, err)
					return
				}

				if exist, err := common.IsContainerCgroupExist(podUID, containerID); err != nil {
					general.Errorf("check if container cgroup exists failed, pod: %s, container: %s(%s), err: %v",
						podUID, containerName, containerID, err)
					return
				} else if !exist {
					general.Infof("container cgroup does not exist, pod: %s, container: %s(%s)", podUID, containerName, containerID)
					return
				}

				if p.CgroupV2Env {
					cgID, err := p.metaServer.ExternalManager.GetCgroupIDForContainer(podUID, containerID)
					if err != nil {
						general.Errorf("get cgroup id failed, pod: %s, container: %s(%s), err: %v",
							podUID, containerName, containerID, err)
						return
					}
					netClsData.CgroupID = cgID
				}

				if err = p.applyNetClassFunc(podUID, containerID, netClsData); err != nil {
					general.Errorf("apply net class failed, pod: %s, container: %s(%s), netClsData: %+v, err: %v",
						podUID, containerName, containerID, *netClsData, err)
					return
				}

				general.Infof("apply net class successfully, pod: %s, container: %s(%s), netClsData: %+v",
					podUID, containerName, containerID, *netClsData)
			}(string(pod.UID), container.Name, netClsData)
		}
	}
}

func (p *DynamicPolicy) removePod(podUID string) error {
	cgIDList, err := p.metaServer.ExternalManager.ListCgroupIDsForPod(podUID)
	if err != nil {
		return fmt.Errorf("[NetworkDynamicPolicy.removePod] list cgroup ids of pod: %s failed with error: %v", podUID, err)
	}

	for _, cgID := range cgIDList {
		go func(cgID uint64) {
			if err := p.metaServer.ExternalManager.ClearNetClass(cgID); err != nil {
				general.Errorf("delete net class failed, cgID: %v, err: %v", cgID, err)
				return
			}
		}(cgID)
	}

	return nil
}

func (p *DynamicPolicy) getNetClassID(pod *v1.Pod, podLevelNetClassAnnoKey string) (uint32, error) {
	isPodLevelNetClassExist, classID, err := qos.GetPodNetClassID(pod, podLevelNetClassAnnoKey)
	if err != nil {
		return 0, err
	}
	if isPodLevelNetClassExist {
		return classID, nil
	}

	qosClass, err := p.qosConfig.GetQoSLevelForPod(pod)
	if err != nil {
		return 0, err
	}
	return p.getNetClassIDByQoS(qosClass), nil
}

func (p *DynamicPolicy) getNetClassIDByQoS(qosClass string) uint32 {
	p.RLock()
	defer p.RUnlock()

	return p.netClassMap[qosClass]
}
