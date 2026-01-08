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

package staticpolicy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/io/handlers/dirtymem"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/io/handlers/iocost"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/io/handlers/ioweight"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	// IOResourcePluginPolicyNameStatic is the policy name of static io resource plugin
	IOResourcePluginPolicyNameStatic = string(consts.ResourcePluginPolicyNameStatic)
)

// StaticPolicy is the static io policy
type StaticPolicy struct {
	sync.Mutex
	pluginapi.UnimplementedResourcePluginServer

	name      string
	stopCh    chan struct{}
	started   bool
	qosConfig *generic.QoSConfiguration

	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer
	agentCtx   *agent.GenericContext

	enableSettingWBT      bool
	enableSettingIOWeight bool
}

// NewStaticPolicy returns a static io policy
func NewStaticPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: IOResourcePluginPolicyNameStatic,
	})

	policyImplement := &StaticPolicy{
		emitter:               wrappedEmitter,
		metaServer:            agentCtx.MetaServer,
		agentCtx:              agentCtx,
		stopCh:                make(chan struct{}),
		name:                  fmt.Sprintf("%s_%s", agentName, IOResourcePluginPolicyNameStatic),
		qosConfig:             conf.QoSConfiguration,
		enableSettingWBT:      conf.EnableSettingWBT,
		enableSettingIOWeight: conf.EnableSettingIOWeight,
	}

	// todo: currently there is no resource needed to be topology-aware and synchronously allocated in this plugin,
	// so not to wrap the plugin by RegistrationPluginWrapper and it won't be registered to QRM framework temporarily.

	return true, &agent.PluginWrapper{GenericPlugin: policyImplement}, nil
}

// Start starts this plugin
func (p *StaticPolicy) Start() (err error) {
	general.Infof("called")

	p.Lock()
	defer func() {
		if !p.started {
			if err == nil {
				p.started = true
			} else {
				close(p.stopCh)
			}
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

	if p.enableSettingIOWeight {
		err = periodicalhandler.RegisterPeriodicalHandler(qrm.QRMIOPluginPeriodicalHandlerGroupName,
			ioweight.EnableSetIOWeightPeriodicalHandlerName, ioweight.IOWeightTaskFunc, 30*time.Second)
		if err != nil {
			general.Infof("register syncIOWeight failed, err=%v", err)
		}
	}
	if p.enableSettingWBT {
		general.Infof("setWBT enabled")
		err := periodicalhandler.RegisterPeriodicalHandler(qrm.QRMIOPluginPeriodicalHandlerGroupName,
			dirtymem.EnableSetDirtyMemPeriodicalHandlerName, dirtymem.SetDirtyMem, 300*time.Second)
		if err != nil {
			general.Infof("setSockMem failed, err=%v", err)
		}
	}

	// Notice: iocost.SetIOCost will check the featuregate.
	// If conf.EnableSettingIOCost was disabled,
	// iocost.SetIOCost will disable all the io.cost related functions in host.
	general.Infof("setIOCost handler started")
	err = periodicalhandler.RegisterPeriodicalHandler(qrm.QRMIOPluginPeriodicalHandlerGroupName,
		iocost.EnableSetIOCostPeriodicalHandlerName, iocost.SetIOCost, 300*time.Second)
	if err != nil {
		general.Infof("setIOCost failed, err=%v", err)
	}

	go wait.Until(func() {
		periodicalhandler.ReadyToStartHandlersByGroup(qrm.QRMIOPluginPeriodicalHandlerGroupName)
	}, 5*time.Second, p.stopCh)
	return nil
}

// Stop stops this plugin
func (p *StaticPolicy) Stop() error {
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

	periodicalhandler.StopHandlersByGroup(qrm.QRMIOPluginPeriodicalHandlerGroupName)
	return nil
}

// Name returns the name of this plugin
func (p *StaticPolicy) Name() string {
	return p.name
}

// ResourceName returns resource names managed by this plugin
func (p *StaticPolicy) ResourceName() string {
	// todo: return correct value when there is resource needed to be topology-aware and synchronously allocated in this plugin
	return ""
}

// GetTopologyHints returns hints of corresponding resources
func (p *StaticPolicy) GetTopologyHints(_ context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceHintsResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	return util.PackResourceHintsResponse(req, p.ResourceName(), nil)
}

// GetPodTopologyHints returns hints of corresponding resources
func (p *StaticPolicy) GetPodTopologyHints(_ context.Context,
	req *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceHintsResponse, err error) {
	return nil, util.ErrNotImplemented
}

func (p *StaticPolicy) RemovePod(_ context.Context,
	req *pluginapi.RemovePodRequest,
) (*pluginapi.RemovePodResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}

	return &pluginapi.RemovePodResponse{}, nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *StaticPolicy) GetResourcesAllocation(_ context.Context,
	_ *pluginapi.GetResourcesAllocationRequest,
) (*pluginapi.GetResourcesAllocationResponse, error) {
	return &pluginapi.GetResourcesAllocationResponse{}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as topology aware format
func (p *StaticPolicy) GetTopologyAwareResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareResourcesRequest,
) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	return &pluginapi.GetTopologyAwareResourcesResponse{}, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as topology aware format
func (p *StaticPolicy) GetTopologyAwareAllocatableResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareAllocatableResourcesRequest,
) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{}, nil
}

// GetResourcePluginOptions returns options to be communicated with Resource Manager
func (p *StaticPolicy) GetResourcePluginOptions(context.Context,
	*pluginapi.Empty,
) (*pluginapi.ResourcePluginOptions, error) {
	return &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: false,
		NeedReconcile:         false,
	}, nil
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
func (p *StaticPolicy) Allocate(_ context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceAllocationResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
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
		ResourceName:   p.ResourceName(),
		Labels:         general.DeepCopyMap(req.Labels),
		Annotations:    general.DeepCopyMap(req.Annotations),
	}, nil
}

// AllocateForPod is called during pod admit so that the resource
// plugin can allocate corresponding resource for the pod
// according to resource request
func (p *StaticPolicy) AllocateForPod(_ context.Context,
	req *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceAllocationResponse, err error) {
	return nil, util.ErrNotImplemented
}

// PreStartContainer is called, if indicated by resource plugin during registration phase,
// before each container start. Resource plugin can run resource specific operations
// such as resetting the resource before making resources available to the container
func (p *StaticPolicy) PreStartContainer(context.Context,
	*pluginapi.PreStartContainerRequest,
) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}
