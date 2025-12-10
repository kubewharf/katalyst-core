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
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"
	"k8s.io/utils/clock"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/registry"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/irqtuner"
	irqtuingcontroller "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/irqtuner/controller"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/validator"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
	"github.com/kubewharf/katalyst-core/pkg/util/timemonitor"
)

const (
	cpuPluginStateFileName = "cpu_plugin_state"

	reservedReclaimedCPUsSize = 4

	cpusetCheckPeriod = 10 * time.Second
	stateCheckPeriod  = 30 * time.Second
	maxResidualTime   = 5 * time.Minute
	syncCPUIdlePeriod = 30 * time.Second

	healthCheckTolerationTimes = 3
)

// DynamicPolicy is the policy that's used by default;
// it will consider the dynamic running information to calculate
// and adjust resource requirements and configurations
type DynamicPolicy struct {
	sync.RWMutex
	name    string
	stopCh  chan struct{}
	started bool

	emitter     metrics.MetricEmitter
	metaServer  *metaserver.MetaServer
	machineInfo *machine.KatalystMachineInfo

	advisorClient    advisorapi.CPUAdvisorClient
	advisorConn      *grpc.ClientConn
	advisorValidator *validator.CPUAdvisorValidator
	advisorapi.UnimplementedCPUPluginServer
	advisorMonitor     *timemonitor.TimeMonitor
	featureGateManager featuregatenegotiation.FeatureGateManager

	state              state.State
	residualHitMap     map[string]int64
	allocationHandlers map[string]util.AllocationHandler
	hintHandlers       map[string]util.HintHandler

	cpuPressureEviction       agent.Component
	cpuPressureEvictionCancel context.CancelFunc

	irqTuner irqtuner.Tuner

	// those are parsed from configurations
	// todo if we want to use dynamic configuration, we'd better not use self-defined conf
	enableCPUAdvisor                          bool
	getAdviceInterval                         time.Duration
	reservedCPUs                              machine.CPUSet
	cpuAdvisorSocketAbsPath                   string
	cpuPluginSocketAbsPath                    string
	extraStateFileAbsPath                     string
	enableReclaimNUMABinding                  bool
	enableSNBHighNumaPreference               bool
	enableCPUIdle                             bool
	enableSyncingCPUIdle                      bool
	reclaimRelativeRootCgroupPath             string
	numaBindingReclaimRelativeRootCgroupPaths map[int]string
	qosConfig                                 *generic.QoSConfiguration
	dynamicConfig                             *dynamicconfig.DynamicAgentConfiguration
	conf                                      *config.Configuration
	podDebugAnnoKeys                          []string
	podAnnotationKeptKeys                     []string
	podLabelKeptKeys                          []string
	sharedCoresNUMABindingResultAnnotationKey string
	transitionPeriod                          time.Duration

	reservedReclaimedCPUsSize                 int
	reservedReclaimedCPUSet                   machine.CPUSet
	reservedReclaimedTopologyAwareAssignments map[int]machine.CPUSet

	sharedCoresNUMABindingHintOptimizer    hintoptimizer.HintOptimizer
	dedicatedCoresNUMABindingHintOptimizer hintoptimizer.HintOptimizer
}

func NewDynamicPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	// add watcher for general gvrs needed in most cases
	reservedCPUs, reserveErr := cpuutil.GetCoresReservedForSystem(conf, agentCtx.MetaServer, agentCtx.KatalystMachineInfo, agentCtx.CPUDetails.CPUs().Clone())
	if reserveErr != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("GetCoresReservedForSystem for reservedCPUsNum: %d, reservedCPUList: %s failed with error: %v",
			conf.ReservedCPUCores, conf.ReservedCPUList, reserveErr)
	}

	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: cpuconsts.CPUResourcePluginPolicyNameDynamic,
	})

	stateImpl, stateErr := state.NewCheckpointState(conf.StateDirectoryConfiguration, cpuPluginStateFileName,
		cpuconsts.CPUResourcePluginPolicyNameDynamic, agentCtx.CPUTopology, conf.SkipCPUStateCorruption, state.GenerateMachineStateFromPodEntries, wrappedEmitter)
	if stateErr != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("NewCheckpointState failed with error: %v", stateErr)
	}

	state.SetReadonlyState(stateImpl)
	state.SetReadWriteState(stateImpl)

	var (
		cpuPressureEviction agent.Component
		err                 error
	)
	if conf.EnableCPUPressureEviction {
		cpuPressureEviction, err = cpueviction.NewCPUPressureEviction(
			agentCtx.EmitterPool.GetDefaultMetricsEmitter(), agentCtx.MetaServer, conf, stateImpl)
		if err != nil {
			return false, agent.ComponentStub{}, err
		}
	}

	// since the reservedCPUs won't influence stateImpl directly.
	// so we don't modify stateImpl with reservedCPUs here.
	// for those pods have already been allocated reservedCPUs,
	// we won't touch them and wait them to be deleted the next update.
	policyImplement := &DynamicPolicy{
		name:   fmt.Sprintf("%s_%s", agentName, cpuconsts.CPUResourcePluginPolicyNameDynamic),
		stopCh: make(chan struct{}),

		machineInfo: agentCtx.KatalystMachineInfo,
		emitter:     wrappedEmitter,
		metaServer:  agentCtx.MetaServer,

		state:          stateImpl,
		residualHitMap: make(map[string]int64),

		advisorValidator:   validator.NewCPUAdvisorValidator(stateImpl, agentCtx.KatalystMachineInfo),
		featureGateManager: featuregatenegotiation.NewFeatureGateManager(conf),

		cpuPressureEviction: cpuPressureEviction,

		conf:                          conf,
		qosConfig:                     conf.QoSConfiguration,
		dynamicConfig:                 conf.DynamicAgentConfiguration,
		cpuAdvisorSocketAbsPath:       conf.CPUAdvisorSocketAbsPath,
		cpuPluginSocketAbsPath:        conf.CPUPluginSocketAbsPath,
		enableReclaimNUMABinding:      conf.EnableReclaimNUMABinding,
		enableSNBHighNumaPreference:   conf.EnableSNBHighNumaPreference,
		enableCPUAdvisor:              conf.CPUQRMPluginConfig.EnableCPUAdvisor,
		getAdviceInterval:             conf.CPUQRMPluginConfig.GetAdviceInterval,
		reservedCPUs:                  reservedCPUs,
		extraStateFileAbsPath:         conf.ExtraStateFileAbsPath,
		enableSyncingCPUIdle:          conf.CPUQRMPluginConfig.EnableSyncingCPUIdle,
		enableCPUIdle:                 conf.CPUQRMPluginConfig.EnableCPUIdle,
		reclaimRelativeRootCgroupPath: conf.ReclaimRelativeRootCgroupPath,
		numaBindingReclaimRelativeRootCgroupPaths: common.GetNUMABindingReclaimRelativeRootCgroupPaths(conf.ReclaimRelativeRootCgroupPath,
			agentCtx.CPUDetails.NUMANodes().ToSliceNoSortInt()),
		podDebugAnnoKeys:                          conf.PodDebugAnnoKeys,
		podAnnotationKeptKeys:                     conf.PodAnnotationKeptKeys,
		podLabelKeptKeys:                          conf.PodLabelKeptKeys,
		sharedCoresNUMABindingResultAnnotationKey: conf.SharedCoresNUMABindingResultAnnotationKey,
		transitionPeriod:                          30 * time.Second,
		reservedReclaimedCPUsSize:                 general.Max(reservedReclaimedCPUsSize, agentCtx.KatalystMachineInfo.NumNUMANodes),
	}

	// initialize hint optimizer
	err = policyImplement.initHintOptimizers()
	if err != nil {
		return false, nil, err
	}

	if conf.EnableIRQTuner {
		irqTuner, err := irqtuingcontroller.NewIrqTuningController(conf.AgentConfiguration, policyImplement, policyImplement.emitter, policyImplement.machineInfo)
		if err != nil {
			general.Errorf("failed to NewIrqTuningController, err %s", err)
			return false, agent.ComponentStub{}, err
		} else {
			policyImplement.irqTuner = irqTuner
		}
	}

	// register allocation behaviors for pods with different QoS level
	policyImplement.allocationHandlers = map[string]util.AllocationHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelSystemCores:    policyImplement.systemCoresAllocationHandler,
	}

	// register hint providers for pods with different QoS level
	policyImplement.hintHandlers = map[string]util.HintHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresHintHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresHintHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresHintHandler,
		consts.PodAnnotationQoSLevelSystemCores:    policyImplement.systemCoresHintHandler,
	}

	if err := policyImplement.cleanPools(); err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("cleanPools failed with error: %v", err)
	}

	if err := policyImplement.initReservePool(); err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy initReservePool failed with error: %v", err)
	}

	if err := policyImplement.initReclaimPool(); err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy initReclaimPool failed with error: %v", err)
	}

	if conf.EnableIRQTuner {
		if err := policyImplement.initInterruptPool(); err != nil {
			return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy initInterruptPool failed with error: %v", err)
		}
	}

	err = agentCtx.MetaServer.ConfigurationManager.AddConfigWatcher(crd.AdminQoSConfigurationGVR)
	if err != nil {
		return false, nil, err
	}

	if err := agentCtx.MetaServer.ConfigurationManager.AddConfigWatcher(crd.StrategyGroupGVR); err != nil {
		return false, nil, err
	}

	err = agentCtx.ConfigurationManager.AddConfigWatcher(crd.IRQTuningConfigurationGVR)
	if err != nil {
		return false, nil, err
	}

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(policyImplement, conf.QRMPluginSocketDirs, func(key string, value int64) {
		_ = wrappedEmitter.StoreInt64(key, value, metrics.MetricTypeNameRaw)
	})
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy new plugin wrapper failed with error: %v", err)
	}

	return true, &agent.PluginWrapper{GenericPlugin: pluginWrapper}, nil
}

func (p *DynamicPolicy) Name() string {
	return p.name
}

