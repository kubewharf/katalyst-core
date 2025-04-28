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
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	coreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func TestDynamicPolicy_collectNUMAMetrics(t *testing.T) {
	t.Parallel()
	as := require.New(t)
	testingDir, err := ioutil.TempDir("", "dynamic_policy_numa_metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testingDir)

	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 4)
	state1, err := state.NewCheckpointState(testingDir, "test", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries)
	as.Nil(err)
	testName := "test"
	podUID := "373d08e4-7a6b-4293-aaaf-b135ff812aaa"
	containerName := testName
	emitter := metrics.DummyMetrics{}
	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{
			DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(0).Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA0",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("3,11"),
						OriginalAllocationResult: machine.MustParse("3,11"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 11),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 11),
						},
						RequestQuantity: 2,
					},
				},
			},
		},
		1: &state.NUMANodeState{
			DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(1).Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries:      state.PodEntries{},
		},
		2: &state.NUMANodeState{
			DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(2).Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries:      state.PodEntries{},
		},
		3: &state.NUMANodeState{
			DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(3).Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries:      state.PodEntries{},
		},
	}
	state1.SetMachineState(machineState, false)
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetContainerMetric(podUID, containerName, coreconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 5})
	fakeFetcher.SetContainerMetric(podUID, containerName, coreconsts.MetricLoad1MinContainer, utilmetric.MetricData{Value: 5})
	metaServer := &metaserver.MetaServer{
		MetaAgent: &metaagent.MetaAgent{
			MetricsFetcher: fakeFetcher,
		},
	}
	numaMetrics := make(map[int]strategy.SubEntries)
	for _, numaID := range cpuTopology.CPUDetails.NUMANodes().ToSliceInt() {
		numaMetrics[numaID] = make(strategy.SubEntries)
	}
	type fields struct {
		metaServer  *metaserver.MetaServer
		numaMetrics map[int]strategy.SubEntries
	}
	tests := []struct {
		name   string
		fields fields
	}{
		{
			name: "normal case",
			fields: fields{
				metaServer:  metaServer,
				numaMetrics: numaMetrics,
			},
		},
	}
	for _, tt := range tests {
		ttt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &DynamicPolicy{
				metaServer:  ttt.fields.metaServer,
				numaMetrics: ttt.fields.numaMetrics,
				emitter:     metrics.DummyMetrics{},
				state:       state1,
			}
			p.collectNUMAMetrics()
		})
	}
}

func TestDynamicPolicy_getNUMAMeric(t *testing.T) {
	t.Parallel()
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 4)
	testName := "test"
	podUID := "373d08e4-7a6b-4293-aaaf-b135ff812aaa"
	containerName := testName
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetContainerMetric(podUID, containerName, coreconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 5})
	fakeFetcher.SetContainerMetric(podUID, containerName, coreconsts.MetricLoad1MinContainer, utilmetric.MetricData{Value: 5})
	metaServer := &metaserver.MetaServer{
		MetaAgent: &metaagent.MetaAgent{
			MetricsFetcher: fakeFetcher,
		},
	}
	type fields struct {
		metaServer *metaserver.MetaServer
	}
	type args struct {
		numa         int
		resourceName string
		machineState state.NUMANodeMap
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    float64
		wantErr bool
	}{
		{
			name: "normal case",
			fields: fields{
				metaServer: metaServer,
			},
			args: args{
				numa:         0,
				resourceName: coreconsts.MetricCPUUsageContainer,
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(0).Clone(),
						AllocatedCPUSet: machine.NewCPUSet(),
						PodEntries: state.PodEntries{
							"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
								testName: &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
										PodNamespace:   testName,
										PodName:        testName,
										ContainerName:  testName,
										ContainerType:  pluginapi.ContainerType_MAIN.String(),
										ContainerIndex: 0,
										OwnerPoolName:  "share-NUMA0",
										Labels: map[string]string{
											consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
										},
										Annotations: map[string]string{
											consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
											consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
										},
										QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
									},
									RampUp:                   false,
									AllocationResult:         machine.MustParse("3,11"),
									OriginalAllocationResult: machine.MustParse("3,11"),
									TopologyAwareAssignments: map[int]machine.CPUSet{
										1: machine.NewCPUSet(3, 11),
									},
									OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
										1: machine.NewCPUSet(3, 11),
									},
									RequestQuantity: 2,
								},
							},
						},
					},
					1: &state.NUMANodeState{
						DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(1).Clone(),
						AllocatedCPUSet: machine.NewCPUSet(),
						PodEntries:      state.PodEntries{},
					},
					2: &state.NUMANodeState{
						DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(2).Clone(),
						AllocatedCPUSet: machine.NewCPUSet(),
						PodEntries:      state.PodEntries{},
					},
					3: &state.NUMANodeState{
						DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(3).Clone(),
						AllocatedCPUSet: machine.NewCPUSet(),
						PodEntries:      state.PodEntries{},
					},
				},
			},
			want:    5,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		ttt := tt
		t.Run(ttt.name, func(t *testing.T) {
			t.Parallel()
			p := &DynamicPolicy{
				metaServer: ttt.fields.metaServer,
			}
			got, err := p.getNUMAMeric(ttt.args.numa, ttt.args.resourceName, ttt.args.machineState)
			if (err != nil) != ttt.wantErr {
				t.Errorf("DynamicPolicy.getNUMAMeric() error = %v, wantErr %v", err, ttt.wantErr)
				return
			}
			if got != ttt.want {
				t.Errorf("DynamicPolicy.getNUMAMeric() = %v, want %v", got, ttt.want)
			}
		})
	}
}

