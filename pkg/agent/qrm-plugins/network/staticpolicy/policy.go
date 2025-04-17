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
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	apinode "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	appqrm "github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/staticpolicy/nic"
	networkreactor "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/staticpolicy/reactor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/reactor"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	"github.com/kubewharf/katalyst-core/pkg/config"
	agentconfig "github.com/kubewharf/katalyst-core/pkg/config/agent"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
	qrmgeneral "github.com/kubewharf/katalyst-core/pkg/util/qrm"
)

const (
	// NetworkResourcePluginPolicyNameStatic is the policy name of static network resource plugin
	NetworkResourcePluginPolicyNameStatic = string(apiconsts.ResourcePluginPolicyNameStatic)

	NetworkPluginStateFileName = "network_plugin_state"

	// IPsSeparator is used to split merged IPs string
	IPsSeparator = ","

	// residualNetClassTTL is the ttl of residual net class in cgroupv2
	residualNetClassTTL = 30 * time.Minute
	// maxResidualNetClassNum is the max number of allowed net class in cgroupv2
	maxResidualNetClassNum = 4096
)

// StaticPolicy is the static network policy
type StaticPolicy struct {
	sync.Mutex

	name           string
	stopCh         chan struct{}
	started        bool
	qosConfig      *generic.QoSConfiguration
	qrmConfig      *qrm.QRMPluginsConfiguration
	emitter        metrics.MetricEmitter
	metaServer     *metaserver.MetaServer
	agentCtx       *agent.GenericContext
	state          state.State
	residualHitMap map[string]int64

	CgroupV2Env                                     bool
	qosLevelToNetClassMap                           map[string]uint32
	applyNetClassFunc                               func(podUID, containerID string, data *common.NetClsData) error
	applyNetworkGroupsFunc                          func(map[string]*qrmgeneral.NetworkGroup) error
	podLevelNetClassAnnoKey                         string
	podLevelNetAttributesAnnoKeys                   []string
	ipv4ResourceAllocationAnnotationKey             string
	ipv6ResourceAllocationAnnotationKey             string
	netNSPathResourceAllocationAnnotationKey        string
	netInterfaceNameResourceAllocationAnnotationKey string
	netClassIDResourceAllocationAnnotationKey       string
	netBandwidthResourceAllocationAnnotationKey     string

	podAnnotationKeptKeys []string
	podLabelKeptKeys      []string

	lowPriorityGroups map[string]*qrmgeneral.NetworkGroup

	nicAllocationReactor reactor.AllocationReactor

	nicManager nic.NICManager

	// aliveCgroupID is used to record the alive cgroupIDs and their last alive time
	aliveCgroupID map[uint64]time.Time
}

// NewStaticPolicy returns a static network policy
func NewStaticPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: NetworkResourcePluginPolicyNameStatic,
	})

	nicManager, err := nic.NewNICManager(agentCtx.MetaServer, wrappedEmitter, conf)
	if err != nil {
		return false, nil, err
	}
	enabledNICs := getAllNICs(nicManager)

	// we only support one spreading policy for now: reserve the bandwidth on the first NIC.
	// TODO: make the reservation policy configurable
	reservation, err := getReservedBandwidth(enabledNICs, conf.ReservedBandwidth, FirstNIC)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("getReservedBandwidth failed with error: %v", err)
	}

	stateImpl, err := state.NewCheckpointState(conf.QRMPluginsConfiguration, conf.GenericQRMPluginConfiguration.StateFileDirectory, NetworkPluginStateFileName,
		NetworkResourcePluginPolicyNameStatic, agentCtx.MachineInfo, enabledNICs, reservation, conf.SkipNetworkStateCorruption)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("NewCheckpointState failed with error: %v", err)
	}

	policyImplement := &StaticPolicy{
		nicManager:            nicManager,
		qosConfig:             conf.QoSConfiguration,
		qrmConfig:             conf.QRMPluginsConfiguration,
		emitter:               wrappedEmitter,
		metaServer:            agentCtx.MetaServer,
		agentCtx:              agentCtx,
		state:                 stateImpl,
		residualHitMap:        make(map[string]int64),
		stopCh:                make(chan struct{}),
		name:                  fmt.Sprintf("%s_%s", agentName, NetworkResourcePluginPolicyNameStatic),
		qosLevelToNetClassMap: make(map[string]uint32),
		podAnnotationKeptKeys: conf.PodAnnotationKeptKeys,
		podLabelKeptKeys:      conf.PodLabelKeptKeys,
		aliveCgroupID:         make(map[uint64]time.Time),
	}

	if common.CheckCgroup2UnifiedMode() {
		policyImplement.CgroupV2Env = true
		policyImplement.applyNetClassFunc = agentCtx.MetaServer.ExternalManager.ApplyNetClass
	} else {
		policyImplement.CgroupV2Env = false
		policyImplement.applyNetClassFunc = cgroupcmutils.ApplyNetClsForContainer
	}

	policyImplement.applyNetworkGroupsFunc = agentCtx.MetaServer.ExternalManager.ApplyNetworkGroups

	policyImplement.ApplyConfig(conf.StaticAgentConfiguration)

	err = policyImplement.generateAndApplyGroups()
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("generateAndApplyGroups failed with error: %v", err)
	}

	policyImplement.nicAllocationReactor = reactor.DummyAllocationReactor{}
	if conf.EnableNICAllocationReactor {
		policyImplement.nicAllocationReactor = networkreactor.NewNICPodAllocationReactor(
			reactor.NewPodAllocationReactor(
				agentCtx.MetaServer.PodFetcher,
				agentCtx.Client.KubeClient,
			))
	}

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(policyImplement, conf.QRMPluginSocketDirs,
		func(key string, value int64) {
			_ = wrappedEmitter.StoreInt64(key, value, metrics.MetricTypeNameRaw)
		})
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("static policy new plugin wrapper failed with error: %v", err)
	}

	return true, &agent.PluginWrapper{GenericPlugin: pluginWrapper}, nil
}