func (p *DynamicPolicy) ResourceName() string {
	return string(v1.ResourceCPU)
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
		general.Infof("is already started")
		return nil
	}

	p.stopCh = make(chan struct{})

	if p.irqTuner != nil {
		go p.irqTuner.Run(p.stopCh)
	}

	go wait.Until(func() {
		_ = p.emitter.StoreInt64(util.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
	}, time.Second*30, p.stopCh)

	err = periodicalhandler.RegisterPeriodicalHandlerWithHealthz(cpuconsts.ClearResidualState, general.HealthzCheckStateNotReady,
		qrm.QRMCPUPluginPeriodicalHandlerGroupName, p.clearResidualState, stateCheckPeriod, healthCheckTolerationTimes)
	if err != nil {
		general.Errorf("start %v failed,err:%v", cpuconsts.ClearResidualState, err)
	}

	err = periodicalhandler.RegisterPeriodicalHandlerWithHealthz(cpuconsts.CheckCPUSet, general.HealthzCheckStateNotReady,
		qrm.QRMCPUPluginPeriodicalHandlerGroupName, p.checkCPUSet, cpusetCheckPeriod, healthCheckTolerationTimes)
	if err != nil {
		general.Errorf("start %v failed,err:%v", cpuconsts.CheckCPUSet, err)
	}

	// start cpu-idle syncing if needed
	if p.enableSyncingCPUIdle {
		general.Infof("syncCPUIdle enabled")

		if p.reclaimRelativeRootCgroupPath == "" {
			return fmt.Errorf("enable syncing cpu idle but not set reclaiemd relative root cgroup path in configuration")
		}

		err = periodicalhandler.RegisterPeriodicalHandlerWithHealthz(cpuconsts.SyncCPUIdle, general.HealthzCheckStateNotReady,
			qrm.QRMCPUPluginPeriodicalHandlerGroupName, p.syncCPUIdle, syncCPUIdlePeriod, healthCheckTolerationTimes)
		if err != nil {
			general.Errorf("start %v failed,err:%v", cpuconsts.SyncCPUIdle, err)
		}
	}

	// start cpu-pressure eviction plugin if needed
	if p.cpuPressureEviction != nil {
		var ctx context.Context
		ctx, p.cpuPressureEvictionCancel = context.WithCancel(context.Background())
		go p.cpuPressureEviction.Run(ctx)
	}

	go wait.Until(func() {
		periodicalhandler.ReadyToStartHandlersByGroup(qrm.QRMCPUPluginPeriodicalHandlerGroupName)
	}, 5*time.Second, p.stopCh)

	// pre-check necessary dirs if sys-advisor is enabled
	if !p.enableCPUAdvisor {
		general.Infof("start dynamic policy cpu plugin without sys-advisor")
		return nil
	} else if p.cpuAdvisorSocketAbsPath == "" || p.cpuPluginSocketAbsPath == "" {
		return fmt.Errorf("invalid cpuAdvisorSocketAbsPath: %s or cpuPluginSocketAbsPath: %s",
			p.cpuAdvisorSocketAbsPath, p.cpuPluginSocketAbsPath)
	}

	general.Infof("start dynamic policy cpu plugin with sys-advisor")
	general.RegisterHeartbeatCheck(cpuconsts.CommunicateWithAdvisor, 2*time.Minute, general.HealthzCheckStateNotReady, 2*time.Minute)

	err = p.initAdvisorClientConn()
	if err != nil {
		general.Errorf("initAdvisorClientConn failed with error: %v", err)
		return
	}

	p.advisorMonitor, err = timemonitor.NewTimeMonitor(cpuAdvisorHealthMonitorName, cpuAdvisorHealthMonitorInterval,
		cpuAdvisorUnhealthyThreshold, cpuAdvisorHealthyThreshold,
		util.MetricNameAdvisorUnhealthy, p.emitter, cpuAdvisorHealthyCount, true)
	if err != nil {
		general.Errorf("initialize cpu advisor monitor failed with error: %v", err)
		return
	}
	go p.advisorMonitor.Run(p.stopCh)

	go wait.BackoffUntil(func() { p.serveForAdvisor(p.stopCh) }, wait.NewExponentialBackoffManager(
		800*time.Millisecond, 30*time.Second, 2*time.Minute, 2.0, 0, &clock.RealClock{}), true, p.stopCh)

	communicateWithCPUAdvisorServer := func() {
		general.Infof("waiting cpu plugin checkpoint server serving confirmation")
		if conn, err := process.Dial(p.cpuPluginSocketAbsPath, 5*time.Second); err != nil {
			general.Errorf("dial check at socket: %s failed with err: %v", p.cpuPluginSocketAbsPath, err)
			return
		} else {
			_ = conn.Close()
		}
		general.Infof("cpu plugin checkpoint server serving confirmed")

		p.getAdviceFromAdvisorLoop(p.stopCh)
		select {
		case <-p.stopCh:
			// stopCh closed, no need to fall back to ListAndWatch.
			return
		default:
		}

		general.Infof("advisor does not implement GetAdvice, fall back to ListAndWatch")

		if err := p.pushCPUAdvisor(); err != nil {
			general.Errorf("sync existing containers to cpu advisor failed with error: %v", err)
			return
		}
		general.Infof("sync existing containers to cpu advisor successfully")

		// call lw of CPUAdvisorServer and do allocation
		if err := p.lwCPUAdvisorServer(p.stopCh); err != nil {
			general.Errorf("lwCPUAdvisorServer failed with error: %v", err)
		} else {
			general.Infof("lwCPUAdvisorServer finished")
		}
	}

	go wait.BackoffUntil(communicateWithCPUAdvisorServer, wait.NewExponentialBackoffManager(800*time.Millisecond,
		30*time.Second, 2*time.Minute, 2.0, 0, &clock.RealClock{}), true, p.stopCh)

	err = p.sharedCoresNUMABindingHintOptimizer.Run(p.stopCh)
	if err != nil {
		return fmt.Errorf("sharedCoresNUMABindingHintOptimizer.Run failed with error: %v", err)
	}

	err = p.dedicatedCoresNUMABindingHintOptimizer.Run(p.stopCh)
	if err != nil {
		return fmt.Errorf("dedicatedCoresNUMABindingHintOptimizer.Run failed with error: %v", err)
	}

	return nil
}

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

	if p.cpuPressureEvictionCancel != nil {
		p.cpuPressureEvictionCancel()
	}

	periodicalhandler.StopHandlersByGroup(qrm.QRMCPUPluginPeriodicalHandlerGroupName)

	if p.advisorConn != nil {
		return p.advisorConn.Close()
	}

	return nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *DynamicPolicy) GetResourcesAllocation(_ context.Context,
	req *pluginapi.GetResourcesAllocationRequest,
) (*pluginapi.GetResourcesAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetResourcesAllocation got nil req")
	}

	general.Infof("called")
	p.Lock()
	defer p.Unlock()

	podEntries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()

	// rumpUpPooledCPUs is the total available cpu cores minus those that are reserved
	rumpUpPooledCPUs := machineState.GetFilteredAvailableCPUSet(p.reservedCPUs,
		func(ai *state.AllocationInfo) bool {
			return ai.CheckDedicated() || ai.CheckSharedNUMABinding()
		},
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicatedNUMABinding))
	rumpUpPooledCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, rumpUpPooledCPUs)
	if err != nil {
		return nil, fmt.Errorf("GetNumaAwareAssignments err: %v", err)
	}

	var allocationInfosJustFinishRampUp []*state.AllocationInfo
	needUpdateMachineState := false
	for podUID, containerEntries := range podEntries {
		// if it's a pool, not returning to QRM
		if containerEntries.IsPoolEntry() {
			continue
		}

		mainContainerAllocationInfo := podEntries[podUID].GetMainContainerEntry()
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}
			allocationInfo = allocationInfo.Clone()

			// sync allocation info from main container to sidecar
			if allocationInfo.CheckSideCar() && mainContainerAllocationInfo != nil {
				if p.applySidecarAllocationInfoFromMainContainer(allocationInfo, mainContainerAllocationInfo) {
					general.Infof("pod: %s/%s, container: %s sync allocation info from main container",
						allocationInfo.PodNamespace, allocationInfo.PodName, containerName)
					p.state.SetAllocationInfo(podUID, containerName, allocationInfo, true)
					needUpdateMachineState = true
				}
			}

			initTs, tsErr := time.Parse(util.QRMTimeFormat, allocationInfo.InitTimestamp)
			if tsErr != nil {
				if allocationInfo.CheckShared() && !allocationInfo.CheckNUMABinding() {
					general.Errorf("pod: %s/%s, container: %s init timestamp parsed failed with error: %v, re-ramp-up it",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, tsErr)

					clonedPooledCPUs := rumpUpPooledCPUs.Clone()
					clonedPooledCPUsTopologyAwareAssignments := machine.DeepcopyCPUAssignment(rumpUpPooledCPUsTopologyAwareAssignments)

					allocationInfo.AllocationResult = clonedPooledCPUs
					allocationInfo.OriginalAllocationResult = clonedPooledCPUs
					allocationInfo.TopologyAwareAssignments = clonedPooledCPUsTopologyAwareAssignments
					allocationInfo.OriginalTopologyAwareAssignments = clonedPooledCPUsTopologyAwareAssignments
					// fill OwnerPoolName with empty string when ramping up
					allocationInfo.OwnerPoolName = commonstate.EmptyOwnerPoolName
					allocationInfo.RampUp = true
				}

				allocationInfo.InitTimestamp = time.Now().Format(util.QRMTimeFormat)
				p.state.SetAllocationInfo(podUID, containerName, allocationInfo, true)
			} else if allocationInfo.RampUp && time.Now().After(initTs.Add(p.transitionPeriod)) {
				general.Infof("pod: %s/%s, container: %s ramp up finished", allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				allocationInfo.RampUp = false
				p.state.SetAllocationInfo(podUID, containerName, allocationInfo, true)

				if allocationInfo.CheckShared() {
					allocationInfosJustFinishRampUp = append(allocationInfosJustFinishRampUp, allocationInfo)
				}
			}

		}
	}

	if len(allocationInfosJustFinishRampUp) > 0 {
		if err = p.putAllocationsAndAdjustAllocationEntries(allocationInfosJustFinishRampUp, true, true); err != nil {
			// not influencing return response to kubelet when putAllocationsAndAdjustAllocationEntries failed
			general.Errorf("putAllocationsAndAdjustAllocationEntries failed with error: %v", err)
		}
	} else if needUpdateMachineState {
		// NOTE: we only need update machine state when putAllocationsAndAdjustAllocationEntries is skipped,
		// because putAllocationsAndAdjustAllocationEntries will update machine state.
		general.Infof("GetResourcesAllocation update machine state")
		podEntries = p.state.GetPodEntries()
		updatedMachineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries, machineState)
		if err != nil {
			general.Errorf("GetResourcesAllocation GenerateMachineStateFromPodEntries failed with error: %v", err)
			return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
		}
		p.state.SetMachineState(updatedMachineState, true)
	}

	podEntries = p.state.GetPodEntries()
	podResources := make(map[string]*pluginapi.ContainerResources)
	for podUID, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		if podResources[podUID] == nil {
			podResources[podUID] = &pluginapi.ContainerResources{}
		}

		for containerName, allocationInfo := range containerEntries {
			if podResources[podUID].ContainerResources == nil {
				podResources[podUID].ContainerResources = make(map[string]*pluginapi.ResourceAllocation)
			}
			podResources[podUID].ContainerResources[containerName] = &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceCPU): {
						OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
						IsNodeResource:    false,
						IsScalarResource:  true,
						AllocatedQuantity: float64(allocationInfo.AllocationResult.Size()),
						AllocationResult:  allocationInfo.AllocationResult.String(),
					},
				},
			}
		}
	}

	return &pluginapi.GetResourcesAllocationResponse{
		PodResources: podResources,
	}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as machineInfo aware format
