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
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"
	"k8s.io/utils/clock"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	memconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/oom"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/handlers/fragmem"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/handlers/logcache"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/handlers/sockmem"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/asyncworker"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
	"github.com/kubewharf/katalyst-core/pkg/util/timemonitor"
)

const (
	MemoryResourcePluginPolicyNameDynamic = string(apiconsts.ResourcePluginPolicyNameDynamic)

	memoryPluginStateFileName                    = "memory_plugin_state"
	memoryPluginAsyncWorkersName                 = "qrm_memory_plugin_async_workers"
	memoryPluginAsyncWorkTopicDropCache          = "qrm_memory_plugin_drop_cache"
	memoryPluginAsyncWorkTopicSetExtraCGMemLimit = "qrm_memory_plugin_set_extra_mem_limit"
	memoryPluginAsyncWorkTopicMovePage           = "qrm_memory_plugin_move_page"
	memoryPluginAsyncWorkTopicMemoryOffloading   = "qrm_memory_plugin_mem_offload"

	dropCacheTimeoutSeconds          = 30
	setExtraCGMemLimitTimeoutSeconds = 60
)

const (
	memsetCheckPeriod          = 10 * time.Second
	stateCheckPeriod           = 30 * time.Second
	maxResidualTime            = 5 * time.Minute
	setMemoryMigratePeriod     = 5 * time.Second
	applyCgroupPeriod          = 5 * time.Second
	setExtraControlKnobsPeriod = 5 * time.Second
	clearOOMPriorityPeriod     = 1 * time.Hour
	syncOOMPriorityPeriod      = 5 * time.Second

	healthCheckTolerationTimes = 3
	dropCacheGracePeriod       = 60 * time.Second

	defaultAsyncWorkLimit = 8
	movePagesWorkLimit    = 2
)

type DynamicPolicy struct {
	sync.RWMutex

	stopCh                  chan struct{}
	started                 bool
	qosConfig               *generic.QoSConfiguration
	extraControlKnobConfigs commonstate.ExtraControlKnobConfigs

	// emitter is used to emit metrics.
	// metaServer is used to collect metadata universal metaServer.
	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer

	advisorClient  advisorsvc.AdvisorServiceClient
	advisorConn    *grpc.ClientConn
	advisorMonitor *timemonitor.TimeMonitor

	topology *machine.CPUTopology
	state    state.State

	migrateMemoryLock sync.Mutex
	migratingMemory   map[string]map[string]bool
	residualHitMap    map[string]int64

	allocationHandlers  map[string]util.AllocationHandler
	hintHandlers        map[string]util.HintHandler
	enhancementHandlers util.ResourceEnhancementHandlerMap

	extraStateFileAbsPath string
	name                  string

	podDebugAnnoKeys      []string
	podAnnotationKeptKeys []string
	podLabelKeptKeys      []string

	asyncWorkers *asyncworker.AsyncWorkers
	// defaultAsyncLimitedWorkers is general workers with default limit.
	// asyncLimitedWorkersMap is workers map for plugin can define its own limit.
	defaultAsyncLimitedWorkers *asyncworker.AsyncLimitedWorkers
	asyncLimitedWorkersMap     map[string]*asyncworker.AsyncLimitedWorkers

	enableSettingMemoryMigrate bool
	enableSettingSockMem       bool
	enableSettingFragMem       bool
	enableMemoryAdvisor        bool
	memoryAdvisorSocketAbsPath string
	memoryPluginSocketAbsPath  string

	enableOOMPriority        bool
	oomPriorityMapPinnedPath string
	oomPriorityMapLock       sync.Mutex
	oomPriorityMap           *ebpf.Map

	enableEvictingLogCache  bool
	logCacheEvictionManager logcache.Manager

	enableNonBindingShareCoresMemoryResourceCheck bool
}

func NewDynamicPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	reservedMemory, err := getReservedMemory(conf, agentCtx.MetaServer, agentCtx.MachineInfo)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("getReservedMemoryFromOptions failed with error: %v", err)
	}

	resourcesReservedMemory := map[v1.ResourceName]map[int]uint64{
		v1.ResourceMemory: reservedMemory,
	}
	stateImpl, err := state.NewCheckpointState(conf.GenericQRMPluginConfiguration.StateFileDirectory, memoryPluginStateFileName,
		memconsts.MemoryResourcePluginPolicyNameDynamic, agentCtx.CPUTopology, agentCtx.MachineInfo, resourcesReservedMemory, conf.SkipMemoryStateCorruption)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("NewCheckpointState failed with error: %v", err)
	}

	extraControlKnobConfigs := make(commonstate.ExtraControlKnobConfigs)
	if len(conf.ExtraControlKnobConfigFile) > 0 {
		extraControlKnobConfigs, err = commonstate.LoadExtraControlKnobConfigs(conf.ExtraControlKnobConfigFile)
		if err != nil {
			return false, agent.ComponentStub{}, fmt.Errorf("loadExtraControlKnobConfigs failed with error: %v", err)
		}
	} else {
		general.Infof("empty ExtraControlKnobConfigFile, initialize empty extraControlKnobConfigs")
	}

	state.SetReadonlyState(stateImpl)
	state.SetReadWriteState(stateImpl)

	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: memconsts.MemoryResourcePluginPolicyNameDynamic,
	})

	policyImplement := &DynamicPolicy{
		topology:                   agentCtx.CPUTopology,
		qosConfig:                  conf.QoSConfiguration,
		emitter:                    wrappedEmitter,
		metaServer:                 agentCtx.MetaServer,
		state:                      stateImpl,
		stopCh:                     make(chan struct{}),
		migratingMemory:            make(map[string]map[string]bool),
		residualHitMap:             make(map[string]int64),
		enhancementHandlers:        make(util.ResourceEnhancementHandlerMap),
		extraStateFileAbsPath:      conf.ExtraStateFileAbsPath,
		name:                       fmt.Sprintf("%s_%s", agentName, memconsts.MemoryResourcePluginPolicyNameDynamic),
		podDebugAnnoKeys:           conf.PodDebugAnnoKeys,
		podAnnotationKeptKeys:      conf.PodAnnotationKeptKeys,
		podLabelKeptKeys:           conf.PodLabelKeptKeys,
		asyncWorkers:               asyncworker.NewAsyncWorkers(memoryPluginAsyncWorkersName, wrappedEmitter),
		defaultAsyncLimitedWorkers: asyncworker.NewAsyncLimitedWorkers(memoryPluginAsyncWorkersName, defaultAsyncWorkLimit, wrappedEmitter),
		enableSettingMemoryMigrate: conf.EnableSettingMemoryMigrate,
		enableSettingSockMem:       conf.EnableSettingSockMem,
		enableSettingFragMem:       conf.EnableSettingFragMem,
		enableMemoryAdvisor:        conf.EnableMemoryAdvisor,
		memoryAdvisorSocketAbsPath: conf.MemoryAdvisorSocketAbsPath,
		memoryPluginSocketAbsPath:  conf.MemoryPluginSocketAbsPath,
		extraControlKnobConfigs:    extraControlKnobConfigs, // [TODO]: support modifying extraControlKnobConfigs by KCC
		enableOOMPriority:          conf.EnableOOMPriority,
		oomPriorityMapPinnedPath:   conf.OOMPriorityPinnedMapAbsPath,
		enableEvictingLogCache:     conf.EnableEvictingLogCache,
		enableNonBindingShareCoresMemoryResourceCheck: conf.EnableNonBindingShareCoresMemoryResourceCheck,
	}

	policyImplement.allocationHandlers = map[string]util.AllocationHandler{
		apiconsts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresAllocationHandler,
		apiconsts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresAllocationHandler,
		apiconsts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresAllocationHandler,
		apiconsts.PodAnnotationQoSLevelSystemCores:    policyImplement.systemCoresAllocationHandler,
	}

	policyImplement.hintHandlers = map[string]util.HintHandler{
		apiconsts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresHintHandler,
		apiconsts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresHintHandler,
		apiconsts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresHintHandler,
		apiconsts.PodAnnotationQoSLevelSystemCores:    policyImplement.systemCoresHintHandler,
	}

	policyImplement.asyncLimitedWorkersMap = map[string]*asyncworker.AsyncLimitedWorkers{
		memoryPluginAsyncWorkTopicMovePage: asyncworker.NewAsyncLimitedWorkers(memoryPluginAsyncWorkTopicMovePage, movePagesWorkLimit, wrappedEmitter),
	}

	if policyImplement.enableOOMPriority {
		policyImplement.enhancementHandlers.Register(apiconsts.QRMPhaseRemovePod,
			apiconsts.PodAnnotationMemoryEnhancementOOMPriority, policyImplement.clearOOMPriority)
	}

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(policyImplement, conf.QRMPluginSocketDirs,
		func(key string, value int64) {
			_ = wrappedEmitter.StoreInt64(key, value, metrics.MetricTypeNameRaw)
		})
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy new plugin wrapper failed with error: %v", err)
	}

	memoryadvisor.RegisterControlKnobHandler(memoryadvisor.ControlKnobKeyMemoryLimitInBytes,
		memoryadvisor.ControlKnobHandlerWithChecker(policyImplement.handleAdvisorMemoryLimitInBytes))
	memoryadvisor.RegisterControlKnobHandler(memoryadvisor.ControlKnobKeyCPUSetMems,
		memoryadvisor.ControlKnobHandlerWithChecker(handleAdvisorCPUSetMems))
	memoryadvisor.RegisterControlKnobHandler(memoryadvisor.ControlKnobKeyDropCache,
		memoryadvisor.ControlKnobHandlerWithChecker(policyImplement.handleAdvisorDropCache))
	memoryadvisor.RegisterControlKnobHandler(memoryadvisor.ControlKnobReclaimedMemorySize,
		memoryadvisor.ControlKnobHandlerWithChecker(policyImplement.handleAdvisorMemoryProvisions))
	memoryadvisor.RegisterControlKnobHandler(memoryadvisor.ControlKnobKeyBalanceNumaMemory,
		memoryadvisor.ControlKnobHandlerWithChecker(policyImplement.handleNumaMemoryBalance))
	memoryadvisor.RegisterControlKnobHandler(memoryadvisor.ControlKnowKeyMemoryOffloading,
		memoryadvisor.ControlKnobHandlerWithChecker(policyImplement.handleAdvisorMemoryOffloading))

	if policyImplement.enableEvictingLogCache {
		policyImplement.logCacheEvictionManager = logcache.NewManager(conf, agentCtx.MetaServer)
	}
	return true, &agent.PluginWrapper{GenericPlugin: pluginWrapper}, nil
}

