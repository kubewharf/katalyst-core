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

package metricbased

import (
	"io/ioutil"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	nodev1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	optimizerutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuUtil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/metricthreshold"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/npd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/threshold"
)

func TestNewMetricBasedHintOptimizer(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 2)
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: cpuTopology,
			},
		},
	}
	emitter := metrics.DummyMetrics{}
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestNewMetricBasedHintOptimizer")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	stateImpl, err := state.NewCheckpointState(tmpDir, "test", "test", metaServer.CPUTopology, false, state.GenerateMachineStateFromPodEntries, emitter, false)
	require.NoError(t, err)

	options := policy.HintOptimizerFactoryOptions{
		Conf:         conf,
		MetaServer:   metaServer,
		Emitter:      emitter,
		State:        stateImpl,
		ReservedCPUs: machine.NewCPUSet(),
	}

	optimizer, err := NewMetricBasedHintOptimizer(options)
	assert.NoError(t, err)
	assert.NotNil(t, optimizer)

	mbOptimizer, ok := optimizer.(*metricBasedHintOptimizer)
	assert.True(t, ok)
	assert.Equal(t, conf, mbOptimizer.conf)
	assert.Equal(t, metaServer, mbOptimizer.metaServer)
	assert.Equal(t, emitter, mbOptimizer.emitter)
	assert.Equal(t, stateImpl, mbOptimizer.state)
	assert.True(t, mbOptimizer.reservedCPUs.IsEmpty())
	assert.Len(t, mbOptimizer.numaMetrics, 2) // Based on CPUTopology (2 NUMA nodes)
}