// ApplyConfig applies config to StaticPolicy
func (p *StaticPolicy) ApplyConfig(conf *agentconfig.StaticAgentConfiguration) {
	p.Lock()
	defer p.Unlock()

	p.qosLevelToNetClassMap[apiconsts.PodAnnotationQoSLevelReclaimedCores] = conf.NetClass.ReclaimedCores
	p.qosLevelToNetClassMap[apiconsts.PodAnnotationQoSLevelSharedCores] = conf.NetClass.SharedCores
	p.qosLevelToNetClassMap[apiconsts.PodAnnotationQoSLevelDedicatedCores] = conf.NetClass.DedicatedCores
	p.qosLevelToNetClassMap[apiconsts.PodAnnotationQoSLevelSystemCores] = conf.NetClass.SystemCores

	p.podLevelNetClassAnnoKey = conf.PodLevelNetClassAnnoKey
	p.podLevelNetAttributesAnnoKeys = strings.Split(conf.PodLevelNetAttributesAnnoKeys, ",")
	p.ipv4ResourceAllocationAnnotationKey = conf.IPv4ResourceAllocationAnnotationKey
	p.ipv6ResourceAllocationAnnotationKey = conf.IPv6ResourceAllocationAnnotationKey
	p.netNSPathResourceAllocationAnnotationKey = conf.NetNSPathResourceAllocationAnnotationKey
	p.netInterfaceNameResourceAllocationAnnotationKey = conf.NetInterfaceNameResourceAllocationAnnotationKey
	p.netClassIDResourceAllocationAnnotationKey = conf.NetClassIDResourceAllocationAnnotationKey
	p.netBandwidthResourceAllocationAnnotationKey = conf.NetBandwidthResourceAllocationAnnotationKey

	general.Infof("apply configs, "+
		"qosLevelToNetClassMap: %+v, "+
		"podLevelNetClassAnnoKey: %s, "+
		"podLevelNetAttributesAnnoKeys: %+v",
		p.qosLevelToNetClassMap,
		p.podLevelNetClassAnnoKey,
		p.podLevelNetAttributesAnnoKeys)
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

	err = periodicalhandler.RegisterPeriodicalHandlerWithHealthz(consts.ClearResidualState, general.HealthzCheckStateNotReady,
		appqrm.QRMNetworkPluginPeriodicalHandlerGroupName, p.clearResidualState, consts.StateCheckPeriod, consts.StateCheckTolerationTimes)
	if err != nil {
		general.Errorf("start %v failed, err: %v", consts.ClearResidualState, err)
	}

	go wait.Until(func() {
		periodicalhandler.ReadyToStartHandlersByGroup(appqrm.QRMNetworkPluginPeriodicalHandlerGroupName)
	}, 5*time.Second, p.stopCh)

	go func(stopCh <-chan struct{}) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		p.nicManager.Run(ctx)
		<-stopCh
	}(p.stopCh)

	go wait.Until(p.applyNetClass, 5*time.Second, p.stopCh)

	return nil
}

// clearResidualState is used to clean residual pods in local state
func (p *StaticPolicy) clearResidualState(_ *config.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("exec")
	var (
		err     error
		podList []*v1.Pod
	)
	residualSet := make(map[string]bool)

	defer func() {
		_ = general.UpdateHealthzStateByError(consts.ClearResidualState, err)
	}()

	if p.metaServer == nil {
		general.Errorf("nil metaServer")
		return
	}

	ctx := context.Background()
	podList, err = p.metaServer.GetPodList(ctx, nil)
	if err != nil {
		general.Errorf("get pod list failed: %v", err)
		return
	}

	podSet := sets.NewString()
	for _, pod := range podList {
		podSet.Insert(fmt.Sprintf("%v", pod.UID))
	}

	p.Lock()
	defer p.Unlock()

	podEntries := p.state.GetPodEntries()
	for podUID := range podEntries {
		if !podSet.Has(podUID) {
			residualSet[podUID] = true
			p.residualHitMap[podUID] += 1
			general.Infof("found pod: %s with state but doesn't show up in pod watcher, hit count: %d", podUID, p.residualHitMap[podUID])
		}
	}

	podsToDelete := sets.NewString()
	for podUID, hitCount := range p.residualHitMap {
		if !residualSet[podUID] {
			general.Infof("already found pod: %s in pod watcher or its state is cleared, delete it from residualHitMap", podUID)
			delete(p.residualHitMap, podUID)
			continue
		}

		if time.Duration(hitCount)*consts.StateCheckPeriod >= consts.MaxResidualTime {
			podsToDelete.Insert(podUID)
		}
	}

	if podsToDelete.Len() > 0 {
		for {
			podUID, found := podsToDelete.PopAny()
			if !found {
				break
			}

			general.Infof("clear residual pod: %s in state", podUID)
			delete(podEntries, podUID)
		}

		machineState, err := state.GenerateMachineStateFromPodEntries(p.qrmConfig, getAllNICs(p.nicManager), podEntries, p.state.GetReservedBandwidth())
		if err != nil {
			general.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			return
		}

		p.state.SetPodEntries(podEntries, false)
		p.state.SetMachineState(machineState, false)

		err = p.state.StoreState()
		if err != nil {
			general.Errorf("store state failed: %v", err)
			return
		}
	}

	err = p.generateAndApplyGroups()
	if err != nil {
		general.Errorf("generateAndApplyGroups failed with error: %v", err)
	}
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
	return nil
}