func (p *DynamicPolicy) registerControlKnobHandlerCheckRules() {
	general.RegisterReportCheck(memconsts.DropCache, dropCacheGracePeriod)
}

func (p *DynamicPolicy) Start() (err error) {
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

	p.registerControlKnobHandlerCheckRules()
	go wait.Until(func() {
		_ = p.emitter.StoreInt64(util.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
	}, time.Second*30, p.stopCh)

	err = periodicalhandler.RegisterPeriodicalHandlerWithHealthz(memconsts.ClearResidualState,
		general.HealthzCheckStateNotReady, qrm.QRMMemoryPluginPeriodicalHandlerGroupName,
		p.clearResidualState, stateCheckPeriod, healthCheckTolerationTimes)
	if err != nil {
		general.Errorf("start %v failed, err: %v", memconsts.ClearResidualState, err)
	}

	// TODO: we should remove this healthy check when we support inplace update resize in all clusters.
	syncMemoryStatusFromSpec := func(_ *config.Configuration,
		_ interface{},
		_ *dynamicconfig.DynamicAgentConfiguration,
		_ metrics.MetricEmitter,
		_ *metaserver.MetaServer,
	) {
		p.Lock()
		defer func() {
			p.Unlock()
		}()
		if err := p.adjustAllocationEntries(); err != nil {
			general.Warningf("failed to sync memory state from pod spec: %q", err)
		} else {
			general.Warningf("sync memory state from pod spec successfully")
		}
	}
	err = periodicalhandler.RegisterPeriodicalHandler(qrm.QRMMemoryPluginPeriodicalHandlerGroupName, memconsts.SyncMemoryStateFromSpec,
		syncMemoryStatusFromSpec, stateCheckPeriod)
	if err != nil {
		general.Errorf("start %v failed, err: %v", memconsts.SyncMemoryStateFromSpec, err)
	}

	err = periodicalhandler.RegisterPeriodicalHandlerWithHealthz(memconsts.CheckMemSet, general.HealthzCheckStateNotReady,
		qrm.QRMMemoryPluginPeriodicalHandlerGroupName, p.checkMemorySet, memsetCheckPeriod, healthCheckTolerationTimes)
	if err != nil {
		general.Errorf("start %v failed, err: %v", memconsts.CheckMemSet, err)
	}

	err = periodicalhandler.RegisterPeriodicalHandlerWithHealthz(memconsts.ApplyExternalCGParams, general.HealthzCheckStateNotReady,
		qrm.QRMMemoryPluginPeriodicalHandlerGroupName, p.applyExternalCgroupParams, applyCgroupPeriod, healthCheckTolerationTimes)
	if err != nil {
		general.Errorf("start %v failed, err: %v", memconsts.ApplyExternalCGParams, err)
	}

	err = periodicalhandler.RegisterPeriodicalHandlerWithHealthz(memconsts.SetExtraControlKnob, general.HealthzCheckStateNotReady,
		qrm.QRMMemoryPluginPeriodicalHandlerGroupName, p.setExtraControlKnobByConfigs, setExtraControlKnobsPeriod, healthCheckTolerationTimes)
	if err != nil {
		general.Errorf("start %v failed, err: %v", memconsts.SetExtraControlKnob, err)
	}

	err = p.asyncWorkers.Start(p.stopCh)
	if err != nil {
		general.Errorf("start async worker failed, err: %v", err)
	}
	err = p.defaultAsyncLimitedWorkers.Start(p.stopCh)
	if err != nil {
		general.Errorf("start async limited worker failed, err: %v", err)
	}

	for name, workers := range p.asyncLimitedWorkersMap {
		err = workers.Start(p.stopCh)
		if err != nil {
			general.Errorf("start async limited worker for plugin %s failed, err: %v", name, err)
		}
	}

	if p.enableSettingMemoryMigrate {
		general.Infof("setMemoryMigrate enabled")
		go wait.Until(p.setMemoryMigrate, setMemoryMigratePeriod, p.stopCh)
	}

	if p.enableOOMPriority {
		general.Infof("OOM priority enabled")
		go p.PollOOMBPFInit(p.stopCh)

		err := periodicalhandler.RegisterPeriodicalHandler(qrm.QRMMemoryPluginPeriodicalHandlerGroupName,
			oom.ClearResidualOOMPriorityPeriodicalHandlerName, p.clearResidualOOMPriority, clearOOMPriorityPeriod)
		if err != nil {
			general.Infof("register clearResidualOOMPriority failed, err=%v", err)
		}

		err = periodicalhandler.RegisterPeriodicalHandlerWithHealthz(memconsts.OOMPriority, general.HealthzCheckStateNotReady,
			qrm.QRMMemoryPluginPeriodicalHandlerGroupName, p.syncOOMPriority, syncOOMPriorityPeriod, healthCheckTolerationTimes)
		if err != nil {
			general.Infof("register syncOOMPriority failed, err=%v", err)
		}
	}

	if p.enableSettingSockMem {
		general.Infof("setSockMem enabled")
		err := periodicalhandler.RegisterPeriodicalHandlerWithHealthz(memconsts.SetSockMem,
			general.HealthzCheckStateNotReady, qrm.QRMMemoryPluginPeriodicalHandlerGroupName,
			sockmem.SetSockMemLimit, 120*time.Second, healthCheckTolerationTimes)
		if err != nil {
			general.Infof("setSockMem failed, err=%v", err)
		}
	}

	if p.enableEvictingLogCache {
		general.Infof("evictLogCache enabled")
		err := periodicalhandler.RegisterPeriodicalHandlerWithHealthz(memconsts.EvictLogCache,
			general.HealthzCheckStateNotReady, qrm.QRMMemoryPluginPeriodicalHandlerGroupName,
			p.logCacheEvictionManager.EvictLogCache, 600*time.Second, healthCheckTolerationTimes)
		if err != nil {
			general.Errorf("evictLogCache failed, err=%v", err)
		}
	}
	if p.enableSettingFragMem {
		general.Infof("setFragMem enabled")
		err := periodicalhandler.RegisterPeriodicalHandlerWithHealthz(memconsts.SetMemCompact,
			general.HealthzCheckStateNotReady, qrm.QRMMemoryPluginPeriodicalHandlerGroupName,
			fragmem.SetMemCompact, 1800*time.Second, healthCheckTolerationTimes)
		if err != nil {
			general.Infof("setFragMem failed, err=%v", err)
		}
	}

	go wait.Until(func() {
		periodicalhandler.ReadyToStartHandlersByGroup(qrm.QRMMemoryPluginPeriodicalHandlerGroupName)
	}, 5*time.Second, p.stopCh)

	if !p.enableMemoryAdvisor {
		general.Infof("start dynamic policy memory plugin without memory advisor")
		return nil
	} else if p.memoryAdvisorSocketAbsPath == "" {
		return fmt.Errorf("invalid memoryAdvisorSocketAbsPath: %s", p.memoryAdvisorSocketAbsPath)
	}

	general.Infof("start dynamic policy memory plugin with memory advisor")
	err = p.initAdvisorClientConn()
	if err != nil {
		general.Errorf("initAdvisorClientConn failed with error: %v", err)
		return
	}

	p.advisorMonitor, err = timemonitor.NewTimeMonitor(memoryAdvisorHealthMonitorName, memoryAdvisorHealthMonitorInterval,
		memoryAdvisorUnhealthyThreshold, memoryAdvisorHealthyThreshold,
		util.MetricNameAdvisorUnhealthy, p.emitter, memoryAdvisorHealthyCount, true)
	if err != nil {
		general.Errorf("initialize memory advisor monitor failed with error: %v", err)
		return
	}
	go p.advisorMonitor.Run(p.stopCh)

	go wait.BackoffUntil(func() { p.serveForAdvisor(p.stopCh) }, wait.NewExponentialBackoffManager(
		800*time.Millisecond, 30*time.Second, 2*time.Minute, 2.0, 0, &clock.RealClock{}), true, p.stopCh)

	communicateWithMemoryAdvisorServer := func() {
		general.Infof("waiting memory plugin checkpoint server serving confirmation")
		if conn, err := process.Dial(p.memoryPluginSocketAbsPath, 5*time.Second); err != nil {
			general.Errorf("dial check at socket: %s failed with err: %v", p.memoryPluginSocketAbsPath, err)
			return
		} else {
			_ = conn.Close()
		}
		general.Infof("memory plugin checkpoint server serving confirmed")

		// keep compatible to old version sys advisor not supporting list containers from memory plugin
		err = p.pushMemoryAdvisor()
		if err != nil {
			general.Errorf("sync existing containers to memory advisor failed with error: %v", err)
			return
		}

		// call lw of MemoryAdvisorServer and do allocation
		if err := p.lwMemoryAdvisorServer(p.stopCh); err != nil {
			general.Errorf("lwMemoryAdvisorServer failed with error: %v", err)
		} else {
			general.Infof("lwMemoryAdvisorServer finished")
		}
	}

	general.RegisterHeartbeatCheck(memconsts.CommunicateWithAdvisor, 2*time.Minute, general.HealthzCheckStateNotReady,
		2*time.Minute)
	go wait.BackoffUntil(communicateWithMemoryAdvisorServer, wait.NewExponentialBackoffManager(800*time.Millisecond,
		30*time.Second, 2*time.Minute, 2.0, 0, &clock.RealClock{}), true, p.stopCh)

	return nil
}

func (p *DynamicPolicy) Stop() error {
	p.Lock()
	defer func() {
		p.oomPriorityMap.Close()
		p.started = false
		p.Unlock()
		general.Warningf("stopped")
	}()

	if !p.started {
		general.Warningf("already stopped")
		return nil
	}
	close(p.stopCh)

	periodicalhandler.StopHandlersByGroup(qrm.QRMMemoryPluginPeriodicalHandlerGroupName)

	return nil
}

func (p *DynamicPolicy) Name() string {
	return p.name
}

func (p *DynamicPolicy) ResourceName() string {
	return string(v1.ResourceMemory)
}

// GetTopologyHints returns hints of corresponding resources
func (p *DynamicPolicy) GetTopologyHints(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	// identify if the pod is a debug pod,
	// if so, apply specific strategy to it.
	// since GetKatalystQoSLevelFromResourceReq function will filter annotations,
	// we should do it before GetKatalystQoSLevelFromResourceReq.
	isDebugPod := util.IsDebugPod(req.Annotations, p.podDebugAnnoKeys)

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
		"podType", req.PodType,
		"podRole", req.PodRole,
		"containerType", req.ContainerType,
		"qosLevel", qosLevel,
		"memoryReq(bytes)", reqInt,
		"isDebugPod", isDebugPod)

	if req.ContainerType == pluginapi.ContainerType_INIT || isDebugPod {
		general.Infof("there is no NUMA preference, return nil hint")
		return util.PackResourceHintsResponse(req, string(v1.ResourceMemory),
			map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceMemory): nil,
			})
	}

	p.RLock()
	defer func() {
		p.RUnlock()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw)
		}
	}()

	if p.hintHandlers[qosLevel] == nil {
		return nil, fmt.Errorf("katalyst QoS level: %s is not supported yet", qosLevel)
	}
	return p.hintHandlers[qosLevel](ctx, req)
}