func (p *DynamicPolicy) GetTopologyAwareResources(_ context.Context,
	req *pluginapi.GetTopologyAwareResourcesRequest,
) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyAwareResources got nil req")
	}

	general.Infof("called")
	p.RLock()
	defer p.RUnlock()

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo == nil {
		return nil, fmt.Errorf("pod: %s, container: %s is not show up in cpu plugin state", req.PodUid, req.ContainerName)
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
			string(v1.ResourceCPU): {
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
			string(v1.ResourceCPU): {
				IsNodeResource:                    false,
				IsScalarResource:                  true,
				AggregatedQuantity:                float64(allocationInfo.AllocationResult.Size()),
				OriginalAggregatedQuantity:        float64(allocationInfo.OriginalAllocationResult.Size()),
				TopologyAwareQuantityList:         util.GetTopologyAwareQuantityFromAssignments(allocationInfo.TopologyAwareAssignments),
				OriginalTopologyAwareQuantityList: util.GetTopologyAwareQuantityFromAssignments(allocationInfo.OriginalTopologyAwareAssignments),
			},
		}
	}

	return resp, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as machineInfo aware format
func (p *DynamicPolicy) GetTopologyAwareAllocatableResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareAllocatableResourcesRequest,
) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	general.Infof("is called")

	numaNodes := p.machineInfo.CPUDetails.NUMANodes().ToSliceInt()
	topologyAwareAllocatableQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(numaNodes))
	topologyAwareCapacityQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(numaNodes))

	for _, numaNode := range numaNodes {
		numaNodeCPUs := p.machineInfo.CPUDetails.CPUsInNUMANodes(numaNode).Clone()
		topologyAwareAllocatableQuantityList = append(topologyAwareAllocatableQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(numaNodeCPUs.Difference(p.reservedCPUs).Size()),
			Node:          uint64(numaNode),
		})
		topologyAwareCapacityQuantityList = append(topologyAwareCapacityQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(numaNodeCPUs.Size()),
			Node:          uint64(numaNode),
		})
	}

	return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
		AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
			string(v1.ResourceCPU): {
				IsNodeResource:                       false,
				IsScalarResource:                     true,
				AggregatedAllocatableQuantity:        float64(p.machineInfo.NumCPUs - p.reservedCPUs.Size()),
				TopologyAwareAllocatableQuantityList: topologyAwareAllocatableQuantityList,
				AggregatedCapacityQuantity:           float64(p.machineInfo.NumCPUs),
				TopologyAwareCapacityQuantityList:    topologyAwareCapacityQuantityList,
			},
		},
	}, nil
}