// Name returns the name of this plugin
func (p *StaticPolicy) Name() string {
	return p.name
}

// ResourceName returns resource names managed by this plugin
func (p *StaticPolicy) ResourceName() string {
	return string(apiconsts.ResourceNetBandwidth)
}

// GetTopologyHints returns hints of corresponding resources
func (p *StaticPolicy) GetTopologyHints(_ context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceHintsResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req, p.podAnnotationKeptKeys, p.podLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	reqInt, _, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	general.InfoS("called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"qosLevel", qosLevel,
		"resourceRequests", req.ResourceRequests,
		"reqAnnotations", req.Annotations,
		"netBandwidthReq(Mbps)", reqInt)

	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
		}
	}()

	if req.ContainerType == pluginapi.ContainerType_INIT ||
		req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return util.PackResourceHintsResponse(req, p.ResourceName(), map[string]*pluginapi.ListOfTopologyHints{
			p.ResourceName(): nil, // indicates that there is no numa preference
		})
	}

	hints, err := p.calculateHints(req)
	if err != nil {
		err = fmt.Errorf("calculateHints for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	return util.PackResourceHintsResponse(req, p.ResourceName(), hints)
}

// GetPodTopologyHints returns hints of corresponding resources for pod
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

	p.Lock()
	defer p.Unlock()

	if err := p.removePod(req.PodUid); err != nil {
		general.ErrorS(err, "remove pod failed with error", "podUID", req.PodUid)
		return nil, err
	}

	return &pluginapi.RemovePodResponse{}, nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *StaticPolicy) GetResourcesAllocation(_ context.Context,
	_ *pluginapi.GetResourcesAllocationRequest,
) (*pluginapi.GetResourcesAllocationResponse, error) {
	// no need to implement this function, because NeedReconcile is false
	return &pluginapi.GetResourcesAllocationResponse{}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as topology aware format
func (p *StaticPolicy) GetTopologyAwareResources(_ context.Context,
	req *pluginapi.GetTopologyAwareResourcesRequest,
) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyAwareResources got nil req")
	}

	p.Lock()
	defer p.Unlock()

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo == nil {
		return &pluginapi.GetTopologyAwareResourcesResponse{}, nil
	}

	interfaceInfo := p.getNICByName(getAllNICs(p.nicManager), allocationInfo.IfName)
	socket, err := p.getSocketIDByNIC(interfaceInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to find topologyNode for pod %s, container %s : %v", req.PodUid, req.ContainerName, err)
	}

	identifier := allocationInfo.Identifier
	if identifier == "" {
		// backup to use ifName and interfaceInfo.NSName as identifier
		identifier = getResourceIdentifier(interfaceInfo.NSName, interfaceInfo.Iface)
	}

	topologyAwareQuantityList := []*pluginapi.TopologyAwareQuantity{
		{
			ResourceValue: float64(allocationInfo.Egress),
			Node:          uint64(socket),
			Name:          allocationInfo.IfName,
			Type:          string(apinode.TopologyTypeNIC),
			TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			Annotations: map[string]string{
				apiconsts.ResourceAnnotationKeyResourceIdentifier: identifier,
				apiconsts.ResourceAnnotationKeyNICNetNSName:       interfaceInfo.NSName,
			},
		},
	}
	resp := &pluginapi.GetTopologyAwareResourcesResponse{
		PodUid:       allocationInfo.PodUid,
		PodName:      allocationInfo.PodName,
		PodNamespace: allocationInfo.PodNamespace,
		ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
			ContainerName: allocationInfo.ContainerName,
		},
	}

	if allocationInfo.CheckSideCar() {
		resp.ContainerTopologyAwareResources.AllocatedResources = map[string]*pluginapi.TopologyAwareResource{
			string(apiconsts.ResourceNetBandwidth): {
				IsNodeResource:                    true,
				IsScalarResource:                  true,
				AggregatedQuantity:                0,
				OriginalAggregatedQuantity:        0,
				TopologyAwareQuantityList:         nil,
				OriginalTopologyAwareQuantityList: nil,
			},
		}
	} else {
		resp.ContainerTopologyAwareResources.AllocatedResources = map[string]*pluginapi.TopologyAwareResource{
			string(apiconsts.ResourceNetBandwidth): {
				IsNodeResource:                    true,
				IsScalarResource:                  true,
				AggregatedQuantity:                float64(allocationInfo.Egress),
				OriginalAggregatedQuantity:        float64(allocationInfo.Egress),
				TopologyAwareQuantityList:         topologyAwareQuantityList,
				OriginalTopologyAwareQuantityList: topologyAwareQuantityList,
			},
		}
	}

	return resp, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as topology aware format