func TestDynamicPolicy_getNUMAMetricThreshold(t *testing.T) {
	t.Parallel()
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	// create metric data without time.
	fakeFetcher.SetByStringIndex(coreconsts.MetricCPUCodeName, "Intel_CascadeLake")
	fakeFetcher.SetByStringIndex(coreconsts.MetricInfoIsVM, false)
	metaServer := &metaserver.MetaServer{
		MetaAgent: &metaagent.MetaAgent{
			MetricsFetcher: fakeFetcher,
		},
	}
	dynamicConfig := dynamicconfig.NewDynamicAgentConfiguration()

	type fields struct {
		metaServer    *metaserver.MetaServer
		dynamicConfig *dynamicconfig.DynamicAgentConfiguration
	}
	type args struct {
		resourceName string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    float64
		wantErr bool
	}{
		{
			name: "normal case",
			fields: fields{
				metaServer:    metaServer,
				dynamicConfig: dynamicConfig,
			},
			args: args{
				resourceName: coreconsts.MetricCPUUsageContainer,
			},
			want:    0.55,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &DynamicPolicy{
				metaServer:    tt.fields.metaServer,
				dynamicConfig: tt.fields.dynamicConfig,
			}
			got, err := p.getNUMAMetricThreshold(tt.args.resourceName)
			if (err != nil) != tt.wantErr {
				t.Errorf("DynamicPolicy.getNUMAMetricThreshold() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("DynamicPolicy.getNUMAMetricThreshold() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDynamicPolicy_isNUMAOverThreshold(t *testing.T) {
	t.Parallel()
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 4)
	numaMetrics := make(map[int]strategy.SubEntries)
	for _, numaID := range cpuTopology.CPUDetails.NUMANodes().ToSliceInt() {
		numaMetrics[numaID] = make(strategy.SubEntries)
	}
	testName := "test"
	podUID := "373d08e4-7a6b-4293-aaaf-b135ff812aaa"
	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	containerName := testName
	// create metric data without time.
	fakeFetcher.SetContainerMetric(podUID, containerName, coreconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 3})
	fakeFetcher.SetContainerMetric(podUID, containerName, coreconsts.MetricLoad1MinContainer, utilmetric.MetricData{Value: 3})
	metaServer := &metaserver.MetaServer{
		MetaAgent: &metaagent.MetaAgent{
			MetricsFetcher: fakeFetcher,
		},
	}
	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{
			DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(0).Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA0",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("3,11"),
						OriginalAllocationResult: machine.MustParse("3,11"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 11),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 11),
						},
						RequestQuantity: 2,
					},
				},
			},
		},
		1: &state.NUMANodeState{
			DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(1).Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries:      state.PodEntries{},
		},
		2: &state.NUMANodeState{
			DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(2).Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries:      state.PodEntries{},
		},
		3: &state.NUMANodeState{
			DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(3).Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries:      state.PodEntries{},
		},
	}
	testingDir, err := ioutil.TempDir("", "dynamic_policy_numa_metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testingDir)
	state1, err := state.NewCheckpointState(testingDir, "test", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries)
	if err != nil {
		t.Fatal(err)
	}
	state1.SetMachineState(machineState, false)
	p := &DynamicPolicy{
		reservedCPUs: machine.NewCPUSet(),
		metaServer:   metaServer,
		numaMetrics:  numaMetrics,
		state:        state1,
		emitter:      emitter,
	}
	for i := 0; i < 20; i++ {
		p.collectNUMAMetrics()
	}
	type fields struct{}
	type args struct {
		numa         int
		threshold    float64
		request      float64
		resourceName string
		machineState state.NUMANodeMap
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    bool
		wantErr bool
	}{
		{
			name:   "over threshold",
			fields: fields{},
			args: args{
				numa:         0,
				threshold:    0.5,
				request:      1,
				resourceName: coreconsts.MetricCPUUsageContainer,
				machineState: machineState,
			},
			want:    true,
			wantErr: false,
		},
		{
			name:   "under threshold",
			fields: fields{},
			args: args{
				numa:         1,
				threshold:    0.5,
				request:      1,
				resourceName: coreconsts.MetricCPUUsageContainer,
				machineState: machineState,
			},
			want:    false,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := p.isNUMAOverThreshold(tt.args.numa, tt.args.threshold, tt.args.request, tt.args.resourceName, tt.args.machineState)
			if (err != nil) != tt.wantErr {
				t.Errorf("DynamicPolicy.isNUMAOverThreshold() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("DynamicPolicy.isNUMAOverThreshold() = %v, want %v", got, tt.want)
			}
		})
	}
}