// GetTopologyHints returns hints of corresponding resources
func (p *DynamicPolicy) GetTopologyHints(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceHintsResponse, err error) {
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

	reqInt, reqFloat64, err := util.GetQuantityFromResourceReq(req)
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
		"numCPUsInt", reqInt,
		"numCPUsFloat64", reqFloat64,
		"isDebugPod", isDebugPod,
		"annotation", req.Annotations)

	if req.ContainerType == pluginapi.ContainerType_INIT || isDebugPod {
		general.Infof("there is no NUMA preference, return nil hint")
		return util.PackResourceHintsResponse(req, string(v1.ResourceCPU),
			map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceCPU): nil, // indicates that there is no numa preference
			})
	}

	startTime := time.Now()
	p.RLock()
	defer func() {
		p.RUnlock()
		if err != nil {
			inplaceUpdateResizing := util.PodInplaceUpdateResizing(req)
			_ = p.emitter.StoreInt64(util.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)},
				metrics.MetricTag{Key: util.MetricTagNameInplaceUpdateResizing, Val: strconv.FormatBool(inplaceUpdateResizing)})

			general.ErrorS(err, "GetTopologyHints failed",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"inplaceUpdateResizing", inplaceUpdateResizing,
			)
		}
		general.InfoS("finished",
			"duration", time.Since(startTime).String(),
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
		)
	}()

	if p.hintHandlers[qosLevel] == nil {
		return nil, fmt.Errorf("katalyst QoS level: %s is not supported yet", qosLevel)
	}
	return p.hintHandlers[qosLevel](ctx, req)
}

// GetPodTopologyHints returns hints of corresponding resources for pod
func (p *DynamicPolicy) GetPodTopologyHints(ctx context.Context,
	req *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceHintsResponse, err error) {
	return nil, util.ErrNotImplemented
}