func (p *StaticPolicy) GetTopologyAwareAllocatableResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareAllocatableResourcesRequest,
) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	p.Lock()
	defer p.Unlock()

	machineState := p.state.GetMachineState()

	topologyAwareAllocatableQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))
	topologyAwareCapacityQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))

	var aggregatedAllocatableQuantity, aggregatedCapacityQuantity uint32 = 0, 0
	topologyAwareAllocatableQuantityFunc := func(nics []machine.InterfaceInfo, health bool) error {
		for _, iface := range nics {
			nicState := machineState[iface.Iface]
			if nicState == nil {
				return fmt.Errorf("nil nicState for NIC: %s", iface.Iface)
			}

			topologyNode, err := p.getSocketIDByNIC(iface)
			if err != nil {
				return fmt.Errorf("failed to find topologyNode: %v", err)
			}

			var allocatable uint32
			resourceIdentifier := getResourceIdentifier(iface.NSName, iface.Iface)
			if health {
				allocatable = general.MinUInt32(nicState.EgressState.Allocatable, nicState.IngressState.Allocatable)
			}
			capacity := general.MinUInt32(nicState.EgressState.Capacity, nicState.IngressState.Capacity)
			topologyAwareAllocatableQuantityList = append(topologyAwareAllocatableQuantityList, &pluginapi.TopologyAwareQuantity{
				ResourceValue: float64(allocatable),
				Node:          uint64(topologyNode),
				Name:          iface.Iface,
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
				Annotations: map[string]string{
					apiconsts.ResourceAnnotationKeyResourceIdentifier: resourceIdentifier,
					apiconsts.ResourceAnnotationKeyNICNetNSName:       iface.NSName,
				},
			})
			topologyAwareCapacityQuantityList = append(topologyAwareCapacityQuantityList, &pluginapi.TopologyAwareQuantity{
				ResourceValue: float64(capacity),
				Node:          uint64(topologyNode),
				Name:          iface.Iface,
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
				Annotations: map[string]string{
					apiconsts.ResourceAnnotationKeyResourceIdentifier: resourceIdentifier,
					apiconsts.ResourceAnnotationKeyNICNetNSName:       iface.NSName,
				},
			})
			aggregatedAllocatableQuantity += allocatable
			aggregatedCapacityQuantity += capacity
		}
		return nil
	}

	nics := p.nicManager.GetNICs()
	err := topologyAwareAllocatableQuantityFunc(nics.HealthyNICs, true)
	if err != nil {
		return nil, fmt.Errorf("get healthyNICs failed: %v", err)
	}

	err = topologyAwareAllocatableQuantityFunc(nics.UnhealthyNICs, false)
	if err != nil {
		return nil, fmt.Errorf("get unHealthyNICs failed: %v", err)
	}

	return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
		AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
			string(apiconsts.ResourceNetBandwidth): {
				IsNodeResource:                       true,
				IsScalarResource:                     true,
				AggregatedAllocatableQuantity:        float64(aggregatedAllocatableQuantity),
				TopologyAwareAllocatableQuantityList: topologyAwareAllocatableQuantityList,
				AggregatedCapacityQuantity:           float64(aggregatedCapacityQuantity),
				TopologyAwareCapacityQuantityList:    topologyAwareCapacityQuantityList,
			},
		},
	}, nil
}