func TestMetricBasedHintOptimizer_OptimizeHints(t *testing.T) {
	t.Parallel()

	type fields struct {
		conf        *config.Configuration
		metaServer  *metaserver.MetaServer
		emitter     metrics.MetricEmitter
		state       state.State
		numaMetrics map[int]cpuUtil.SubEntries
	}
	type args struct {
		request hintoptimizer.Request
		hints   *pluginapi.ListOfTopologyHints
	}
	tests := []struct {
		name          string
		fields        fields
		args          args
		wantErr       bool
		expectedHints *pluginapi.ListOfTopologyHints
	}{
		{
			name: "metric policy disabled",
			fields: fields{
				conf: func() *config.Configuration {
					conf := config.NewConfiguration()
					conf.GetDynamicConfiguration().StrategyGroupConfiguration.EnabledStrategies = []v1alpha1.Strategy{}
					return conf
				}(),
				emitter: metrics.DummyMetrics{},
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid: "test-pod",
					},
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: true},
					},
				},
			},
			wantErr: true, // Expect ErrHintOptimizerSkip
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: true},
				},
			},
		},
		// Add more test cases for different scenarios, e.g.:
		// - metric policy enabled, no NUMA over threshold
		// - metric policy enabled, one NUMA over threshold
		// - metric policy enabled, all NUMAs over threshold
		// - error in getNUMAMetricThresholdNameToValue
		// - error in isNUMAOverThreshold
		{
			name: "metric policy enabled, populate hints success",
			fields: fields{
				conf: func() *config.Configuration {
					conf := config.NewConfiguration()
					conf.EnableMetricPreferredNumaAllocation = true
					conf.GetDynamicConfiguration().StrategyGroupConfiguration.EnabledStrategies = []v1alpha1.Strategy{
						{
							Name: pointer.String(consts.StrategyNameMetricPreferredNUMAAllocation),
						},
					}
					conf.GetDynamicConfiguration().MetricThresholdConfiguration = &metricthreshold.MetricThresholdConfiguration{
						Threshold: map[string]map[bool]map[string]float64{
							"Intel_test": {
								false: {
									metricthreshold.NUMACPUUsageRatioThreshold: 0.8,
									metricthreshold.NUMACPULoadRatioThreshold:  0.8,
								},
							},
						},
					}
					return conf
				}(),
				metaServer: func() *metaserver.MetaServer {
					fakeFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
					fakeFetcher.SetByStringIndex(consts.MetricCPUCodeName, "Intel_test")
					fakeFetcher.SetByStringIndex(consts.MetricInfoIsVM, false)
					cpuTopology, _ := machine.GenerateDummyCPUTopology(4, 1, 1) // 1 NUMA node
					return &metaserver.MetaServer{
						MetaAgent: &agent.MetaAgent{
							KatalystMachineInfo: &machine.KatalystMachineInfo{
								CPUTopology: cpuTopology,
							},
							MetricsFetcher: fakeFetcher,
						},
					}
				}(),
				emitter: metrics.DummyMetrics{},
				state: func() state.State {
					tmpDir, _ := ioutil.TempDir("", "test-state")
					defer os.RemoveAll(tmpDir)
					cpuTopology, _ := machine.GenerateDummyCPUTopology(4, 1, 1) // 1 NUMA node
					st, _ := state.NewCheckpointState(tmpDir, "test", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					return st
				}(),
				numaMetrics: func() map[int]cpuUtil.SubEntries {
					nm := map[int]cpuUtil.SubEntries{
						0: {
							metricthreshold.ThresholdNameToResourceName[metricthreshold.NUMACPUUsageRatioThreshold]: cpuUtil.CreateMetricRing(1),
							metricthreshold.ThresholdNameToResourceName[metricthreshold.NUMACPULoadRatioThreshold]:  cpuUtil.CreateMetricRing(1),
						},
					}
					// NUMA 0: current usage 1.0 + request 0.1 = 1.1. Allocatable = 2. Ratio = 1.1/2 = 0.55 (below threshold 0.8)
					nm[0][metricthreshold.ThresholdNameToResourceName[metricthreshold.NUMACPUUsageRatioThreshold]].Push(&cpuUtil.MetricSnapshot{Info: cpuUtil.MetricInfo{Value: 1.0}, Time: time.Now().UnixNano()})
					// NUMA 0: current usage 0.1 + request 0.1 = 0.2. Allocatable = 2. Ratio = 0.2/2 = 0.1 (not over threshold 0.8)
					nm[0][metricthreshold.ThresholdNameToResourceName[metricthreshold.NUMACPULoadRatioThreshold]].Push(&cpuUtil.MetricSnapshot{Info: cpuUtil.MetricInfo{Value: 0.1}, Time: time.Now().UnixNano()})
					return nm
				}(),
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid: "test-pod-enabled",
					},
					CPURequest: 0.1, // 0.1 core
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: true},
					},
				},
			},
			wantErr: false,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: true}, // Assuming it's not over threshold
				},
			},
		},
		{
			name: "metric policy enabled, getNUMAMetricThresholdNameToValue fails",
			fields: fields{
				conf: func() *config.Configuration {
					conf := config.NewConfiguration()
					conf.EnableMetricPreferredNumaAllocation = true
					conf.GetDynamicConfiguration().StrategyGroupConfiguration.EnabledStrategies = []v1alpha1.Strategy{
						{
							Name: pointer.String(consts.StrategyNameMetricPreferredNUMAAllocation),
						},
					}
					// Intentionally make MetricThresholdConfiguration nil to cause an error in getNUMAMetricThresholdNameToValue
					conf.GetDynamicConfiguration().MetricThresholdConfiguration = nil
					return conf
				}(),
				metaServer: func() *metaserver.MetaServer {
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 1)
					return &metaserver.MetaServer{ // MetaServer is needed for the check, but MetricThresholdConfiguration is the key for failure
						MetaAgent: &agent.MetaAgent{
							KatalystMachineInfo: &machine.KatalystMachineInfo{
								CPUTopology: cpuTopology,
							},
						},
					}
				}(),
				emitter: metrics.DummyMetrics{},
				state: func() state.State {
					tmpDir, _ := ioutil.TempDir("", "test-state-fail")
					defer os.RemoveAll(tmpDir)
					cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 1)
					st, _ := state.NewCheckpointState(tmpDir, "test", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					return st
				}(),
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid: "test-pod-fail",
					},
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: true},
					},
				},
			},
			wantErr: true, // Error from populateHintsByMetricPolicy due to getNUMAMetricThresholdNameToValue failure
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: true}, // Hints should remain unchanged on error
				},
			},
		},
		{
			name: "metric policy enabled, one NUMA over threshold",
			fields: fields{
				conf: func() *config.Configuration {
					conf := config.NewConfiguration()
					conf.EnableMetricPreferredNumaAllocation = true
					conf.GetDynamicConfiguration().StrategyGroupConfiguration.EnableStrategyGroup = true
					conf.GetDynamicConfiguration().StrategyGroupConfiguration.EnabledStrategies = []v1alpha1.Strategy{
						{
							Name: pointer.String(consts.StrategyNameMetricPreferredNUMAAllocation),
						},
					}
					conf.GetDynamicConfiguration().MetricThresholdConfiguration = &metricthreshold.MetricThresholdConfiguration{
						Threshold: map[string]map[bool]map[string]float64{
							"Intel_test": {
								false: {
									metricthreshold.NUMACPUUsageRatioThreshold: 0.5, // Lower threshold for testing
									metricthreshold.NUMACPULoadRatioThreshold:  0.5,
								},
							},
						},
					}
					return conf
				}(),
				metaServer: func() *metaserver.MetaServer {
					fakeFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
					fakeFetcher.SetByStringIndex(consts.MetricCPUCodeName, "Intel_test")
					fakeFetcher.SetByStringIndex(consts.MetricInfoIsVM, false)
					cpuTopology, _ := machine.GenerateDummyCPUTopology(4, 1, 2)
					return &metaserver.MetaServer{
						MetaAgent: &agent.MetaAgent{
							KatalystMachineInfo: &machine.KatalystMachineInfo{
								CPUTopology: cpuTopology,
							},
							MetricsFetcher: fakeFetcher,
						},
					}
				}(),
				emitter: metrics.DummyMetrics{},
				state: func() state.State {
					tmpDir, _ := ioutil.TempDir("", "test-state-over")
					defer os.RemoveAll(tmpDir)
					cpuTopology, _ := machine.GenerateDummyCPUTopology(4, 1, 2)
					st, _ := state.NewCheckpointState(tmpDir, "test", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
					return st
				}(),
				numaMetrics: func() map[int]cpuUtil.SubEntries {
					nm := map[int]cpuUtil.SubEntries{
						0: {
							metricthreshold.ThresholdNameToResourceName[metricthreshold.NUMACPUUsageRatioThreshold]: cpuUtil.CreateMetricRing(1),
							metricthreshold.ThresholdNameToResourceName[metricthreshold.NUMACPULoadRatioThreshold]:  cpuUtil.CreateMetricRing(1),
						},
						1: {
							metricthreshold.ThresholdNameToResourceName[metricthreshold.NUMACPUUsageRatioThreshold]: cpuUtil.CreateMetricRing(1),
							metricthreshold.ThresholdNameToResourceName[metricthreshold.NUMACPULoadRatioThreshold]:  cpuUtil.CreateMetricRing(1),
						},
					}
					// NUMA 0: current usage 1.0 + request 0.5 = 1.5. Allocatable = 2. Ratio = 1.5/2 = 0.75 (over threshold 0.5)
					nm[0][metricthreshold.ThresholdNameToResourceName[metricthreshold.NUMACPUUsageRatioThreshold]].Push(&cpuUtil.MetricSnapshot{Info: cpuUtil.MetricInfo{Value: 1.0}, Time: time.Now().UnixNano()})
					nm[0][metricthreshold.ThresholdNameToResourceName[metricthreshold.NUMACPULoadRatioThreshold]].Push(&cpuUtil.MetricSnapshot{Info: cpuUtil.MetricInfo{Value: 1.0}, Time: time.Now().UnixNano()})
					// NUMA 1: current usage 0.1 + request 0.5 = 0.6. Allocatable = 2. Ratio = 0.6/2 = 0.3 (not over threshold 0.5)
					nm[1][metricthreshold.ThresholdNameToResourceName[metricthreshold.NUMACPUUsageRatioThreshold]].Push(&cpuUtil.MetricSnapshot{Info: cpuUtil.MetricInfo{Value: 0.1}, Time: time.Now().UnixNano()})
					nm[1][metricthreshold.ThresholdNameToResourceName[metricthreshold.NUMACPULoadRatioThreshold]].Push(&cpuUtil.MetricSnapshot{Info: cpuUtil.MetricInfo{Value: 0.1}, Time: time.Now().UnixNano()})
					return nm
				}(),
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid: "test-pod-over-threshold",
					},
					CPURequest: 0.5, // 0.5 core
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: true},
						{Nodes: []uint64{1}, Preferred: true},
					},
				},
			},
			wantErr: false,
			expectedHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false}, // NUMA 0 becomes not preferred
					{Nodes: []uint64{1}, Preferred: true},
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &metricBasedHintOptimizer{
				conf:              tt.fields.conf,
				metaServer:        tt.fields.metaServer,
				emitter:           tt.fields.emitter,
				state:             tt.fields.state,
				numaMetrics:       tt.fields.numaMetrics,
				metricSampleTime:  10 * time.Minute,
				metricSampleCount: 1,
			}
			err := o.OptimizeHints(tt.args.request, tt.args.hints)
			if tt.wantErr {
				assert.Error(t, err)
				if optimizerutil.IsSkipOptimizeHintsError(err) {
					assert.True(t, optimizerutil.IsSkipOptimizeHintsError(err))
				}
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedHints, tt.args.hints)
		})
	}
}