// GetPodTopologyHints returns hints of corresponding resources for pod
func (p *DynamicPolicy) GetPodTopologyHints(ctx context.Context,
	req *pluginapi.PodResourceRequest,
) (*pluginapi.PodResourceHintsResponse, error) {
	return nil, util.ErrNotImplemented
}

func (p *DynamicPolicy) RemovePod(ctx context.Context,
	req *pluginapi.RemovePodRequest,
) (resp *pluginapi.RemovePodResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}

	general.InfoS("called", "podUID", req.PodUid)

	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameRemovePodFailed, 1, metrics.MetricTypeNameRaw)
		}
	}()

	for lastLevelEnhancementKey, handler := range p.enhancementHandlers[apiconsts.QRMPhaseRemovePod] {
		if p.hasLastLevelEnhancementKey(lastLevelEnhancementKey, req.PodUid) {
			herr := handler(ctx, p.emitter, p.metaServer, req,
				p.state.GetPodResourceEntries())
			if herr != nil {
				return &pluginapi.RemovePodResponse{}, herr
			}
		}
	}

	if p.enableMemoryAdvisor {
		_, err = p.advisorClient.RemovePod(ctx, &advisorsvc.RemovePodRequest{PodUid: req.PodUid})
		if err != nil {
			return nil, fmt.Errorf("remove pod in QoS aware server failed with error: %v", err)
		}
	}

	err = p.removePod(req.PodUid)
	if err != nil {
		general.ErrorS(err, "remove pod failed with error", "podUID", req.PodUid)
		_ = p.emitter.StoreInt64(util.MetricNameRemovePodFailed, 1, metrics.MetricTypeNameRaw)
		return nil, err
	}

	aErr := p.adjustAllocationEntries()
	if aErr != nil {
		general.ErrorS(aErr, "adjustAllocationEntries failed", "podUID", req.PodUid)
	}

	return &pluginapi.RemovePodResponse{}, nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *DynamicPolicy) GetResourcesAllocation(_ context.Context,
	req *pluginapi.GetResourcesAllocationRequest,
) (*pluginapi.GetResourcesAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetResourcesAllocation got nil req")
	}

	p.RLock()
	defer p.RUnlock()

	podResources := make(map[string]*pluginapi.ContainerResources)
	podEntries := p.state.GetPodResourceEntries()[v1.ResourceMemory]
	needUpdateMachineState := false
	for podUID, containerEntries := range podEntries {
		if podResources[podUID] == nil {
			podResources[podUID] = &pluginapi.ContainerResources{}
		}

		mainContainerAllocationInfo, _ := podEntries.GetMainContainerAllocation(podUID)
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}

			if allocationInfo.CheckSideCar() && mainContainerAllocationInfo != nil {
				if applySidecarAllocationInfoFromMainContainer(allocationInfo, mainContainerAllocationInfo) {
					general.Infof("pod: %s/%s sidecar container: %s update its allocation",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
					p.state.SetAllocationInfo(v1.ResourceMemory, podUID, containerName, allocationInfo)
					needUpdateMachineState = true
				}
			}

			if podResources[podUID].ContainerResources == nil {
				podResources[podUID].ContainerResources = make(map[string]*pluginapi.ResourceAllocation)
			}

			var err error
			podResources[podUID].ContainerResources[containerName], err = allocationInfo.GetResourceAllocation()
			if err != nil {
				errMsg := "allocationInfo.GetResourceAllocation failed"
				general.ErrorS(err, errMsg,
					"podNamespace", allocationInfo.PodNamespace,
					"podName", allocationInfo.PodName,
					"containerName", allocationInfo.ContainerName)
				return nil, fmt.Errorf(errMsg)
			}
		}
	}

	if needUpdateMachineState {
		general.Infof("GetResourcesAllocation update machine state")
		podResourceEntries := p.state.GetPodResourceEntries()
		resourcesState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
		if err != nil {
			general.Infof("GetResourcesAllocation GenerateMachineStateFromPodEntries failed with error: %v", err)
			return nil, fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
		}
		p.state.SetMachineState(resourcesState)
	}

	return &pluginapi.GetResourcesAllocationResponse{
		PodResources: podResources,
	}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as topology aware format
