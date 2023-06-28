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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	agentconfig "github.com/kubewharf/katalyst-core/pkg/config/agent"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const (
	// NetworkResourcePluginPolicyNameStatic is the policy name of static network resource plugin
	NetworkResourcePluginPolicyNameStatic = "static"

	NetworkPluginStateFileName = "network_plugin_state"

	// IPsSeparator is used to split merged IPs string
	IPsSeparator = ","
)

// StaticPolicy is the static network policy
type StaticPolicy struct {
	sync.Mutex

	name       string
	stopCh     chan struct{}
	started    bool
	qosConfig  *generic.QoSConfiguration
	qrmConfig  *qrm.QRMPluginsConfiguration
	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer
	agentCtx   *agent.GenericContext
	nics       []machine.InterfaceInfo
	state      state.State

	CgroupV2Env                                     bool
	qosLevelToNetClassMap                           map[string]uint32
	applyNetClassFunc                               func(podUID, containerID string, data *common.NetClsData) error
	podLevelNetClassAnnoKey                         string
	podLevelNetAttributesAnnoKeys                   []string
	ipv4ResourceAllocationAnnotationKey             string
	ipv6ResourceAllocationAnnotationKey             string
	netNSPathResourceAllocationAnnotationKey        string
	netInterfaceNameResourceAllocationAnnotationKey string
	netClassIDResourceAllocationAnnotationKey       string
	netBandwidthResourceAllocationAnnotationKey     string
}

// NewStaticPolicy returns a static network policy
func NewStaticPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string) (bool, agent.Component, error) {
	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: NetworkResourcePluginPolicyNameStatic,
	})

	// it is incorrect to reserve bandwidth on those diabled NICs.
	// we only count active NICs as available network devices and allocate bandwidth on them
	enabledNICs := filterNICsByAvailability(agentCtx.KatalystMachineInfo.ExtraNetworkInfo.Interface, nil, nil)

	// the NICs should be in order by interface name so that we can adopt specific policies for bandwidth reservation or allocation
	// e.g. reserve bandwidth for high-priority tasks on the first NIC
	sort.SliceStable(enabledNICs, func(i, j int) bool {
		return enabledNICs[i].Iface < enabledNICs[j].Iface
	})

	// we only support one spreading policy for now: reserve the bandwidth on the first NIC.
	// it would be configurable later
	reservation, err := GetReservedBandwidth(enabledNICs, conf.ReservedBandwidth, FirstNIC)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("getReservedBandwidth failed with error: %v", err)
	}

	stateImpl, err := state.NewCheckpointState(conf.QRMPluginsConfiguration, conf.GenericQRMPluginConfiguration.StateFileDirectory, NetworkPluginStateFileName,
		NetworkResourcePluginPolicyNameStatic, agentCtx.MachineInfo, enabledNICs, reservation, conf.SkipNetworkStateCorruption)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("NewCheckpointState failed with error: %v", err)
	}

	policyImplement := &StaticPolicy{
		nics:                  enabledNICs,
		qosConfig:             conf.QoSConfiguration,
		qrmConfig:             conf.QRMPluginsConfiguration,
		emitter:               wrappedEmitter,
		metaServer:            agentCtx.MetaServer,
		agentCtx:              agentCtx,
		state:                 stateImpl,
		stopCh:                make(chan struct{}),
		name:                  fmt.Sprintf("%s_%s", agentName, NetworkResourcePluginPolicyNameStatic),
		qosLevelToNetClassMap: make(map[string]uint32),
	}

	if common.CheckCgroup2UnifiedMode() {
		policyImplement.CgroupV2Env = true
		policyImplement.applyNetClassFunc = agentCtx.MetaServer.ExternalManager.ApplyNetClass
	} else {
		policyImplement.CgroupV2Env = false
		policyImplement.applyNetClassFunc = cgroupcmutils.ApplyNetClsForContainer
	}

	policyImplement.ApplyConfig(conf.StaticAgentConfiguration)

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(
		policyImplement, conf.QRMPluginSocketDirs, nil)
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

func (p *StaticPolicy) filterAvailableNICsByBandwidth(req *pluginapi.ResourceRequest) ([]machine.InterfaceInfo, error) {
	if req == nil {
		return nil, fmt.Errorf("filterNICsByBandwidth got nil req")
	}

	reqInt, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	machineState := p.state.GetMachineState()
	if len(machineState) == 0 || len(p.nics) == 0 {
		return nil, fmt.Errorf("filterNICsByBandwidth with 0 NIC")
	}

	filteredNICs := make([]machine.InterfaceInfo, 0, len(p.nics))
	// filter NICs by available bandwidth
	for _, iface := range p.nics {
		if machineState[iface.Iface].EgressState.Free >= uint32(reqInt) && machineState[iface.Iface].IngressState.Free >= uint32(reqInt) {
			filteredNICs = append(filteredNICs, iface)
		}
	}

	return filteredNICs, nil
}