// GetResourcePluginOptions returns options to be communicated with Resource Manager
func (p *DynamicPolicy) GetResourcePluginOptions(context.Context,
	*pluginapi.Empty,
) (*pluginapi.ResourcePluginOptions, error) {
	general.Infof("called")
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
		return nil, fmt.Errorf("allocate got nil req")
	}

	// identify if the pod is a debug pod,
	// if so, apply specific strategy to it.
	// since GetKatalystQoSLevelFromResourceReq function will filter annotations,
	// we should do it before GetKatalystQoSLevelFromResourceReq.
	isDebugPod := util.IsDebugPod(req.Annotations, p.podDebugAnnoKeys)

	existReallocAnno, isReallocation := util.IsReallocation(req.Annotations)

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req, p.podAnnotationKeptKeys, p.podLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	reqInt, reqFloat64, err := util.GetQuantityFromResourceReq(req)
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
		"numCPUsInt", reqInt,
		"numCPUsFloat64", reqFloat64,
		"isDebugPod", isDebugPod,
		"annotations", req.Annotations)

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
			ResourceName:   string(v1.ResourceCPU),
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
			ResourceName:   string(v1.ResourceCPU),
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceCPU): {
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

	startTime := time.Now()
	p.Lock()
	defer func() {
		// calls sys-advisor to inform the latest container
		if p.enableCPUAdvisor && respErr == nil && req.ContainerType != pluginapi.ContainerType_INIT {
			_, err := p.advisorClient.AddContainer(ctx, &advisorsvc.ContainerMetadata{
				PodUid:               req.PodUid,
				PodNamespace:         req.PodNamespace,
				PodName:              req.PodName,
				ContainerName:        req.ContainerName,
				ContainerType:        req.ContainerType,
				ContainerIndex:       req.ContainerIndex,
				Labels:               maputil.CopySS(req.Labels),
				Annotations:          maputil.CopySS(req.Annotations),
				QosLevel:             qosLevel,
				RequestQuantity:      uint64(reqInt),
				RequestMilliQuantity: uint64(reqFloat64 * 1000),
				UseMilliQuantity:     true,
			})
			if err != nil {
				resp = nil
				respErr = fmt.Errorf("add container to qos aware server failed with error: %v", err)
				_ = p.removeContainer(req.PodUid, req.ContainerName, false)
			}
		} else if respErr != nil {
			inplaceUpdateResizing := util.PodInplaceUpdateResizing(req)
			if !inplaceUpdateResizing {
				_ = p.removeContainer(req.PodUid, req.ContainerName, false)
			}

			metricTags := []metrics.MetricTag{
				{Key: "error_message", Val: metric.MetricTagValueFormat(respErr)},
				{Key: util.MetricTagNameInplaceUpdateResizing, Val: strconv.FormatBool(inplaceUpdateResizing)},
			}
			if existReallocAnno {
				metricTags = append(metricTags, metrics.MetricTag{Key: "reallocation", Val: isReallocation})
			}
			_ = p.emitter.StoreInt64(util.MetricNameAllocateFailed, 1, metrics.MetricTypeNameRaw, metricTags...)
		}
		if err := p.state.StoreState(); err != nil {
			general.ErrorS(err, "store state failed", "podName", req.PodName, "containerName", req.ContainerName)
		}

		p.Unlock()
		if respErr != nil {
			general.ErrorS(respErr, "Allocate failed",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
			)
		}
		general.InfoS("finished",
			"duration", time.Since(startTime).String(),
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
		)
		return
	}()

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo != nil && allocationInfo.OriginalAllocationResult.Size() >= reqInt && !util.PodInplaceUpdateResizing(req) {
		general.InfoS("already allocated and meet requirement",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"numCPUs", reqInt,
			"originalAllocationResult", allocationInfo.OriginalAllocationResult.String(),
			"currentResult", allocationInfo.AllocationResult.String())

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
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceCPU): {
						OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
						IsNodeResource:    false,
						IsScalarResource:  true,
						AllocatedQuantity: float64(allocationInfo.AllocationResult.Size()),
						AllocationResult:  allocationInfo.AllocationResult.String(),
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
	return p.allocationHandlers[qosLevel](ctx, req, false)
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
func (p *DynamicPolicy) PreStartContainer(context.Context,
	*pluginapi.PreStartContainerRequest,
) (*pluginapi.PreStartContainerResponse, error) {
	return nil, nil
}

func (p *DynamicPolicy) RemovePod(ctx context.Context,
	req *pluginapi.RemovePodRequest,
) (resp *pluginapi.RemovePodResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}
	general.InfoS("called", "podUID", req.PodUid)

	startTime := time.Now()
	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameRemovePodFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
			general.ErrorS(err, "RemovePod failed", "podUID", req.PodUid)
		}
		general.InfoS("finished", "duration", time.Since(startTime).String(), "podUID", req.PodUid)
	}()

	podEntries := p.state.GetPodEntries()
	if len(podEntries[req.PodUid]) == 0 {
		return &pluginapi.RemovePodResponse{}, nil
	}

	if p.enableCPUAdvisor {
		if p.advisorClient == nil {
			return nil, fmt.Errorf("cpu advisor client is nil")
		}
		_, err = p.advisorClient.RemovePod(ctx, &advisorsvc.RemovePodRequest{PodUid: req.PodUid})
		if err != nil {
			return nil, fmt.Errorf("remove pod in QoS aware server failed with error: %v", err)
		}
	}

	err = p.removePod(req.PodUid, podEntries, false)
	if err != nil {
		general.ErrorS(err, "remove pod failed with error", "podUID", req.PodUid)
		return nil, err
	}

	aErr := p.adjustAllocationEntries(false)
	if aErr != nil {
		general.ErrorS(aErr, "adjustAllocationEntries failed", "podUID", req.PodUid)
	}
	if err := p.state.StoreState(); err != nil {
		general.ErrorS(err, "store state failed", "podUID", req.PodUid)
	}

	return &pluginapi.RemovePodResponse{}, nil
}

func (p *DynamicPolicy) removePod(podUID string, podEntries state.PodEntries, persistCheckpoint bool) error {
	delete(podEntries, podUID)

	updatedMachineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries, p.state.GetMachineState())
	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	p.state.SetPodEntries(podEntries, false)
	p.state.SetMachineState(updatedMachineState, false)
	if persistCheckpoint {
		return p.state.StoreState()
	}
	return nil
}

func (p *DynamicPolicy) removeContainer(podUID, containerName string, persistCheckpoint bool) error {
	podEntries := p.state.GetPodEntries()

	found := false
	if podEntries[podUID][containerName] != nil {
		found = true
	}

	delete(podEntries[podUID], containerName)

	if !found {
		return nil
	}

	updatedMachineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries, p.state.GetMachineState())
	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	p.state.SetPodEntries(podEntries, false)
	p.state.SetMachineState(updatedMachineState, false)
	if persistCheckpoint {
		return p.state.StoreState()
	}
	return nil
}

// initAdvisorClientConn initializes cpu-advisor related connections
func (p *DynamicPolicy) initAdvisorClientConn() (err error) {
	cpuAdvisorConn, err := process.Dial(
		p.cpuAdvisorSocketAbsPath,
		5*time.Second,
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			// add metadata to outgoing context to indicate that qrm supports GetAdvice.
			// advisor that also supports GetAdvice will ignore such AddContainer/RemovePod requests.
			ctx = metadata.AppendToOutgoingContext(ctx, util.AdvisorRPCMetadataKeySupportsGetAdvice, util.AdvisorRPCMetadataValueSupportsGetAdvice)
			return invoker(ctx, method, req, reply, cc, opts...)
		}),
	)
	if err != nil {
		err = fmt.Errorf("get cpu advisor connection with socket: %s failed with error: %v", p.cpuAdvisorSocketAbsPath, err)
		return
	}

	p.advisorClient = advisorapi.NewCPUAdvisorClient(cpuAdvisorConn)
	p.advisorConn = cpuAdvisorConn
	return nil
}