func (p *DynamicPolicy) GetTopologyAwareResources(_ context.Context,
	req *pluginapi.GetTopologyAwareResourcesRequest,
) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyAwareResources got nil req")
	}

	p.RLock()
	defer p.RUnlock()

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo == nil {
		return nil, fmt.Errorf("pod: %s, container: %s is not show up in memory plugin state", req.PodUid, req.ContainerName)
	}

	topologyAwareQuantityList := util.GetTopologyAwareQuantityFromAssignmentsSize(allocationInfo.TopologyAwareAllocations)
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
			string(v1.ResourceMemory): {
				IsNodeResource:                    false,
				IsScalarResource:                  true,
				AggregatedQuantity:                0,
				OriginalAggregatedQuantity:        0,
				TopologyAwareQuantityList:         nil,
				OriginalTopologyAwareQuantityList: nil,
			},
		}
	} else {
		resp.ContainerTopologyAwareResources.AllocatedResources = map[string]*pluginapi.TopologyAwareResource{
			string(v1.ResourceMemory): {
				IsNodeResource:                    false,
				IsScalarResource:                  true,
				AggregatedQuantity:                float64(allocationInfo.AggregatedQuantity),
				OriginalAggregatedQuantity:        float64(allocationInfo.AggregatedQuantity),
				TopologyAwareQuantityList:         topologyAwareQuantityList,
				OriginalTopologyAwareQuantityList: topologyAwareQuantityList,
			},
		}
	}

	return resp, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as topology aware format