func (p *StaticPolicy) calculateHints(req *pluginapi.ResourceRequest) (map[string]*pluginapi.ListOfTopologyHints, error) {
	hints := map[string]*pluginapi.ListOfTopologyHints{
		p.ResourceName(): {
			Hints: []*pluginapi.TopologyHint{},
		},
	}

	candidateNICs, err := p.selectNICsByReq(req)
	if err != nil {
		return hints, fmt.Errorf("failed to select available NICs: %v", err)
	}

	if len(candidateNICs) == 0 {
		general.InfoS("candidateNICs is empty",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName)
		return hints, nil
	}

	numasToHintMap := make(map[string]*pluginapi.TopologyHint)
	for _, nic := range candidateNICs {
		siblingNUMAs, err := machine.GetSiblingNUMAs(nic.NumaNode, p.agentCtx.CPUTopology)
		if err != nil {
			return nil, fmt.Errorf("get siblingNUMAs for nic: %s failed with error: %v", nic.Iface, err)
		}

		nicPreference, err := checkNICPreferenceOfReq(nic, req.Annotations)
		if err != nil {
			return nil, fmt.Errorf("checkNICPreferenceOfReq for nic: %s failed with error: %v", nic.Iface, err)
		}

		siblingNUMAsStr := siblingNUMAs.String()
		if numasToHintMap[siblingNUMAsStr] == nil {
			numasToHintMap[siblingNUMAsStr] = &pluginapi.TopologyHint{
				Nodes: siblingNUMAs.ToSliceUInt64(),
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

	if !isReqAffinityRestricted(req.Annotations) && !isReqNamespaceRestricted(req.Annotations) {
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

// GetTopologyHints returns hints of corresponding resources
func (p *StaticPolicy) GetTopologyHints(_ context.Context,
	req *pluginapi.ResourceRequest) (resp *pluginapi.ResourceHintsResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	reqInt, err := util.GetQuantityFromResourceReq(req)
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
			_ = p.emitter.StoreInt64(util.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw)
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

func (p *StaticPolicy) RemovePod(_ context.Context,
	req *pluginapi.RemovePodRequest) (*pluginapi.RemovePodResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}

	if err := p.removePod(req.PodUid); err != nil {
		general.ErrorS(err, "remove pod failed with error", "podUID", req.PodUid)
		return nil, err
	}

	return &pluginapi.RemovePodResponse{}, nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *StaticPolicy) GetResourcesAllocation(_ context.Context,
	req *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetResourcesAllocation got nil req")
	}

	p.Lock()
	defer p.Unlock()

	podResources := make(map[string]*pluginapi.ContainerResources)
	podEntries := p.state.GetPodEntries()
	for podUID, containerEntries := range podEntries {
		if podResources[podUID] == nil {
			podResources[podUID] = &pluginapi.ContainerResources{}
		}

		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}

			if podResources[podUID].ContainerResources == nil {
				podResources[podUID].ContainerResources = make(map[string]*pluginapi.ResourceAllocation)
			}

			podResources[podUID].ContainerResources[containerName] = &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(apiconsts.ResourceNetBandwidth): {
						// no ociproperty needed for network
						IsNodeResource:    false,
						IsScalarResource:  true,
						AllocatedQuantity: float64(allocationInfo.Egress),
						AllocationResult:  allocationInfo.NumaNodes.String(),
					},
				},
			}
		}
	}

	return &pluginapi.GetResourcesAllocationResponse{
		PodResources: podResources,
	}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as topology aware format
func (p *StaticPolicy) GetTopologyAwareResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareResourcesRequest) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	return &pluginapi.GetTopologyAwareResourcesResponse{}, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as topology aware format
func (p *StaticPolicy) GetTopologyAwareAllocatableResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareAllocatableResourcesRequest) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{}, nil
}