//func TestDynamicPolicy_populateHintsByMetricPolicy(t *testing.T) {
//	type fields struct {
//		RWMutex                                   sync.RWMutex
//		name                                      string
//		stopCh                                    chan struct{}
//		started                                   bool
//		emitter                                   metrics.MetricEmitter
//		metaServer                                *metaserver.MetaServer
//		machineInfo                               *machine.KatalystMachineInfo
//		advisorClient                             advisorapi.CPUAdvisorClient
//		advisorConn                               *grpc.ClientConn
//		advisorValidator                          *validator.CPUAdvisorValidator
//		UnimplementedCPUPluginServer              advisorapi.UnimplementedCPUPluginServer
//		advisorMonitor                            *timemonitor.TimeMonitor
//		state                                     state.State
//		residualHitMap                            map[string]int64
//		allocationHandlers                        map[string]util.AllocationHandler
//		hintHandlers                              map[string]util.HintHandler
//		cpuPressureEviction                       agent.Component
//		cpuPressureEvictionCancel                 context.CancelFunc
//		enableCPUAdvisor                          bool
//		getAdviceInterval                         time.Duration
//		reservedCPUs                              machine.CPUSet
//		cpuAdvisorSocketAbsPath                   string
//		cpuPluginSocketAbsPath                    string
//		extraStateFileAbsPath                     string
//		enableReclaimNUMABinding                  bool
//		enableSNBHighNumaPreference               bool
//		enableCPUIdle                             bool
//		enableSyncingCPUIdle                      bool
//		reclaimRelativeRootCgroupPath             string
//		numaBindingReclaimRelativeRootCgroupPaths map[int]string
//		qosConfig                                 *generic.QoSConfiguration
//		dynamicConfig                             *dynamicconfig.DynamicAgentConfiguration
//		conf                                      *config.Configuration
//		podDebugAnnoKeys                          []string
//		podAnnotationKeptKeys                     []string
//		podLabelKeptKeys                          []string
//		sharedCoresNUMABindingResultAnnotationKey string
//		transitionPeriod                          time.Duration
//		cpuNUMAHintPreferPolicy                   string
//		cpuNUMAHintPreferLowThreshold             float64
//		reservedReclaimedCPUsSize                 int
//		reservedReclaimedCPUSet                   machine.CPUSet
//		reservedReclaimedTopologyAwareAssignments map[int]machine.CPUSet
//		numaMetrics                               map[int]strategy.SubEntries
//	}
//	type args struct {
//		numaNodes    []int
//		hints        *pluginapi.ListOfTopologyHints
//		machineState state.NUMANodeMap
//		request      float64
//	}
//	tests := []struct {
//		name    string
//		fields  fields
//		args    args
//		wantErr bool
//	}{
//		// TODO: Add test cases.
//	}
//	for _, tt := range tests {
//		t.Run(tt.name, func(t *testing.T) {
//			p := &DynamicPolicy{
//				RWMutex:                       tt.fields.RWMutex,
//				name:                          tt.fields.name,
//				stopCh:                        tt.fields.stopCh,
//				started:                       tt.fields.started,
//				emitter:                       tt.fields.emitter,
//				metaServer:                    tt.fields.metaServer,
//				machineInfo:                   tt.fields.machineInfo,
//				advisorClient:                 tt.fields.advisorClient,
//				advisorConn:                   tt.fields.advisorConn,
//				advisorValidator:              tt.fields.advisorValidator,
//				UnimplementedCPUPluginServer:  tt.fields.UnimplementedCPUPluginServer,
//				advisorMonitor:                tt.fields.advisorMonitor,
//				state:                         tt.fields.state,
//				residualHitMap:                tt.fields.residualHitMap,
//				allocationHandlers:            tt.fields.allocationHandlers,
//				hintHandlers:                  tt.fields.hintHandlers,
//				cpuPressureEviction:           tt.fields.cpuPressureEviction,
//				cpuPressureEvictionCancel:     tt.fields.cpuPressureEvictionCancel,
//				enableCPUAdvisor:              tt.fields.enableCPUAdvisor,
//				getAdviceInterval:             tt.fields.getAdviceInterval,
//				reservedCPUs:                  tt.fields.reservedCPUs,
//				cpuAdvisorSocketAbsPath:       tt.fields.cpuAdvisorSocketAbsPath,
//				cpuPluginSocketAbsPath:        tt.fields.cpuPluginSocketAbsPath,
//				extraStateFileAbsPath:         tt.fields.extraStateFileAbsPath,
//				enableReclaimNUMABinding:      tt.fields.enableReclaimNUMABinding,
//				enableSNBHighNumaPreference:   tt.fields.enableSNBHighNumaPreference,
//				enableCPUIdle:                 tt.fields.enableCPUIdle,
//				enableSyncingCPUIdle:          tt.fields.enableSyncingCPUIdle,
//				reclaimRelativeRootCgroupPath: tt.fields.reclaimRelativeRootCgroupPath,
//				numaBindingReclaimRelativeRootCgroupPaths: tt.fields.numaBindingReclaimRelativeRootCgroupPaths,
//				qosConfig:             tt.fields.qosConfig,
//				dynamicConfig:         tt.fields.dynamicConfig,
//				conf:                  tt.fields.conf,
//				podDebugAnnoKeys:      tt.fields.podDebugAnnoKeys,
//				podAnnotationKeptKeys: tt.fields.podAnnotationKeptKeys,
//				podLabelKeptKeys:      tt.fields.podLabelKeptKeys,
//				sharedCoresNUMABindingResultAnnotationKey: tt.fields.sharedCoresNUMABindingResultAnnotationKey,
//				transitionPeriod:                          tt.fields.transitionPeriod,
//				cpuNUMAHintPreferPolicy:                   tt.fields.cpuNUMAHintPreferPolicy,
//				cpuNUMAHintPreferLowThreshold:             tt.fields.cpuNUMAHintPreferLowThreshold,
//				reservedReclaimedCPUsSize:                 tt.fields.reservedReclaimedCPUsSize,
//				reservedReclaimedCPUSet:                   tt.fields.reservedReclaimedCPUSet,
//				reservedReclaimedTopologyAwareAssignments: tt.fields.reservedReclaimedTopologyAwareAssignments,
//				numaMetrics:                               tt.fields.numaMetrics,
//			}
//			if err := p.populateHintsByMetricPolicy(tt.args.numaNodes, tt.args.hints, tt.args.machineState, tt.args.request); (err != nil) != tt.wantErr {
//				t.Errorf("DynamicPolicy.populateHintsByMetricPolicy() error = %v, wantErr %v", err, tt.wantErr)
//			}
//		})
//	}
//}