func TestMetricBasedHintOptimizer_isNUMAOverThreshold(t *testing.T) {
	t.Parallel()

	// cpuTopology, _ := machine.GenerateDummyCPUTopology(8, 1, 2) // 2 NUMA nodes, 4 CPUs each
	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3)},
		1: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(4, 5, 6, 7)},
	}

	type fields struct {
		numaMetrics  map[int]cpuUtil.SubEntries
		reservedCPUs machine.CPUSet
	}
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
			name: "over threshold",
			fields: fields{
				numaMetrics: map[int]cpuUtil.SubEntries{
					0: {
						consts.MetricCPUUsageContainer: func() *cpuUtil.MetricRing {
							r := cpuUtil.CreateMetricRing(1)
							r.Push(&cpuUtil.MetricSnapshot{Info: cpuUtil.MetricInfo{Value: 3.0}, Time: time.Now().UnixNano()})
							return r
						}(),
					},
				},
				reservedCPUs: machine.NewCPUSet(),
			},
			args: args{
				numa:         0,
				threshold:    0.8,
				request:      1.0, // 3.0 (current) + 1.0 (request) = 4.0. Allocatable = 4. Ratio = 4.0/4 = 1.0. 1.0 >= 0.8
				resourceName: consts.MetricCPUUsageContainer,
				machineState: machineState,
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "not over threshold",
			fields: fields{
				numaMetrics: map[int]cpuUtil.SubEntries{
					0: {
						consts.MetricCPUUsageContainer: func() *cpuUtil.MetricRing {
							r := cpuUtil.CreateMetricRing(1)
							r.Push(&cpuUtil.MetricSnapshot{Info: cpuUtil.MetricInfo{Value: 1.0}, Time: time.Now().UnixNano()})
							return r
						}(),
					},
				},
				reservedCPUs: machine.NewCPUSet(),
			},
			args: args{
				numa:         0,
				threshold:    0.8,
				request:      1.0, // 1.0 (current) + 1.0 (request) = 2.0. Allocatable = 4. Ratio = 2.0/4 = 0.5. 0.5 < 0.8
				resourceName: consts.MetricCPUUsageContainer,
				machineState: machineState,
			},
			want:    false,
			wantErr: false,
		},
		{
			name: "invalid machine state - nil",
			fields: fields{
				numaMetrics: map[int]cpuUtil.SubEntries{},
			},
			args: args{
				numa:         0,
				machineState: nil,
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "invalid machine state - numa not found",
			fields: fields{
				numaMetrics: map[int]cpuUtil.SubEntries{},
			},
			args: args{
				numa:         99, // Non-existent NUMA node
				machineState: machineState,
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "invalid numaMetrics - resource not found",
			fields: fields{
				numaMetrics: map[int]cpuUtil.SubEntries{
					0: {},
				},
			},
			args: args{
				numa:         0,
				resourceName: "non_existent_metric",
				machineState: machineState,
			},
			want:    false,
			wantErr: true, // Should be error because numaMetrics[0][resourceName] is nil
		},
		{
			name: "allocatable is zero",
			fields: fields{
				numaMetrics: map[int]cpuUtil.SubEntries{
					0: {
						consts.MetricCPUUsageContainer: cpuUtil.CreateMetricRing(1),
					},
				},
				reservedCPUs: machine.NewCPUSet(0, 1, 2, 3), // Reserve all CPUs on NUMA 0
			},
			args: args{
				numa:         0,
				threshold:    0.8,
				request:      1.0,
				resourceName: consts.MetricCPUUsageContainer,
				machineState: machineState,
			},
			want:    false,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &metricBasedHintOptimizer{
				mutex:             sync.RWMutex{},
				numaMetrics:       tt.fields.numaMetrics,
				reservedCPUs:      tt.fields.reservedCPUs,
				metricSampleTime:  10 * time.Minute,
				metricSampleCount: 1,
			}
			got, err := o.isNUMAOverThreshold(tt.args.numa, tt.args.threshold, tt.args.request, tt.args.resourceName, tt.args.machineState)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMetricBasedHintOptimizer_collectNUMAMetrics(t *testing.T) {
	t.Parallel()

	cpuTopology, _ := machine.GenerateDummyCPUTopology(8, 1, 2) // 2 NUMA nodes
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestCollectNUMAMetrics")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	stateImpl, err := state.NewCheckpointState(tmpDir, "test", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{}, false)
	require.NoError(t, err)

	podUID1 := "pod1"
	containerName1 := "container1"
	podUID2 := "pod2"
	containerName2 := "container2"

	// Setup machine state with pods on different NUMA nodes
	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{
			PodEntries: state.PodEntries{
				podUID1: state.ContainerEntries{
					containerName1: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid: podUID1, ContainerName: containerName1,
							Annotations: map[string]string{
								apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
						},
					},
				},
			},
		},
		1: &state.NUMANodeState{
			PodEntries: state.PodEntries{
				podUID2: state.ContainerEntries{
					containerName2: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid: podUID2, ContainerName: containerName2,
							Annotations: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable},
							QoSLevel:    apiconsts.PodAnnotationQoSLevelSharedCores,
						},
					},
				},
			},
		},
	}
	stateImpl.SetMachineState(machineState, false)

	fakeFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	fakeFetcher.SetContainerMetric(podUID1, containerName1, consts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 2.5})
	fakeFetcher.SetContainerMetric(podUID2, containerName2, consts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 1.5})

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{CPUTopology: cpuTopology},
			MetricsFetcher:      fakeFetcher,
		},
	}

	conf := config.NewConfiguration()
	conf.EnableMetricPreferredNumaAllocation = true
	conf.GetDynamicConfiguration().StrategyGroupConfiguration.EnabledStrategies = []v1alpha1.Strategy{
		{
			Name: pointer.String(consts.StrategyNameMetricPreferredNUMAAllocation),
		},
	}

	o := &metricBasedHintOptimizer{
		conf:       conf,
		emitter:    metrics.DummyMetrics{},
		metaServer: metaServer,
		state:      stateImpl,
		numaMetrics: map[int]cpuUtil.SubEntries{
			0: {consts.MetricCPUUsageContainer: cpuUtil.CreateMetricRing(1)},
			1: {consts.MetricCPUUsageContainer: cpuUtil.CreateMetricRing(1)},
		},
		metricSampleTime:  10 * time.Minute,
		metricSampleCount: 1,
	}

	o.collectNUMAMetrics()

	// Check NUMA 0 metrics
	val0, err := o.numaMetrics[0][consts.MetricCPUUsageContainer].AvgAfterTimestampWithCountBound(0, 1)
	assert.NoError(t, err)
	assert.Equal(t, 2.5, val0)

	// Check NUMA 1 metrics
	val1, err := o.numaMetrics[1][consts.MetricCPUUsageContainer].AvgAfterTimestampWithCountBound(0, 1)
	assert.NoError(t, err)
	assert.Equal(t, 1.5, val1)
}