// GetResourcePluginOptions returns options to be communicated with Resource Manager
func (p *StaticPolicy) GetResourcePluginOptions(context.Context,
	*pluginapi.Empty) (*pluginapi.ResourcePluginOptions, error) {
	return &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: true,
		NeedReconcile:         false,
	}, nil
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
func (p *StaticPolicy) selectNICsByReq(req *pluginapi.ResourceRequest) ([]machine.InterfaceInfo, error) {
	candidateNICs := make([]machine.InterfaceInfo, 0, len(p.nics))

	availableNICs, err := p.filterAvailableNICsByBandwidth(req)
	if err != nil {
		return nil, fmt.Errorf("filterAvailableNICsByBandwidth failed with error: %v", err)
	}

	// this node can not meet the bandwidth request
	if len(availableNICs) == 0 {
		general.InfoS("nic list returned by filtereNICsByBandwidth is empty",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName)
		return candidateNICs, nil
	}

	availableNICs = filterNICsByNamespaceType(availableNICs, req, nil)
	// this node can not meet the ns + bandwidth request
	if len(availableNICs) == 0 {
		general.InfoS("nic list returned by filterNICsByNamespaceType is empty",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName)
		return candidateNICs, nil
	}

	// calculate the intersection
	candidateNICs = filterNICsByHint(availableNICs, req, p.agentCtx)

	// If filteredNICs and "hintNICs" indicated by req.Hint have an overlap, the intersection of two sets is what we need.
	// Otherwise, pick their difference (i.e. the NICs in filteredNICs but not in "hintNICs", which actually equals to the former set).
	if len(candidateNICs) > 0 {
		return candidateNICs, nil
	}

	return availableNICs, nil
}