func (p *DynamicPolicy) GetTopologyAwareAllocatableResources(context.Context,
	*pluginapi.GetTopologyAwareAllocatableResourcesRequest,
) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	p.RLock()
	defer p.RUnlock()

	machineState := p.state.GetMachineState()[v1.ResourceMemory]

	numaNodes := p.topology.CPUDetails.NUMANodes().ToSliceInt()
	topologyAwareAllocatableQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))
	topologyAwareCapacityQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))

	var aggregatedAllocatableQuantity, aggregatedCapacityQuantity uint64 = 0, 0
	for _, numaNode := range numaNodes {
		numaNodeState := machineState[numaNode]
		if numaNodeState == nil {
			return nil, fmt.Errorf("nil numaNodeState for NUMA: %d", numaNode)
		}

		topologyAwareAllocatableQuantityList = append(topologyAwareAllocatableQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(numaNodeState.Allocatable),
			Node:          uint64(numaNode),
		})
		topologyAwareCapacityQuantityList = append(topologyAwareCapacityQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(numaNodeState.TotalMemSize),
			Node:          uint64(numaNode),
		})
		aggregatedAllocatableQuantity += numaNodeState.Allocatable
		aggregatedCapacityQuantity += numaNodeState.TotalMemSize
	}

	return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
		AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
			string(v1.ResourceMemory): {
				IsNodeResource:                       false,
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
func (p *DynamicPolicy) GetResourcePluginOptions(context.Context,
	*pluginapi.Empty,
) (*pluginapi.ResourcePluginOptions, error) {
	return &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: true,
		NeedReconcile:         true,
	}, nil
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
func (p *DynamicPolicy) Allocate(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceAllocationResponse, respErr error) {
	if req == nil {
		return nil, fmt.Errorf("Allocate got nil req")
	}

	// identify if the pod is a debug pod,
	// if so, apply specific strategy to it.
	// since GetKatalystQoSLevelFromResourceReq function will filter annotations,
	// we should do it before GetKatalystQoSLevelFromResourceReq.
	isDebugPod := util.IsDebugPod(req.Annotations, p.podDebugAnnoKeys)

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
		"podType", req.PodType,
		"podRole", req.PodRole,
		"qosLevel", qosLevel,
		"memoryReq(bytes)", reqInt,
		"hint", req.Hint)

	if req.ContainerType == pluginapi.ContainerType_INIT {
		return &pluginapi.ResourceAllocationResponse{
			PodUid:         req.PodUid,
			PodNamespace:   req.PodNamespace,
			PodName:        req.PodName,
			ContainerName:  req.ContainerName,
			ContainerType:  req.ContainerType,
			ContainerIndex: req.ContainerIndex,
			PodRole:        req.PodRole,
			PodType:        req.PodType,
			ResourceName:   string(v1.ResourceMemory),
			Labels:         general.DeepCopyMap(req.Labels),
			Annotations:    general.DeepCopyMap(req.Annotations),
		}, nil
	} else if isDebugPod {
		return &pluginapi.ResourceAllocationResponse{
			PodUid:         req.PodUid,
			PodNamespace:   req.PodNamespace,
			PodName:        req.PodName,
			ContainerName:  req.ContainerName,
			ContainerType:  req.ContainerType,
			ContainerIndex: req.ContainerIndex,
			PodRole:        req.PodRole,
			PodType:        req.PodType,
			ResourceName:   string(v1.ResourceMemory),
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceMemory): {
						// return ResourceAllocation with empty OciPropertyName, AllocatedQuantity, AllocationResult for containers in debug pod,
						// it won't influence oci spec properties of the container
						IsNodeResource:   false,
						IsScalarResource: true,
					},
				},
			},
			Labels:      general.DeepCopyMap(req.Labels),
			Annotations: general.DeepCopyMap(req.Annotations),
		}, nil
	}

	p.Lock()
	defer func() {
		// calls sys-advisor to inform the latest container
		if p.enableMemoryAdvisor && respErr == nil && req.ContainerType != pluginapi.ContainerType_INIT {
			_, err := p.advisorClient.AddContainer(ctx, &advisorsvc.ContainerMetadata{
				PodUid:          req.PodUid,
				PodNamespace:    req.PodNamespace,
				PodName:         req.PodName,
				ContainerName:   req.ContainerName,
				ContainerType:   req.ContainerType,
				ContainerIndex:  req.ContainerIndex,
				Labels:          maputil.CopySS(req.Labels),
				Annotations:     maputil.CopySS(req.Annotations),
				QosLevel:        qosLevel,
				RequestQuantity: uint64(reqInt),
			})
			if err != nil {
				resp = nil
				respErr = fmt.Errorf("add container to qos aware server failed with error: %v", err)
				_ = p.removeContainer(req.PodUid, req.ContainerName)
			}
		} else if respErr != nil {
			_ = p.removeContainer(req.PodUid, req.ContainerName)
			_ = p.emitter.StoreInt64(util.MetricNameAllocateFailed, 1, metrics.MetricTypeNameRaw)
		}

		p.Unlock()
		return
	}()

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil && allocationInfo.AggregatedQuantity >= uint64(reqInt) && !util.PodInplaceUpdateResizing(req) {
		general.InfoS("already allocated and meet requirement",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"memoryReq(bytes)", reqInt,
			"currentResult(bytes)", allocationInfo.AggregatedQuantity)
		return &pluginapi.ResourceAllocationResponse{
			PodUid:         req.PodUid,
			PodNamespace:   req.PodNamespace,
			PodName:        req.PodName,
			ContainerName:  req.ContainerName,
			ContainerType:  req.ContainerType,
			ContainerIndex: req.ContainerIndex,
			PodRole:        req.PodRole,
			PodType:        req.PodType,
			ResourceName:   string(v1.ResourceMemory),
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceMemory): {
						OciPropertyName:   util.OCIPropertyNameCPUSetMems,
						IsNodeResource:    false,
						IsScalarResource:  true,
						AllocatedQuantity: float64(allocationInfo.AggregatedQuantity),
						AllocationResult:  allocationInfo.NumaAllocationResult.String(),
					},
				},
			},
			Labels:      general.DeepCopyMap(req.Labels),
			Annotations: general.DeepCopyMap(req.Annotations),
		}, nil
	}

	if p.allocationHandlers[qosLevel] == nil {
		return nil, fmt.Errorf("katalyst QoS level: %s is not supported yet", qosLevel)
	}
	return p.allocationHandlers[qosLevel](ctx, req)
}