func TestMetricBasedHintOptimizer_getNUMAMetric(t *testing.T) {
	t.Parallel()

	podUID1 := "pod1"
	containerName1 := "c1"
	podUID2 := "pod2"
	containerName2 := "c2"

	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{
			PodEntries: state.PodEntries{
				podUID1: state.ContainerEntries{
					containerName1: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid: podUID1, ContainerName: containerName1,
							Annotations: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable},
							QoSLevel:    apiconsts.PodAnnotationQoSLevelSharedCores,
						},
					},
				},
				podUID2: state.ContainerEntries{ // Pod2 also on NUMA 0
					containerName2: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid: podUID2, ContainerName: containerName2,
							Annotations: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable},
							QoSLevel:    apiconsts.PodAnnotationQoSLevelSharedCores,
						},
					},
				},
			},
		},
		1: &state.NUMANodeState{ // NUMA 1 has no pods with NUMA binding
			PodEntries: state.PodEntries{},
		},
	}

	fakeFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	fakeFetcher.SetContainerMetric(podUID1, containerName1, consts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 2.0})
	fakeFetcher.SetContainerMetric(podUID2, containerName2, consts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 3.0})

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			MetricsFetcher: fakeFetcher,
		},
	}

	o := &metricBasedHintOptimizer{
		metaServer: metaServer,
	}

	type args struct {
		numa         int
		resourceName string
		machineState state.NUMANodeMap
	}
	tests := []struct {
		name    string
		args    args
		want    float64
		wantErr bool
	}{
		{
			name: "sum metrics for NUMA 0",
			args: args{
				numa:         0,
				resourceName: consts.MetricCPUUsageContainer,
				machineState: machineState,
			},
			want:    5.0, // 2.0 (pod1/c1) + 3.0 (pod2/c2)
			wantErr: false,
		},
		{
			name: "no metrics for NUMA 1",
			args: args{
				numa:         1,
				resourceName: consts.MetricCPUUsageContainer,
				machineState: machineState,
			},
			want:    0.0,
			wantErr: false,
		},
		{
			name: "invalid machine state - nil",
			args: args{
				numa:         0,
				machineState: nil,
			},
			want:    0.0,
			wantErr: true,
		},
		{
			name: "invalid machine state - numa not found",
			args: args{
				numa:         99,
				machineState: machineState,
			},
			want:    0.0,
			wantErr: true,
		},
		{
			name: "error fetching metric",
			args: args{
				numa:         0,
				resourceName: "error_metric", // Assume this causes GetContainerMetric to error
				machineState: machineState,
			},
			want:    0.0,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := o.getNUMAMetric(tt.args.numa, tt.args.resourceName, tt.args.machineState)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMetricBasedHintOptimizer_getNUMAMetricThresholdNameToValue(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	conf.DynamicAgentConfiguration = dynamic.NewDynamicAgentConfiguration()
	conf.GetDynamicConfiguration().MetricThresholdConfiguration = &metricthreshold.MetricThresholdConfiguration{
		Threshold: map[string]map[bool]map[string]float64{
			"Intel_test": {
				false: {
					metricthreshold.NUMACPUUsageRatioThreshold: 0.75,
					metricthreshold.NUMACPULoadRatioThreshold:  0.65, // Example for another threshold
				},
			},
		},
	}

	fakeFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	// Mock GetCpuCodeName and GetIsVm behavior by setting values in fakeFetcher
	// This is a simplified way; real scenarios might need more direct mocking of helper functions.
	fakeFetcher.SetByStringIndex(consts.MetricCPUCodeName, "Intel_test")
	fakeFetcher.SetByStringIndex(consts.MetricInfoIsVM, false)

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			MetricsFetcher: fakeFetcher,
		},
	}

	o := &metricBasedHintOptimizer{
		conf:       conf,
		metaServer: metaServer,
	}

	want := map[string]float64{
		metricthreshold.NUMACPUUsageRatioThreshold: 0.75,
		metricthreshold.NUMACPULoadRatioThreshold:  0.65,
	}

	got, err := o.getNUMAMetricThresholdNameToValue()
	assert.NoError(t, err)
	assert.Equal(t, want, got)

	metaServer.NPDFetcher = &npd.DummyNPDFetcher{
		NPD: &nodev1.NodeProfileDescriptor{
			Status: nodev1.NodeProfileDescriptorStatus{
				NodeMetrics: []nodev1.ScopedNodeMetrics{
					{
						Scope: threshold.ScopeMetricThreshold,
						Metrics: []nodev1.MetricValue{
							{
								MetricName: metricthreshold.NUMACPUUsageRatioThreshold,
								Value:      resource.MustParse("0.55"),
							},
						},
					},
				},
			},
		},
	}
	want = map[string]float64{
		metricthreshold.NUMACPUUsageRatioThreshold: 0.55,
	}
	got, err = o.getNUMAMetricThresholdNameToValue()
	assert.NoError(t, err)
	assert.Equal(t, want, got)

	// Test case: nil dynamicConf
	oNilDynamicConf := &metricBasedHintOptimizer{
		conf:       config.NewConfiguration(), // Fresh config without DynamicAgentConfiguration set
		metaServer: metaServer,
	}
	oNilDynamicConf.conf.DynamicAgentConfiguration = nil
	_, err = oNilDynamicConf.getNUMAMetricThresholdNameToValue()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil dynamicConf")

	// Test case: nil metaServer
	oNilMetaServer := &metricBasedHintOptimizer{
		conf:       conf,
		metaServer: nil,
	}
	_, err = oNilMetaServer.getNUMAMetricThresholdNameToValue()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil metaServer")

	// Test case: nil metricThreshold
	confNoMetricThreshold := config.NewConfiguration()
	confNoMetricThreshold.DynamicAgentConfiguration = dynamic.NewDynamicAgentConfiguration() // MetricThresholdConfiguration is nil by default
	oNilMetricThreshold := &metricBasedHintOptimizer{
		conf:       confNoMetricThreshold,
		metaServer: metaServer,
	}
	confNoMetricThreshold.GetDynamicConfiguration().MetricThresholdConfiguration = nil
	_, err = oNilMetricThreshold.getNUMAMetricThresholdNameToValue()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil metricThreshold")
}