// GetResourcePluginOptions returns options to be communicated with Resource Manager
func (p *StaticPolicy) GetResourcePluginOptions(context.Context,
	*pluginapi.Empty,
) (*pluginapi.ResourcePluginOptions, error) {
	return &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: true,
		NeedReconcile:         false,
	}, nil
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
func (p *StaticPolicy) Allocate(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceAllocationResponse, err error) {
	var isReallocated bool

	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	// since qos config util will filter out annotation keys not related to katalyst QoS,
	// we copy original pod annotations here to use them later
	podAnnotations := maputil.CopySS(req.Annotations)

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req, p.podAnnotationKeptKeys, p.podLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	netClassID, err := p.getNetClassID(podAnnotations, p.podLevelNetClassAnnoKey, qosLevel)
	if err != nil {
		err = fmt.Errorf("getNetClassID for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	reqInt, _, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	general.InfoS("called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"qosLevel", qosLevel,
		"reqAnnotations", req.Annotations,
		"netBandwidthReq(Mbps)", reqInt,
		"netClassID", netClassID)

	p.Lock()
	defer func() {
		if err := p.state.StoreState(); err != nil {
			general.ErrorS(err, "store state failed", "podName", req.PodName, "containerName", req.ContainerName)
		}
		p.Unlock()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameAllocateFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)},
				metrics.MetricTag{Key: "reallocated", Val: strconv.FormatBool(isReallocated)})
		}
	}()

	emptyResponse := &pluginapi.ResourceAllocationResponse{
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
	}

	// currently, not to deal with init containers
	if req.ContainerType == pluginapi.ContainerType_INIT {
		return emptyResponse, nil
	} else if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		// not to deal with sidecars, and return a trivial allocationResult to avoid re-allocating
		return packAllocationResponse(req, &state.AllocationInfo{}, nil)
	}

	// check allocationInfo is nil or not
	podEntries := p.state.GetPodEntries()
	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	nics := getAllNICs(p.nicManager)

	// if allocationInfo is not nil, assume that this container has already been allocated,
	// and check whether the current allocation meets the requirement, if not, clear the record and re-allocate,
	// otherwise, return the current allocationResult
	if allocationInfo != nil {
		isReallocated = true
		if allocationInfo.Egress >= uint32(reqInt) && allocationInfo.Ingress >= uint32(reqInt) {
			general.InfoS("already allocated and meet requirement",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"bandwidthReq(Mbps)", reqInt,
				"currentResult(Mbps)", allocationInfo.Egress)

			resourceAllocationAnnotations, err := p.getResourceAllocationAnnotations(nics, podAnnotations, allocationInfo, netClassID)
			if err != nil {
				err = fmt.Errorf("getResourceAllocationAnnotations for pod: %s/%s, container: %s failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				general.Errorf("%s", err.Error())
				return nil, err
			}

			resp, packErr := packAllocationResponse(req, allocationInfo, resourceAllocationAnnotations)
			if packErr != nil {
				general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, packErr)
				return nil, fmt.Errorf("packAllocationResponse failed with error: %v", packErr)
			}
			return resp, nil
		} else {
			general.InfoS("not meet requirement, clear record and re-allocate",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"bandwidthReq(Mbps)", reqInt,
				"currentResult(Mbps)", allocationInfo.Egress)
			delete(podEntries, req.PodUid)

			_, stateErr := state.GenerateMachineStateFromPodEntries(p.qrmConfig, nics, podEntries, p.state.GetReservedBandwidth())
			if stateErr != nil {
				general.ErrorS(stateErr, "generateNetworkMachineStateByPodEntries failed",
					"podNamespace", req.PodNamespace,
					"podName", req.PodName,
					"containerName", req.ContainerName,
					"bandwidthReq(Mbps)", reqInt,
					"currentResult(Mbps)", allocationInfo.Egress)
				return nil, fmt.Errorf("generateNetworkMachineStateByPodEntries failed with error: %v", stateErr)
			}
		}
	}

	candidateNICs, err := p.selectNICsByReq(nics, req)
	if err != nil {
		err = fmt.Errorf("selectNICsByReq for pod: %s/%s, container: %s, reqInt: %d, failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, reqInt, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	if len(candidateNICs) == 0 {
		general.ErrorS(err, "insufficient bandwidth on this node to satisfy the request",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"netBandwidthReq(Mbps)", reqInt,
			"nicState", p.state.GetMachineState().String())
		return nil, fmt.Errorf("failed to meet the bandwidth requirement of %d Mbps", reqInt)
	}

	// we only support one policy and hard code it for now
	// TODO: make the policy configurable
	selectedNIC := selectOneNIC(candidateNICs, RandomOne)
	general.Infof("select NIC %s to allocate bandwidth (%dMbps)", selectedNIC.Iface, reqInt)

	allocateNUMAs, err := machine.GetNICAllocateNUMAs(selectedNIC, p.agentCtx.KatalystMachineInfo)
	if err != nil {
		general.Errorf("get allocateNUMAs for nic: %s failed with error: %v. Incorrect NumaNodes in machineState allocationInfo", selectedNIC.Iface, err)
	}

	// generate allocationInfo and update the checkpoint accordingly
	newAllocation := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
			commonstate.EmptyOwnerPoolName, qosLevel),
		Egress:     uint32(reqInt),
		Ingress:    uint32(reqInt),
		Identifier: getResourceIdentifier(selectedNIC.NSName, selectedNIC.Iface),
		NSName:     selectedNIC.NSName,
		IfName:     selectedNIC.Iface,
		NumaNodes:  allocateNUMAs,
		NetClassID: fmt.Sprintf("%d", netClassID),
	}

	err = applyImplicitReq(req, newAllocation)
	if err != nil {
		err = fmt.Errorf("p.applyImplicitReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	resourceAllocationAnnotations, err := p.getResourceAllocationAnnotations(nics, podAnnotations, newAllocation, netClassID)
	if err != nil {
		err = fmt.Errorf("getResourceAllocationAnnotations for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	// update PodEntries
	p.state.SetAllocationInfo(req.PodUid, req.ContainerName, newAllocation, false)

	machineState, stateErr := state.GenerateMachineStateFromPodEntries(p.qrmConfig, nics, p.state.GetPodEntries(), p.state.GetReservedBandwidth())
	if stateErr != nil {
		general.ErrorS(stateErr, "generateNetworkMachineStateByPodEntries failed",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"bandwidthReq(Mbps)", reqInt,
			"currentResult(Mbps)", newAllocation.Egress)
		return nil, fmt.Errorf("generateNetworkMachineStateByPodEntries failed with error: %v", stateErr)
	}

	// update state cache
	p.state.SetMachineState(machineState, false)

	err = p.generateAndApplyGroups()
	if err != nil {
		general.Errorf("generateAndApplyGroups failed with error: %v", err)
	}

	// update nic allocation
	err = p.nicAllocationReactor.UpdateAllocation(ctx, newAllocation)
	if err != nil {
		general.Errorf("nicAllocationReactor UpdateAllocation failed with error: %v", err)
		return nil, err
	}

	return packAllocationResponse(req, newAllocation, resourceAllocationAnnotations)
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

func (p *StaticPolicy) applyNetClass() {
	if p.metaServer == nil {
		general.Errorf("nil metaServer")
		return
	}

	podList, err := p.metaServer.GetPodList(context.Background(), native.PodIsActive)
	if err != nil {
		general.Errorf("get pod list failed, err: %v", err)
		return
	}

	activeNetClsData := make(map[uint64]*common.NetClsData)
	for _, pod := range podList {
		if pod == nil {
			general.Errorf("get nil pod from metaServer")
			continue
		}

		qosLevel, err := p.qosConfig.GetQoSLevel(nil, pod.Annotations)
		if err != nil {
			general.Errorf("get qos level for pod: %s/%s failed with err", pod.Namespace, pod.Name)
			continue
		}

		classID, err := p.getNetClassID(pod.GetAnnotations(), p.podLevelNetClassAnnoKey, qosLevel)
		if err != nil {
			general.Errorf("get net class id failed, pod: %s, err: %s", native.GenerateUniqObjectNameKey(pod), err)
			continue
		}
		netClsData := &common.NetClsData{
			ClassID:    classID,
			Attributes: native.FilterPodAnnotations(p.podLevelNetAttributesAnnoKeys, pod),
		}

		for _, container := range pod.Spec.Containers {
			podUID := string(pod.GetUID())
			containerName := container.Name
			containerID, err := p.metaServer.GetContainerID(podUID, containerName)
			if err != nil {
				general.Errorf("get container id failed, pod: %s, container: %s(%s), err: %v",
					podUID, containerName, containerID, err)
				continue
			}

			if exist, err := common.IsContainerCgroupExist(podUID, containerID); err != nil {
				general.Errorf("check if container cgroup exists failed, pod: %s, container: %s(%s), err: %v",
					podUID, containerName, containerID, err)
				continue
			} else if !exist {
				general.Infof("container cgroup does not exist, pod: %s, container: %s(%s)", podUID, containerName, containerID)
				continue
			}

			if p.CgroupV2Env {
				cgID, err := p.metaServer.ExternalManager.GetCgroupIDForContainer(podUID, containerID)
				if err != nil {
					general.Errorf("get cgroup id failed, pod: %s, container: %s(%s), err: %v",
						podUID, containerName, containerID, err)
					continue
				}
				netClsData.CgroupID = cgID
				activeNetClsData[cgID] = netClsData
			}

			go func(podUID, containerName string, netClsData *common.NetClsData) {
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

	if p.CgroupV2Env {
		p.clearResidualNetClass(activeNetClsData)
	}
}

func (p *StaticPolicy) filterAvailableNICsByBandwidth(nics []machine.InterfaceInfo, req *pluginapi.ResourceRequest, _ *agent.GenericContext) []machine.InterfaceInfo {
	filteredNICs := make([]machine.InterfaceInfo, 0, len(nics))

	if req == nil {
		general.Infof("filterNICsByBandwidth got nil req")
		return nil
	}

	reqInt, _, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		general.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
		return nil
	}

	machineState := p.state.GetMachineState()
	if len(machineState) == 0 || len(nics) == 0 {
		general.Errorf("filterNICsByBandwidth with 0 NIC")
		return nil
	}

	// filter NICs by available bandwidth
	for _, iface := range nics {
		if machineState[iface.Iface].EgressState.Free >= uint32(reqInt) && machineState[iface.Iface].IngressState.Free >= uint32(reqInt) {
			filteredNICs = append(filteredNICs, iface)
		}
	}

	// no nic meets the bandwidth request
	if len(filteredNICs) == 0 {
		general.InfoS("nic list returned by filtereNICsByBandwidth is empty",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName)
	}

	return filteredNICs
}

func (p *StaticPolicy) calculateHints(req *pluginapi.ResourceRequest) (map[string]*pluginapi.ListOfTopologyHints, error) {
	// resp.hints: 1) empty, means no resource (i.e. NIC) meeting requirements found; 2) nil, does not care about the hints
	// since NIC is a kind of topology-aware resource, it is incorrect to return nil
	hints := map[string]*pluginapi.ListOfTopologyHints{
		p.ResourceName(): {
			Hints: []*pluginapi.TopologyHint{},
		},
	}

	nics := p.nicManager.GetNICs()
	// return empty hints immediately if no healthy nics on this node
	if len(nics.HealthyNICs) == 0 {
		return hints, nil
	}

	candidateNICs, err := p.selectNICsByReq(nics.HealthyNICs, req)
	if err != nil {
		return hints, fmt.Errorf("failed to select available NICs: %v", err)
	}

	if len(candidateNICs) == 0 {
		general.InfoS("candidateNICs is empty",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName)
		// if the req.NS asks to allocate on the 1st NIC which does not have sufficient bandwidth, candidateNICs is empty.
		// however, we should not return directly here. To indicate the option of the 2nd NIC if no restricted affinity or ns requested, we return [0,1,2,3] instead.
	}

	numasToHintMap := make(map[string]*pluginapi.TopologyHint)
	for _, nic := range candidateNICs {
		allocateNUMAs, err := machine.GetNICAllocateNUMAs(nic, p.agentCtx.KatalystMachineInfo)
		if err != nil {
			return nil, fmt.Errorf("get allocateNUMAs for nic: %s failed with error: %v", nic.Iface, err)
		}

		nicPreference, err := checkNICPreferenceOfReq(nic, req.Annotations)
		if err != nil {
			return nil, fmt.Errorf("checkNICPreferenceOfReq for nic: %s failed with error: %v", nic.Iface, err)
		}

		siblingNUMAsStr := allocateNUMAs.String()
		if numasToHintMap[siblingNUMAsStr] == nil {
			numasToHintMap[siblingNUMAsStr] = &pluginapi.TopologyHint{
				Nodes: allocateNUMAs.ToSliceUInt64(),
			}
		}

		if nicPreference {
			general.InfoS("set nic preferred to true",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"nic", nic.Iface)
			numasToHintMap[siblingNUMAsStr].Preferred = nicPreference
		}
	}

	for _, hint := range numasToHintMap {
		hints[p.ResourceName()].Hints = append(hints[p.ResourceName()].Hints, hint)
	}

	// check if restricted affinity requested
	if !isReqAffinityRestricted(req.Annotations) {
		general.InfoS("add all NUMAs to hint to avoid affinity error",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			req.Annotations[apiconsts.PodAnnotationNetworkEnhancementAffinityRestricted],
			apiconsts.PodAnnotationNetworkEnhancementAffinityRestrictedTrue)

		hints[p.ResourceName()].Hints = append(hints[p.ResourceName()].Hints, &pluginapi.TopologyHint{
			Nodes: p.agentCtx.CPUDetails.NUMANodes().ToSliceUInt64(),
		})
	}

	return hints, nil
}

/*
The NIC selection depends on the following three aspects: available Bandwidth on each NIC, Namespace parameter in request, and req.Hints.
1) The availability of sufficient bandwidth on the NIC is a prerequisite for determining whether the card can be selected.
If there is insufficient bandwidth on a NIC, it cannot be included in the candidate list.

2) We may put NICs into separate net namespaces in order to use both NICs simultaneously (Host network mode).
If a container wants to request a specific NIC through the namespace parameter, this requirement must also be met.
If the specified NIC has insufficient bandwidth, it cannot be included in the candidate list.

3) The req.Hints parameter represents the affinity of a NIC. For example, a socket container running on a specific socket
may use req.Hints to prioritize the selection of a NIC connected to that socket. However, this requirement is only satisfied as much as possible.
If the NIC connected to the socket has sufficient bandwidth, only this NIC is returned. Otherwise, other cards with sufficient bandwidth will be returned.
*/
func (p *StaticPolicy) selectNICsByReq(nics []machine.InterfaceInfo, req *pluginapi.ResourceRequest) ([]machine.InterfaceInfo, error) {
	nicFilters := []NICFilter{
		p.filterAvailableNICsByBandwidth,
		filterNICsByNamespaceType,
		filterNICsByHint,
	}

	if len(nics) == 0 {
		return []machine.InterfaceInfo{}, nil
	}

	candidateNICs, err := filterAvailableNICsByReq(nics, req, p.agentCtx, nicFilters)
	if err != nil {
		return nil, fmt.Errorf("filterAvailableNICsByReq failed with error: %v", err)
	}

	// this node can not meet the combined requests
	if len(candidateNICs) == 0 {
		general.InfoS("nic list returned by filterAvailableNICsByReq is empty",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName)
	}

	return candidateNICs, nil
}

func (p *StaticPolicy) getResourceAllocationAnnotations(
	nics []machine.InterfaceInfo,
	podAnnotations map[string]string,
	allocation *state.AllocationInfo, netClsID uint32,
) (map[string]string, error) {
	selectedNIC := p.getNICByName(nics, allocation.IfName)

	resourceAllocationAnnotations := map[string]string{
		p.ipv4ResourceAllocationAnnotationKey:             strings.Join(selectedNIC.Addr.GetNICIPs(machine.IPVersionV4), IPsSeparator),
		p.ipv6ResourceAllocationAnnotationKey:             strings.Join(selectedNIC.Addr.GetNICIPs(machine.IPVersionV6), IPsSeparator),
		p.netInterfaceNameResourceAllocationAnnotationKey: selectedNIC.Iface,
		p.netClassIDResourceAllocationAnnotationKey:       fmt.Sprintf("%d", netClsID),
	}

	if !isImplicitReq(podAnnotations) {
		// TODO: support differentiated Egress/Ingress bandwidth later
		resourceAllocationAnnotations[p.netBandwidthResourceAllocationAnnotationKey] = strconv.Itoa(int(allocation.Egress))
	}

	if len(selectedNIC.NSAbsolutePath) > 0 {
		resourceAllocationAnnotations[p.netNSPathResourceAllocationAnnotationKey] = selectedNIC.NSAbsolutePath
	}

	return resourceAllocationAnnotations, nil
}

func (p *StaticPolicy) removePod(podUID string) error {
	if p.CgroupV2Env {
		cgIDList, err := p.metaServer.ExternalManager.ListCgroupIDsForPod(podUID)
		if err != nil {
			if general.IsErrNotFound(err) {
				general.Warningf("cgroup ids for pod not found")
				return nil
			}
			return fmt.Errorf("[NetworkStaticPolicy.removePod] list cgroup ids of pod: %s failed with error: %v", podUID, err)
		}

		for _, cgID := range cgIDList {
			go func(cgID uint64) {
				if err := p.metaServer.ExternalManager.ClearNetClass(cgID); err != nil {
					general.Errorf("delete net class failed, cgID: %v, err: %v", cgID, err)
					return
				}
			}(cgID)
		}
	}

	// update state cache
	podEntries := p.state.GetPodEntries()
	delete(podEntries, podUID)

	machineState, err := state.GenerateMachineStateFromPodEntries(p.qrmConfig, getAllNICs(p.nicManager), podEntries, p.state.GetReservedBandwidth())
	if err != nil {
		general.Errorf("pod: %s, GenerateMachineStateFromPodEntries failed with error: %v", podUID, err)
		return fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	p.state.SetPodEntries(podEntries, false)
	p.state.SetMachineState(machineState, false)

	err = p.state.StoreState()
	if err != nil {
		general.Errorf("store state failed with error: %v", err)
		return err
	}

	err = p.generateAndApplyGroups()
	if err != nil {
		general.Errorf("generateAndApplyGroups failed with error: %v", err)
		return err
	}

	return nil
}

func (p *StaticPolicy) getNetClassID(podAnnotations map[string]string, podLevelNetClassAnnoKey, qosLevel string) (uint32, error) {
	isPodLevelNetClassExist, classID, err := qos.GetPodNetClassID(podAnnotations, podLevelNetClassAnnoKey)
	if err != nil {
		return 0, err
	}
	if isPodLevelNetClassExist {
		return classID, nil
	}

	return p.getNetClassIDByQoSLevel(qosLevel)
}

func (p *StaticPolicy) getNetClassIDByQoSLevel(qosLevel string) (uint32, error) {
	if netClsID, found := p.qosLevelToNetClassMap[qosLevel]; found {
		return netClsID, nil
	} else {
		return 0, fmt.Errorf("netClsID for qosLevel: %s isn't found", qosLevel)
	}
}

func (p *StaticPolicy) getNICByName(nics []machine.InterfaceInfo, ifName string) machine.InterfaceInfo {
	for idx := range nics {
		if nics[idx].Iface == ifName {
			return nics[idx]
		}
	}

	return machine.InterfaceInfo{}
}

// return the Socket id/index that the specified NIC attached to
func (p *StaticPolicy) getSocketIDByNIC(nic machine.InterfaceInfo) (int, error) {
	socketIDs, ok := p.agentCtx.IfIndex2Sockets[nic.IfIndex]
	if !ok {
		return -1, fmt.Errorf("failed to find the associated socket ID for the specified NIC %s - numanode: %d, ifIndex: %d, ifIndex2Sockets: %v",
			nic.Iface, nic.NumaNode, nic.IfIndex, p.agentCtx.IfIndex2Sockets)
	}

	return socketIDs[0], nil
}

// clearResidualNetClass clear residual net class in cgroupv2 environment
func (p *StaticPolicy) clearResidualNetClass(activeNetClsData map[uint64]*common.NetClsData) {
	netClassList, err := p.metaServer.ExternalManager.ListNetClass()
	if err != nil {
		general.Errorf("list net class failed, err: %v", err)
		return
	}
	general.Infof("total %d net class list", len(netClassList))

	now := time.Now()
	residualNetClass := make(map[uint64]*common.NetClsData, len(netClassList))
	for _, netClass := range netClassList {
		residualNetClass[netClass.CgroupID] = netClass
		if _, ok := activeNetClsData[netClass.CgroupID]; !ok {
			lastAliveTime, alive := p.aliveCgroupID[netClass.CgroupID]
			if alive && now.Before(lastAliveTime.Add(residualNetClassTTL)) &&
				len(netClassList) < maxResidualNetClassNum/2 {
				continue
			}
			go func(cgID uint64) {
				general.Infof("clear residual net class, cgID: %v", cgID)
				if err := p.metaServer.ExternalManager.ClearNetClass(cgID); err != nil {
					general.Errorf("delete net class failed, cgID: %v, err: %v", cgID, err)
					return
				}
			}(netClass.CgroupID)
		} else {
			p.aliveCgroupID[netClass.CgroupID] = now
		}
	}

	for cgID := range p.aliveCgroupID {
		if _, ok := residualNetClass[cgID]; !ok {
			delete(p.aliveCgroupID, cgID)
		}
	}
}

func (p *StaticPolicy) generateLowPriorityGroup() error {
	lowPriorityGroups := make(map[string]*qrmgeneral.NetworkGroup)
	machineState := p.state.GetMachineState()
	nics := getAllNICs(p.nicManager)

	for nicName, nicState := range machineState {
		groupName := getGroupName(nicName, LowPriorityGroupNameSuffix)
		// [TODO] since getNICByName has alreday been used,
		// we also assume nic name is unique here.
		// But if the assumption is broken, we should reconsider logic here.
		lowPriorityGroups[groupName] = &qrmgeneral.NetworkGroup{
			Egress: nicState.EgressState.Allocatable,
		}

		negtive := false
		for podUID, containerEntries := range nicState.PodEntries {
			for containerName, allocationInfo := range containerEntries {
				if allocationInfo == nil {
					general.Warningf("nil allocationInfo")
					continue
				}

				if allocationInfo.QoSLevel == apiconsts.PodAnnotationQoSLevelReclaimedCores {
					if allocationInfo.NetClassID != "" {
						lowPriorityGroups[groupName].NetClassIDs = append(lowPriorityGroups[groupName].NetClassIDs, allocationInfo.NetClassID)
					}
				} else {
					requestedEgress, err := allocationInfo.GetRequestedEgress()
					if err != nil {
						return fmt.Errorf("GetRequestedEgress for pod: %s, container: %s failed with error: %v", podUID, containerName, err)
					}

					if !negtive && lowPriorityGroups[groupName].Egress > requestedEgress {
						lowPriorityGroups[groupName].Egress -= requestedEgress
					} else {
						negtive = true
					}
				}
			}
		}

		// [TODO] make 0.05 as option
		if negtive {
			lowPriorityGroups[groupName].Egress = uint32(float64(nicState.EgressState.Allocatable) * 0.05)
		} else {
			lowPriorityGroups[groupName].Egress = general.MaxUInt32(uint32(float64(nicState.EgressState.Allocatable)*0.05), lowPriorityGroups[groupName].Egress)
		}
		selectedNIC := p.getNICByName(nics, nicName)
		lowPriorityGroups[groupName].MergedIPv4 = strings.Join(selectedNIC.Addr.GetNICIPs(machine.IPVersionV4), IPsSeparator)
		lowPriorityGroups[groupName].MergedIPv6 = strings.Join(selectedNIC.Addr.GetNICIPs(machine.IPVersionV6), IPsSeparator)
	}

	general.Infof("old lowPriorityGroups: %+v, new lowPriorityGroups: %+v", p.lowPriorityGroups, lowPriorityGroups)

	p.lowPriorityGroups = lowPriorityGroups
	return nil
}

func (p *StaticPolicy) generateAndApplyGroups() error {
	err := p.generateLowPriorityGroup()

	if err != nil {
		return fmt.Errorf("generateLowPriorityGroup failed with error: %v", err)
	} else {
		err = p.applyNetworkGroupsFunc(p.lowPriorityGroups)

		if err != nil {
			return fmt.Errorf("applyGroups failed with error: %v", err)
		}
	}

	return nil
}

func getAllNICs(nicManager nic.NICManager) []machine.InterfaceInfo {
	nics := nicManager.GetNICs()
	return append(nics.HealthyNICs, nics.UnhealthyNICs...)
}