// AllocateForPod is called during pod admit so that the resource
// plugin can allocate corresponding resource for the pod
// according to resource request
func (p *DynamicPolicy) AllocateForPod(ctx context.Context,
	req *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceAllocationResponse, respErr error) {
	return nil, util.ErrNotImplemented
}

// PreStartContainer is called, if indicated by resource plugin during registration phase,
// before each container start. Resource plugin can run resource specific operations
// such as resetting the resource before making resources available to the container
func (p *DynamicPolicy) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return nil, nil
}

func (p *DynamicPolicy) removePod(podUID string) error {
	podResourceEntries := p.state.GetPodResourceEntries()
	for _, podEntries := range podResourceEntries {
		delete(podEntries, podUID)
	}

	resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("pod: %s, GenerateMachineStateFromPodEntries failed with error: %v", podUID, err)
		return fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	p.state.SetPodResourceEntries(podResourceEntries)
	p.state.SetMachineState(resourcesMachineState)
	return nil
}

func (p *DynamicPolicy) removeContainer(podUID, containerName string) error {
	podResourceEntries := p.state.GetPodResourceEntries()

	found := false
	for _, podEntries := range podResourceEntries {
		if podEntries[podUID][containerName] != nil {
			found = true
		}

		delete(podEntries[podUID], containerName)
	}

	if !found {
		return nil
	}

	resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("pod: %s, container: %s GenerateMachineStateFromPodEntries failed with error: %v", podUID, containerName, err)
		return fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	p.state.SetPodResourceEntries(podResourceEntries)
	p.state.SetMachineState(resourcesMachineState)
	return nil
}