func (p *DynamicPolicy) initHintOptimizers() error {
	var err error
	p.sharedCoresNUMABindingHintOptimizer, err = registry.SharedCoresHintOptimizerRegistry.HintOptimizer(p.conf.SharedCoresHintOptimizerPolicies,
		p.generateHintOptimizerFactoryOptions())
	if err != nil {
		return fmt.Errorf("SharedCoresHintOptimizerRegistry.HintOptimizer failed with error: %v", err)
	}

	p.dedicatedCoresNUMABindingHintOptimizer, err = registry.DedicatedCoresHintOptimizerRegistry.HintOptimizer(p.conf.DedicatedCoresHintOptimizerPolicies,
		p.generateHintOptimizerFactoryOptions())
	if err != nil {
		return fmt.Errorf("DedicatedCoresHintOptimizerRegistry.HintOptimizer failed with error: %v", err)
	}

	return nil
}

func (p *DynamicPolicy) generateHintOptimizerFactoryOptions() policy.HintOptimizerFactoryOptions {
	return policy.HintOptimizerFactoryOptions{
		Conf:         p.conf,
		Emitter:      p.emitter,
		MetaServer:   p.metaServer,
		State:        p.state,
		ReservedCPUs: p.reservedCPUs,
	}
}

// cleanPools is used to clean pools-related data in local state
func (p *DynamicPolicy) cleanPools() error {
	remainPools := make(map[string]bool)

	// walk through pod entries to put them into specified pool maps
	podEntries := p.state.GetPodEntries()
	for _, entries := range podEntries {
		if entries.IsPoolEntry() {
			continue
		}

		for _, allocationInfo := range entries {
			ownerPool := allocationInfo.GetOwnerPoolName()
			if ownerPool != commonstate.EmptyOwnerPoolName {
				remainPools[ownerPool] = true
			}
		}
	}

	// if pool exists in entries, but has no corresponding container, we need to delete it
	poolsToDelete := sets.NewString()
	for poolName, entries := range podEntries {
		if entries.IsPoolEntry() {
			if !remainPools[poolName] && !state.ResidentPools.Has(poolName) {
				poolsToDelete.Insert(poolName)
			}
		}
	}

	if poolsToDelete.Len() > 0 {
		general.Infof("pools to delete: %v", poolsToDelete.UnsortedList())
		for _, poolName := range poolsToDelete.UnsortedList() {
			delete(podEntries, poolName)
		}

		machineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries, p.state.GetMachineState())
		if err != nil {
			return fmt.Errorf("calculate machineState by podEntries failed with error: %v", err)
		}

		p.state.SetPodEntries(podEntries, false)
		p.state.SetMachineState(machineState, false)
		if err := p.state.StoreState(); err != nil {
			general.ErrorS(err, "store state failed")
		}
	} else {
		general.Infof("there is no pool to delete")
	}

	return nil
}

// initReservePool initializes reserve pool for system cores workload
func (p *DynamicPolicy) initReservePool() error {
	reserveAllocationInfo := p.state.GetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName)
	if reserveAllocationInfo != nil && !reserveAllocationInfo.AllocationResult.IsEmpty() {
		general.Infof("pool: %s allocation result transform from %s to %s",
			commonstate.PoolNameReserve, reserveAllocationInfo.AllocationResult.String(), p.reservedCPUs)
	}

	general.Infof("initReservePool %s: %s", commonstate.PoolNameReserve, p.reservedCPUs)
	topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, p.reservedCPUs)
	if err != nil {
		return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, result cpuset: %s, error: %v",
			commonstate.PoolNameReserve, p.reservedCPUs.String(), err)
	}

	curReserveAllocationInfo := &state.AllocationInfo{
		AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult:                 p.reservedCPUs.Clone(),
		OriginalAllocationResult:         p.reservedCPUs.Clone(),
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
	}
	p.state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, curReserveAllocationInfo, true)

	return nil
}

// initReclaimPool initializes pools for reclaimed-cores.
// if this info already exists in state-file, just use it, otherwise calculate right away
func (p *DynamicPolicy) initReclaimPool() error {
	// for reclaimed pool, we must make them exist when the node isn't in hybrid mode even if cause overlap
	allAvailableCPUs := p.machineInfo.CPUDetails.CPUs().Difference(p.reservedCPUs)
	defaultReservedReclaimedCPUSet, _, tErr := calculator.TakeHTByNUMABalance(p.machineInfo, allAvailableCPUs, p.reservedReclaimedCPUsSize)
	if tErr != nil {
		return fmt.Errorf("fallback TakeHTByNUMABalance faild in generatePoolsAndIsolation for defaultReservedReclaimedCPUSet with error: %v", tErr)
	}
	p.reservedReclaimedCPUSet = defaultReservedReclaimedCPUSet.Clone()

	defaultReservedTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, defaultReservedReclaimedCPUSet)
	if err != nil {
		return fmt.Errorf("unable to calculate defaultReservedTopologyAwareAssignments for pool: %s, "+
			"result cpuset: %s, error: %v", commonstate.PoolNameReclaim, defaultReservedReclaimedCPUSet.String(), err)
	}
	p.reservedReclaimedTopologyAwareAssignments = machine.DeepcopyCPUAssignment(defaultReservedTopologyAwareAssignments)

	reclaimedAllocationInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	if reclaimedAllocationInfo == nil {
		podEntries := p.state.GetPodEntries()
		noneResidentCPUs := podEntries.GetFilteredPoolsCPUSet(state.ResidentPools)

		machineState := p.state.GetMachineState()
		availableCPUs := machineState.GetFilteredAvailableCPUSet(p.reservedCPUs,
			func(ai *state.AllocationInfo) bool {
				return ai.CheckDedicated() || ai.CheckSharedNUMABinding()
			},
			state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicatedNUMABinding)).Difference(noneResidentCPUs)

		var initReclaimedCPUSetSize int
		if availableCPUs.Size() >= p.reservedReclaimedCPUsSize {
			initReclaimedCPUSetSize = p.reservedReclaimedCPUsSize
		} else {
			initReclaimedCPUSetSize = availableCPUs.Size()
		}

		reclaimedCPUSet, _, err := calculator.TakeHTByNUMABalance(p.machineInfo, availableCPUs, initReclaimedCPUSetSize)
		if err != nil {
			return fmt.Errorf("takeByNUMABalance faild in initReclaimPool for %s and %s with error: %v",
				commonstate.PoolNameShare, commonstate.PoolNameReclaim, err)
		}

		// for residual pools, we must make them exist even if cause overlap
		// todo: noneResidentCPUs is the same as reservedCPUs, why should we do this?
		if reclaimedCPUSet.IsEmpty() {
			reclaimedCPUSet = p.reservedReclaimedCPUSet.Clone()
		}

		general.Infof("initReclaimPool %s: %s", commonstate.PoolNameReclaim, reclaimedCPUSet.String())
		topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, reclaimedCPUSet)
		if err != nil {
			return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, "+
				"result cpuset: %s, error: %v", commonstate.PoolNameReclaim, reclaimedCPUSet.String(), err)
		}

		curPoolAllocationInfo := &state.AllocationInfo{
			AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult:                 reclaimedCPUSet.Clone(),
			OriginalAllocationResult:         reclaimedCPUSet.Clone(),
			TopologyAwareAssignments:         topologyAwareAssignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		}
		p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, curPoolAllocationInfo, true)
	} else {
		general.Infof("exist initial %s: %s", commonstate.PoolNameReclaim, reclaimedAllocationInfo.AllocationResult.String())
	}

	return nil
}

