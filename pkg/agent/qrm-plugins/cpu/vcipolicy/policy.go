/*
Copyright 2017 The Kubernetes Authors.

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

package vcipolicy

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/vcipolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/vcipolicy/vci"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	utilkubeconfig "github.com/kubewharf/katalyst-core/pkg/util/kubelet/config"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
)

const (
	// cpuPluginStateFileName is the name of cpu plugin state file.
	cpuPluginStateFileName = "cpu_plugin_state"
)

// NativePolicy is a policy compatible with Kubernetes native semantics and is used in topology-aware scheduling scenarios.
type StaticPolicy struct {
	sync.RWMutex
	name    string
	stopCh  chan struct{}
	started bool

	emitter     metrics.MetricEmitter
	metaServer  *metaserver.MetaServer
	machineInfo *machine.KatalystMachineInfo

	topology *machine.CPUTopology
	state    state.State
	// those are parsed from configurations
	reservedCPUs machine.CPUSet
}

func NewStaticPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	general.Infof("new native policy")

	stateImpl, stateErr := state.NewCheckpointState(conf.GenericQRMPluginConfiguration.StateFileDirectory, cpuPluginStateFileName, cpuconsts.CPUResourcePluginPolicyNameStatic)
	if stateErr != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("NewCheckpointState failed with error: %v", stateErr)
	}

	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: cpuconsts.CPUResourcePluginPolicyNameStatic,
	})

	policyImplement := &StaticPolicy{
		name:        fmt.Sprintf("%s_%s", agentName, cpuconsts.CPUResourcePluginPolicyNameStatic),
		stopCh:      make(chan struct{}),
		machineInfo: agentCtx.KatalystMachineInfo,
		emitter:     wrappedEmitter,
		metaServer:  agentCtx.MetaServer,
		state:       stateImpl,
		topology:    agentCtx.CPUTopology,
	}

	if err := policyImplement.setReservedCPUs(agentCtx.CPUTopology); err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("native policy set reserved CPUs failed with error: %v", err)
	}

	err := agentCtx.MetaServer.ConfigurationManager.AddConfigWatcher(crd.AdminQoSConfigurationGVR)
	if err != nil {
		return false, nil, err
	}

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(policyImplement, conf.QRMPluginSocketDirs, nil)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("vci static policy new plugin wrapper failed with error: %v", err)
	}

	return true, &agent.PluginWrapper{GenericPlugin: pluginWrapper}, nil
}

func (p *StaticPolicy) Name() string {
	return p.name
}

func (p *StaticPolicy) ResourceName() string {
	return string(v1.ResourceCPU)
}

func (p *StaticPolicy) Start() (err error) {
	general.Infof("called")

	p.Lock()
	defer func() {
		if err == nil {
			p.started = true
		}
		p.Unlock()
	}()

	if p.started {
		general.Infof("is already started")
		return nil
	}

	if err := p.validateState(p.state); err != nil {
		general.Errorf("[cpumanager] static policy invalid state: %v, please drain node and remove policy state file", err)
		return err
	}

	p.stopCh = make(chan struct{})

	go wait.Until(func() {
		_ = p.emitter.StoreInt64(util.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
	}, time.Second*30, p.stopCh)

	return nil
}

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

	return nil
}

// GetResourcePluginOptions returns options to be communicated with Resource Manager
func (p *StaticPolicy) GetResourcePluginOptions(context.Context,
	*pluginapi.Empty,
) (*pluginapi.ResourcePluginOptions, error) {
	general.Infof("called")
	return &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: true,
		NeedReconcile:         true,
	}, nil
}

// GetTopologyHints returns hints of corresponding resources
func (p *StaticPolicy) GetTopologyHints(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceHintsResponse, err error) {
	// TODO
	return nil, nil
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
func (p *StaticPolicy) Allocate(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceAllocationResponse, respErr error) {
	if req == nil {
		return nil, fmt.Errorf("allocate got nil req")
	}

	if _, ok := p.state.GetCPUSet(req.PodUid); ok {
		general.Infof("[cpumanager] static policy: container already present in state, skipping (pod: %s)", req.PodUid)
		return &pluginapi.ResourceAllocationResponse{
			PodUid:         req.PodUid,
			PodNamespace:   req.PodNamespace,
			PodName:        req.PodName,
			ContainerName:  req.ContainerName,
			ContainerType:  req.ContainerType,
			ContainerIndex: req.ContainerIndex,
			PodRole:        req.PodRole,
			PodType:        req.PodType,
			ResourceName:   string(v1.ResourceCPU),
			Labels:         general.DeepCopyMap(req.Labels),
			Annotations:    general.DeepCopyMap(req.Annotations),
			NativeQosClass: req.NativeQosClass,
		}, nil
	}

	topo := vci.GetPodResourceSpec(req.Annotations)
	if topo == nil {
		return nil, nil
	}

	rl := topo.Resources.Limits.DeepCopy()
	rCPU, find := rl[v1.ResourceCPU]
	if !find {
		return nil, nil
	}

	milliCPU := rCPU.MilliValue()
	numCPUs := int(milliCPU / 1000)
	// container belongs in the shared pool (nothing to do; use default cpuset), pod with decimal cpu
	if milliCPU%1000 != 0 || numCPUs == 0 {
		return nil, nil
	}

	numaAlign, policyOpts, err := vci.GetCPUNUMASpec(topo.CPUInfo, numCPUs, req.Annotations)
	if err != nil {
		return nil, err
	}

	var hint []uint64
	if req.Hint != nil && len(req.Hint.Nodes) != 0 {
		hint = req.Hint.Nodes
	}

	p.Lock()
	defer p.Unlock()

	cpuset, err := p.allocateCPUs(p.state, numCPUs, hint, numaAlign, policyOpts)
	if err != nil {
		general.Errorf("[cpumanager] unable to allocate %d CPUs (pod: %s, numaAlign:%+v, error: %v)", numCPUs, req.PodUid, numaAlign, err)
		return nil, err
	}

	p.state.SetCPUSet(req.PodUid, cpuset)

	return nil, nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *StaticPolicy) GetResourcesAllocation(_ context.Context,
	req *pluginapi.GetResourcesAllocationRequest,
) (*pluginapi.GetResourcesAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetResourcesAllocation got nil req")
	}

	general.Infof("called")
	p.Lock()
	defer p.Unlock()

	podResources := make(map[string]*pluginapi.ContainerResources)
	for podUID, cpuset := range p.state.GetCPUAssignments() {
		if podResources[podUID] == nil {
			podResources[podUID] = &pluginapi.ContainerResources{}
		}

		if podResources[podUID].ContainerResources == nil {
			podResources[podUID].ContainerResources = make(map[string]*pluginapi.ResourceAllocation)
		}

		podResources[podUID].ContainerResources[podUID] = &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				string(v1.ResourceCPU): {
					OciPropertyName:  util.OCIPropertyNameCPUSetCPUs,
					IsNodeResource:   false,
					IsScalarResource: true,
					AllocationResult: cpuset.String(),
				},
			},
		}
	}

	return &pluginapi.GetResourcesAllocationResponse{
		PodResources: podResources,
	}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as machineInfo aware format
func (p *StaticPolicy) GetTopologyAwareResources(_ context.Context,
	req *pluginapi.GetTopologyAwareResourcesRequest,
) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	// TODO
	return nil, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as machineInfo aware format
func (p *StaticPolicy) GetTopologyAwareAllocatableResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareAllocatableResourcesRequest,
) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	general.Infof("is called")
	// TODO
	return nil, nil
}

// PreStartContainer is called, if indicated by resource plugin during registration phase,
// before each container start. Resource plugin can run resource specific operations
// such as resetting the resource before making resources available to the container
func (p *StaticPolicy) PreStartContainer(context.Context,
	*pluginapi.PreStartContainerRequest,
) (*pluginapi.PreStartContainerResponse, error) {
	return nil, nil
}

func (p *StaticPolicy) RemovePod(ctx context.Context,
	req *pluginapi.RemovePodRequest,
) (resp *pluginapi.RemovePodResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}
	general.InfoS("is called", "podUID", req.PodUid)

	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameRemovePodFailed, 1, metrics.MetricTypeNameRaw)
		}
	}()

	if toRelease, ok := p.state.GetCPUSet(req.PodUid); ok {
		p.state.Delete(req.PodUid)
		// Mutate the shared pool, adding released cpus.
		p.state.SetDefaultCPUSet(p.state.GetDefaultCPUSet().Union(toRelease))
	}

	return &pluginapi.RemovePodResponse{}, nil
}

// setReservedCPUs calculates and sets the reservedCPUs field
func (p *StaticPolicy) setReservedCPUs(topo *machine.CPUTopology) error {
	klConfig, err := p.metaServer.GetKubeletConfig(context.TODO())
	if err != nil {
		return fmt.Errorf("NewNativePolicy failed because get kubelet config failed with error: %v", err)
	}

	reservedQuantity, _, err := utilkubeconfig.GetReservedQuantity(klConfig, string(v1.ResourceCPU))
	if err != nil {
		return fmt.Errorf("getKubeletReservedQuantity failed because get kubelet reserved quantity failed with error: %v", err)
	}

	if reservedQuantity.IsZero() {
		// The native policy requires this to be nonzero. Zero CPU reservation
		// would allow the shared pool to be completely exhausted. At that point
		// either we would violate our guarantee of exclusivity or need to evict
		// any pod that has at least one container that requires zero CPUs.
		// See the comments in policy_static.go for more details.
		return fmt.Errorf("the native policy requires systemreserved.cpu + kubereserved.cpu to be greater than zero")
	}

	// Take the ceiling of the reservation, since fractional CPUs cannot be
	// exclusively allocated.
	reservedCPUsFloat := float64(reservedQuantity.MilliValue()) / 1000
	numReservedCPUs := int(math.Ceil(reservedCPUsFloat))

	var reserved machine.CPUSet
	reservedCPUs, err := machine.Parse(klConfig.ReservedSystemCPUs)
	if err != nil {
		return fmt.Errorf("NewNativePolicy parse cpuset for reserved-cpus failed with error: %v", err)
	}
	if reservedCPUs.Size() > 0 {
		reserved = reservedCPUs
	} else {
		// takeByTopology allocates CPUs associated with low-numbered cores from
		// allCPUs.
		reserved, _ = takeByTopology(topo, topo.CPUDetails.CPUs(), numReservedCPUs)
	}

	if reserved.Size() != numReservedCPUs {
		return fmt.Errorf("unable to reserve the required amount of CPUs (size of %s did not equal %d)", reserved, numReservedCPUs)
	}

	general.Infof("take reserved CPUs: %s by reservedCPUsNum: %d", reserved.String(), numReservedCPUs)

	p.reservedCPUs = reserved

	return nil
}