func (p *StaticPolicy) getResourceAllocationAnnotations(podAnnotations map[string]string, selectedNIC machine.InterfaceInfo, reqBandwidth int) (map[string]string, error) {
	netClsID, err := p.getNetClassID(podAnnotations, p.podLevelNetClassAnnoKey)
	if err != nil {
		return nil, fmt.Errorf("getNetClassID failed with error: %v", err)
	}

	resourceAllocationAnnotations := map[string]string{
		p.ipv4ResourceAllocationAnnotationKey:             strings.Join(selectedNIC.GetNICIPs(machine.IPVersionV4), IPsSeparator),
		p.ipv6ResourceAllocationAnnotationKey:             strings.Join(selectedNIC.GetNICIPs(machine.IPVersionV6), IPsSeparator),
		p.netInterfaceNameResourceAllocationAnnotationKey: selectedNIC.Iface,
		p.netClassIDResourceAllocationAnnotationKey:       fmt.Sprintf("%d", netClsID),
		// TODO: support differentiated Egress/Ingress bandwidth later
		p.netBandwidthResourceAllocationAnnotationKey: strconv.Itoa(reqBandwidth),
	}

	if len(selectedNIC.NSAbsolutePath) > 0 {
		resourceAllocationAnnotations[p.netNSPathResourceAllocationAnnotationKey] = selectedNIC.NSAbsolutePath
	}

	return resourceAllocationAnnotations, nil
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
func (p *StaticPolicy) Allocate(_ context.Context,
	req *pluginapi.ResourceRequest) (resp *pluginapi.ResourceAllocationResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	// since qos config util will filter out annotation keys not related to katalyst QoS,
	// we copy original pod annotations here to use them later
	podAnnotations := maputil.CopySS(req.Annotations)

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	reqInt, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	general.InfoS("called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"qosLevel", qosLevel,
		"reqAnnotations", req.Annotations,
		"netBandwidthReq(Mbps)", reqInt)

	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameAllocateFailed, 1, metrics.MetricTypeNameRaw)
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
		// not to deal with sidcars, and return a trivial allocationResult to avoid re-allocating
		return emptyResponse, nil
	}

	candidateNICs, err := p.selectNICsByReq(req)
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

	// we only support one policy and hard code it for now, it would be configurable later
	selectedNIC := selectOneNIC(candidateNICs, LastOne)

	general.Infof("select NIC %s to allocate bandwidth (%dMbps)", selectedNIC.Iface, reqInt)

	resourceAllocationAnnotations, err := p.getResourceAllocationAnnotations(podAnnotations, selectedNIC, reqInt)
	if err != nil {
		err = fmt.Errorf("getResourceAllocationAnnotations for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	siblingNUMAs, err := machine.GetSiblingNUMAs(selectedNIC.NumaNode, p.agentCtx.CPUTopology)
	if err != nil {
		general.Errorf("get siblingNUMAs for nic: %s failed with error: %v. Incorrect NumaNodes in machineState allocationInfo", selectedNIC.Iface, err)
	}

	// generate the response hint
	// it could be different from the req.Hint if the affinitive NIC does not have sufficient bandwidth
	nicPreference, err := checkNICPreferenceOfReq(selectedNIC, req.Annotations)
	if err != nil {
		return nil, fmt.Errorf("checkNICPreferenceOfReq for nic: %s failed with error: %v", selectedNIC.Iface, err)
	}

	respHint := &pluginapi.TopologyHint{
		Nodes:     siblingNUMAs.ToSliceUInt64(),
		Preferred: nicPreference,
	}

	// generate allocationInfo and update the checkpoint accordingly
	machineState := p.state.GetMachineState()
	podEntries := p.state.GetPodEntries()
	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)

	if allocationInfo != nil {
		if allocationInfo.Egress >= uint32(reqInt) && allocationInfo.Ingress >= uint32(reqInt) {
			general.InfoS("already allocated and meet requirement",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"bandwidthReq(Mbps)", reqInt,
				"currentResult(Mbps)", allocationInfo.Egress)

			resp, packErr := packAllocationResponse(req, allocationInfo, req.Hint, resourceAllocationAnnotations)
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

			var stateErr error
			machineState, stateErr = state.GenerateMachineStateFromPodEntries(p.qrmConfig, p.nics, podEntries, p.state.GetReservedBandwidth())
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

	// update the machienState
	machineState[selectedNIC.Iface].EgressState.Allocated += uint32(reqInt)
	machineState[selectedNIC.Iface].IngressState.Allocated += uint32(reqInt)
	machineState[selectedNIC.Iface].EgressState.Free -= uint32(reqInt)
	machineState[selectedNIC.Iface].IngressState.Free -= uint32(reqInt)

	if machineState[selectedNIC.Iface].PodEntries == nil {
		machineState[selectedNIC.Iface].PodEntries = make(state.PodEntries)
	}

	if machineState[selectedNIC.Iface].PodEntries[req.PodUid] == nil {
		machineState[selectedNIC.Iface].PodEntries[req.PodUid] = make(state.ContainerEntries)
	}

	newAllocation := &state.AllocationInfo{
		PodUid:         req.PodUid,
		PodNamespace:   req.PodNamespace,
		PodName:        req.PodName,
		ContainerName:  req.ContainerName,
		ContainerType:  req.ContainerType.String(),
		ContainerIndex: req.ContainerIndex,
		PodRole:        req.PodRole,
		PodType:        req.PodType,
		Egress:         uint32(reqInt),
		Ingress:        uint32(reqInt),
		IfName:         selectedNIC.Iface,
		NumaNodes:      siblingNUMAs,
		Labels:         general.DeepCopyMap(req.Labels),
		Annotations:    general.DeepCopyMap(req.Annotations),
	}

	machineState[selectedNIC.Iface].PodEntries[req.PodUid][req.ContainerName] = newAllocation

	// update state cache
	p.state.SetAllocationInfo(req.PodUid, req.ContainerName, newAllocation)
	p.state.SetMachineState(machineState)

	return packAllocationResponse(req, newAllocation, respHint, resourceAllocationAnnotations)
}

// PreStartContainer is called, if indicated by resource plugin during registration phase,
// before each container start. Resource plugin can run resource specific operations
// such as resetting the resource before making resources available to the container
func (p *StaticPolicy) PreStartContainer(context.Context,
	*pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}

func (p *StaticPolicy) applyNetClass() {
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
		if pod == nil {
			general.Errorf("get nil pod from metaServer")
			continue
		}

		classID, err := p.getNetClassID(pod.GetAnnotations(), p.podLevelNetClassAnnoKey)
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

func (p *StaticPolicy) removePod(podUID string) error {
	if p.CgroupV2Env {
		cgIDList, err := p.metaServer.ExternalManager.ListCgroupIDsForPod(podUID)
		if err != nil {
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

	machineState, err := state.GenerateMachineStateFromPodEntries(p.qrmConfig, p.nics, podEntries, p.state.GetReservedBandwidth())
	if err != nil {
		general.Errorf("pod: %s, GenerateMachineStateFromPodEntries failed with error: %v", podUID, err)
		return fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	p.state.SetPodEntries(podEntries)
	p.state.SetMachineState(machineState)

	return nil
}

func (p *StaticPolicy) getNetClassID(podAnnotations map[string]string, podLevelNetClassAnnoKey string) (uint32, error) {
	isPodLevelNetClassExist, classID, err := qos.GetPodNetClassID(podAnnotations, podLevelNetClassAnnoKey)
	if err != nil {
		return 0, err
	}
	if isPodLevelNetClassExist {
		return classID, nil
	}

	qosLevel, err := p.qosConfig.GetQoSLevel(podAnnotations)
	if err != nil {
		return 0, err
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