func (p *DynamicPolicy) initInterruptPool() error {
	interruptAllocationInfo := p.state.GetAllocationInfo(commonstate.PoolNameInterrupt, commonstate.FakedContainerName)
	if interruptAllocationInfo == nil {
		allocationInfo := &state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameInterrupt),
		}
		p.state.SetAllocationInfo(commonstate.PoolNameInterrupt, commonstate.FakedContainerName, allocationInfo, true)
	} else {
		general.Infof("exist initial %s: %s", commonstate.PoolNameInterrupt, interruptAllocationInfo.AllocationResult.String())
	}

	return nil
}

// getContainerRequestedCores parses and returns request cores for the given container
func (p *DynamicPolicy) getContainerRequestedCores(allocationInfo *state.AllocationInfo) float64 {
	return cpuutil.GetContainerRequestedCores(p.metaServer, allocationInfo)
}

func (p *DynamicPolicy) checkNonBindingShareCoresCpuResource(req *pluginapi.ResourceRequest) (bool, error) {
	_, reqFloat64, err := util.GetPodAggregatedRequestResource(req)
	if err != nil {
		return false, fmt.Errorf("GetQuantityFromResourceReq failed with error: %v", err)
	}

	shareCoresAllocatedInt := state.GetNonBindingSharedRequestedQuantityFromPodEntries(p.state.GetPodEntries(), map[string]float64{req.PodUid: reqFloat64}, p.getContainerRequestedCores)

	machineState := p.state.GetMachineState()
	pooledCPUs := machineState.GetFilteredAvailableCPUSet(p.reservedCPUs,
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicated),
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedOrDedicatedNUMABinding))

	general.Infof("[checkNonBindingShareCoresCpuResource] node cpu allocated: %d, allocatable: %d", shareCoresAllocatedInt, pooledCPUs.Size())
	if shareCoresAllocatedInt > pooledCPUs.Size() {
		general.Warningf("[checkNonBindingShareCoresCpuResource] no enough cpu resource for non-binding share cores pod: %s/%s, container: %s (request: %.02f, node allocated: %d, node allocatable: %d)",
			req.PodNamespace, req.PodName, req.ContainerName, reqFloat64, shareCoresAllocatedInt, pooledCPUs.Size())
		return false, nil
	}

	general.InfoS("checkNonBindingShareCoresCpuResource cpu successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"request", reqFloat64)

	return true, nil
}

func (p *DynamicPolicy) applySidecarAllocationInfoFromMainContainer(sidecarAllocationInfo, mainAllocationInfo *state.AllocationInfo) bool {
	changed := false
	if sidecarAllocationInfo.OwnerPoolName != mainAllocationInfo.OwnerPoolName ||
		!sidecarAllocationInfo.AllocationResult.Equals(mainAllocationInfo.AllocationResult) ||
		!sidecarAllocationInfo.OriginalAllocationResult.Equals(mainAllocationInfo.OriginalAllocationResult) ||
		!state.CheckAllocationInfoTopologyAwareAssignments(sidecarAllocationInfo, mainAllocationInfo) ||
		!state.CheckAllocationInfoOriginTopologyAwareAssignments(sidecarAllocationInfo, mainAllocationInfo) {

		sidecarAllocationInfo.OwnerPoolName = mainAllocationInfo.OwnerPoolName
		sidecarAllocationInfo.AllocationResult = mainAllocationInfo.AllocationResult.Clone()
		sidecarAllocationInfo.OriginalAllocationResult = mainAllocationInfo.OriginalAllocationResult.Clone()
		sidecarAllocationInfo.TopologyAwareAssignments = machine.DeepcopyCPUAssignment(mainAllocationInfo.TopologyAwareAssignments)
		sidecarAllocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(mainAllocationInfo.OriginalTopologyAwareAssignments)

		changed = true
	}

	// Copy missing annotations from main container
	for key, value := range mainAllocationInfo.Annotations {
		if _, ok := sidecarAllocationInfo.Annotations[key]; !ok {
			sidecarAllocationInfo.Annotations[key] = value
			changed = true
		}
	}

	request := p.getContainerRequestedCores(sidecarAllocationInfo)
	if sidecarAllocationInfo.RequestQuantity != request {
		sidecarAllocationInfo.RequestQuantity = request
		changed = true
	}

	return changed
}