// getContainerRequestedMemoryBytes parses and returns requested memory bytes for the given container
func (p *DynamicPolicy) getContainerRequestedMemoryBytes(allocationInfo *state.AllocationInfo) uint64 {
	if allocationInfo == nil {
		general.Errorf("got nil allocationInfo")
		return 0
	}

	if p.metaServer == nil {
		general.Errorf("got nil metaServer, return the origin value")
		return allocationInfo.AggregatedQuantity
	}

	// TODO optimize this logic someday:
	//	only for refresh cpu request for old pod with cpu ceil and old VPA pods.
	//  we can remove refresh logic after upgrade all kubelet and qrm.
	if allocationInfo.CheckShared() {
		// if there is these two annotations in memory state, it is a new pod,
		// we don't need to check the pod request from podWatcher
		if allocationInfo.Annotations[apiconsts.PodAnnotationAggregatedRequestsKey] != "" ||
			allocationInfo.Annotations[apiconsts.PodAnnotationInplaceUpdateResizingKey] != "" {
			return allocationInfo.AggregatedQuantity
		}

		if allocationInfo.CheckNUMABinding() {
			// snb count all memory into main container, sidecar is zero
			if allocationInfo.CheckSideCar() {
				// sidecar container always is zero
				general.Infof("[snb] get memory request quantity: (%d -> 0) for pod: %s/%s container(sidecar): %s",
					allocationInfo.AggregatedQuantity, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				return 0
			} else {
				// main container contains the whole pod request (both sidecars and main container).
				// check pod memory aggregated quantity from podWatcher
				podAggregatedMemoryRequestBytes, err := p.getPodSpecAggregatedMemoryRequestBytes(allocationInfo.PodUid)
				if err != nil {
					general.Errorf("[snb] get container failed with error: %v, return the origin value", err)
					return allocationInfo.AggregatedQuantity
				}

				// only handle scale in case
				if podAggregatedMemoryRequestBytes < allocationInfo.AggregatedQuantity {
					general.Infof("[snb] get memory request quantity: (%d->%d) for pod: %s/%s container: %s from podWatcher",
						allocationInfo.AggregatedQuantity, podAggregatedMemoryRequestBytes, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
					return podAggregatedMemoryRequestBytes
				}
			}
		} else {
			// non-binding share cores pod
			requestBytes, err := p.getContainerSpecMemoryRequestBytes(allocationInfo.PodUid, allocationInfo.ContainerName)
			if err != nil {
				general.Errorf("[other] get container failed with error: %v, return the origin value", err)
				return allocationInfo.AggregatedQuantity
			}
			if requestBytes != allocationInfo.AggregatedQuantity {
				general.Infof("[share] get memory request quantity: (%d->%d) for pod: %s/%s container: %s from podWatcher",
					allocationInfo.AggregatedQuantity, requestBytes, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				return requestBytes
			}
		}
	}

	general.Infof("get memory request bytes: %d for pod: %s/%s container: %s from podWatcher",
		allocationInfo.AggregatedQuantity, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
	return allocationInfo.AggregatedQuantity
}

func (p *DynamicPolicy) getPodSpecAggregatedMemoryRequestBytes(podUID string) (uint64, error) {
	pod, err := p.metaServer.GetPod(context.Background(), podUID)
	if err != nil {
		general.Errorf("get pod failed with error: %v", err)
		return 0, err
	} else if pod == nil {
		general.Errorf("get pod failed with not found")
		return 0, errors.New("pod not found")
	}

	requestBytes := uint64(0)
	for _, container := range pod.Spec.Containers {
		containerMemoryQuantity := native.MemoryQuantityGetter()(container.Resources.Requests)
		requestBytes += uint64(general.Max(int(containerMemoryQuantity.Value()), 0))
	}

	return requestBytes, nil
}

func (p *DynamicPolicy) getContainerSpecMemoryRequestBytes(podUID, containerName string) (uint64, error) {
	container, err := p.metaServer.GetContainerSpec(podUID, containerName)
	if err != nil {
		general.Errorf("get container failed with error: %v", err)
		return 0, err
	} else if container == nil {
		general.Errorf("get container failed with not found")
		return 0, errors.New("container not found")
	}

	memoryQuantity := native.MemoryQuantityGetter()(container.Resources.Requests)
	requestBytes := uint64(general.Max(int(memoryQuantity.Value()), 0))

	return requestBytes, nil
}

// hasLastLevelEnhancementKey check if the pod with the given UID has the corresponding last level enhancement key
func (p *DynamicPolicy) hasLastLevelEnhancementKey(lastLevelEnhancementKey string, podUID string) bool {
	podEntries := p.state.GetPodResourceEntries()[v1.ResourceMemory]

	for _, allocationInfo := range podEntries[podUID] {
		if _, ok := allocationInfo.Annotations[lastLevelEnhancementKey]; ok {
			general.Infof("pod: %s has last level enhancement key: %s", podUID, lastLevelEnhancementKey)
			return true
		}
	}

	general.Infof("pod: %s does not have last level enhancement key: %s", podUID, lastLevelEnhancementKey)
	return false
}

func (p *DynamicPolicy) checkNonBindingShareCoresMemoryResource(req *pluginapi.ResourceRequest) (bool, error) {
	reqInt, _, err := util.GetPodAggregatedRequestResource(req)
	if err != nil {
		return false, fmt.Errorf("GetQuantityFromResourceReq failed with error: %v", err)
	}

	shareCoresAllocated := uint64(reqInt)
	podEntries := p.state.GetPodResourceEntries()[v1.ResourceMemory]
	for podUid, containerEntries := range podEntries {
		for _, containerAllocation := range containerEntries {
			// skip the current pod
			if podUid == req.PodUid {
				continue
			}
			// shareCoresAllocated should involve both main and sidecar containers
			if containerAllocation.CheckDedicated() && !containerAllocation.CheckNUMABinding() {
				shareCoresAllocated += p.getContainerRequestedMemoryBytes(containerAllocation)
			}
		}
	}

	machineState := p.state.GetMachineState()
	resourceState := machineState[v1.ResourceMemory]
	numaWithoutNUMABindingPods := resourceState.GetNUMANodesWithoutSharedOrDedicatedNUMABindingPods()
	numaAllocatableWithoutNUMABindingPods := uint64(0)
	for _, numaID := range numaWithoutNUMABindingPods.ToSliceInt() {
		numaAllocatableWithoutNUMABindingPods += resourceState[numaID].Allocatable
	}

	general.Infof("[checkNonBindingShareCoresMemoryResource] node memory allocated: %d, allocatable: %d", shareCoresAllocated, numaAllocatableWithoutNUMABindingPods)
	if shareCoresAllocated > numaAllocatableWithoutNUMABindingPods {
		general.Warningf("[checkNonBindingShareCoresMemoryResource] no enough memory resource for non-binding share cores pod: %s/%s, container: %s (allocated: %d, allocatable: %d)",
			req.PodNamespace, req.PodName, req.ContainerName, shareCoresAllocated, numaAllocatableWithoutNUMABindingPods)
		return false, nil
	}

	general.InfoS("checkNonBindingShareCoresMemoryResource memory successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"reqInt", reqInt)

	return true, nil
}
