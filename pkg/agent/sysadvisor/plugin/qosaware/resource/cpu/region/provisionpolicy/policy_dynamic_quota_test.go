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

package provisionpolicy

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	metricutil "github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/reclaim"
)

func generateDynamicQuotaTestConfiguration(t *testing.T, checkpointDir, stateFileDir, checkpointManagerDir string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir
	conf.CheckpointManagerDir = checkpointManagerDir
	conf.ReclaimRelativeRootCgroupPath = "/kubepods/besteffort"

	conf.GetDynamicConfiguration().RegionIndicatorTargetConfiguration = map[v1alpha1.QoSRegionType][]v1alpha1.IndicatorTargetConfiguration{
		v1alpha1.QoSRegionTypeShare: {
			{
				Name: workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio,
			},
		},
	}

	conf.GetDynamicConfiguration().EnableReclaim = true

	return conf
}

// dqRegistryMu serializes any parallel test in this file that mutates the
// process-global reclaim consumer registry (via newTestPolicyDynamicQuota or
// direct RegisterNamedGenericConsumer calls). Locking this at the start of
// each parallel subtest is the simplest way to keep the racy "unregister ->
// register" pattern safe across TestPolicyDynamicQuota_* and their subtests.
var dqRegistryMu sync.Mutex

func newTestPolicyDynamicQuota(t *testing.T, checkpointDir string, stateFileDir string,
	checkpointManagerDir string, regionInfo types.RegionInfo, metricFetcher metrictypes.MetricsFetcher,
) ProvisionPolicy {
	conf := generateDynamicQuotaTestConfiguration(t, checkpointDir, stateFileDir, checkpointManagerDir)

	// clear any prior registration; NewMetaServer below calls SetupConsumers,
	// which registers the default "generic" reclaim consumer from conf.
	reclaim.UnregisterConsumer(reclaim.GenericConsumerName)
	metaCacheTmp, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricFetcher)
	require.NoError(t, err)
	require.NotNil(t, metaCacheTmp)

	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	require.NoError(t, err)

	metaServerTmp, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	assert.NoError(t, err)
	require.NotNil(t, metaServerTmp)
	metaServerTmp.SetMetricFetcher(metricFetcher)

	p := NewPolicyDynamicQuota(regionInfo.RegionName, regionInfo.RegionType, regionInfo.OwnerPoolName,
		conf, nil, metaCacheTmp, metaServerTmp, metrics.DummyMetrics{})
	err = metaCacheTmp.SetRegionInfo(regionInfo.RegionName, &regionInfo)
	assert.NoError(t, err)

	p.SetBindingNumas(regionInfo.BindingNumas, true)

	return p
}

func TestPolicyDynamicQuota_updateForCPUQuota(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tests := []struct {
		name                   string
		regionInfo             types.RegionInfo
		controlEssentials      types.ControlEssentials
		reclaimPoolExist       bool
		reclaimPoolAssignments map[int]machine.CPUSet
		metricsData            map[string]metricutil.MetricData
		metricError            bool
		zeroCPU                bool
		notNUMABinding         bool
		reservedForReclaim     float64
		wantControlKnob        types.ControlKnob
		wantErr                bool
	}{
		{
			name: "normal case: reclaim pool exists",
			regionInfo: types.RegionInfo{
				RegionName:   "share-xxx",
				RegionType:   v1alpha1.QoSRegionTypeShare,
				BindingNumas: machine.NewCPUSet(0),
			},
			controlEssentials: types.ControlEssentials{
				Indicators: types.Indicator{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): {
						Target:  0.6,
						Current: 0.4,
					},
				},
			},
			reclaimPoolExist: true,
			reclaimPoolAssignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(10, 11, 12, 13), // Size 4
			},
			metricsData: map[string]metricutil.MetricData{
				common.GetReclaimRelativeRootCgroupPath("/kubepods/besteffort", 0): {
					Value: 2.0,
					Time:  &now,
				},
			},
			reservedForReclaim: 1.0,
			// quota = Max(reclaimNUMACPUSize * (target - current) + usage, reserved)
			// quota = Max(4 * (0.6 - 0.4) + 2.0, 1.0) = Max(0.8 + 2.0, 1.0) = 2.8
			wantControlKnob: types.ControlKnob{
				v1alpha1.ControlKnobReclaimedCoresCPUQuota: types.ControlKnobItem{
					Value:  2.8,
					Action: types.ControlKnobActionNone,
				},
			},
			wantErr: false,
		},
		{
			name: "fallback case: reclaim pool not exist",
			regionInfo: types.RegionInfo{
				RegionName:   "share-xxx",
				RegionType:   v1alpha1.QoSRegionTypeShare,
				BindingNumas: machine.NewCPUSet(0),
			},
			controlEssentials: types.ControlEssentials{
				Indicators: types.Indicator{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): {
						Target:  0.6,
						Current: 0.4,
					},
				},
			},
			reclaimPoolExist: false,
			metricsData: map[string]metricutil.MetricData{
				common.GetReclaimRelativeRootCgroupPath("/kubepods/besteffort", 0): {
					Value: 2.0,
					Time:  &now,
				},
			},
			reservedForReclaim: 1.0,
			// Fallback: NUMA CPU size.
			wantControlKnob: types.ControlKnob{
				v1alpha1.ControlKnobReclaimedCoresCPUQuota: types.ControlKnobItem{
					Value:  10.799999999999997, // 44 * 0.2 + 2.0
					Action: types.ControlKnobActionNone,
				},
			},
			wantErr: false,
		},
		{
			name: "error case: metric missing",
			regionInfo: types.RegionInfo{
				RegionName:   "share-xxx",
				RegionType:   v1alpha1.QoSRegionTypeShare,
				BindingNumas: machine.NewCPUSet(0),
			},
			controlEssentials: types.ControlEssentials{
				Indicators: types.Indicator{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): {
						Target:  0.6,
						Current: 0.4,
					},
				},
			},
			reclaimPoolExist: true,
			reclaimPoolAssignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(10, 11, 12, 13),
			},
			metricsData: map[string]metricutil.MetricData{}, // Empty metrics
			metricError: true,
			wantErr:     true,
		},
		{
			name: "reclaim pool with extra numas",
			regionInfo: types.RegionInfo{
				RegionName:   "share-xxx",
				RegionType:   v1alpha1.QoSRegionTypeShare,
				BindingNumas: machine.NewCPUSet(0),
			},
			controlEssentials: types.ControlEssentials{
				Indicators: types.Indicator{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): {
						Target:  0.6,
						Current: 0.4,
					},
				},
			},
			reclaimPoolExist: true,
			reclaimPoolAssignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(10, 11, 12, 13), // Size 4, Bound
				1: machine.NewCPUSet(14, 15),         // Size 2, Not Bound
			},
			metricsData: map[string]metricutil.MetricData{
				common.GetReclaimRelativeRootCgroupPath("/kubepods/besteffort", 0): {
					Value: 2.0,
					Time:  &now,
				},
			},
			reservedForReclaim: 1.0,
			// quota = Max(4 * 0.2 + 2.0, 1.0) = 2.8. NUMA 1 ignored.
			wantControlKnob: types.ControlKnob{
				v1alpha1.ControlKnobReclaimedCoresCPUQuota: types.ControlKnobItem{
					Value:  2.8,
					Action: types.ControlKnobActionNone,
				},
			},
			wantErr: false,
		},
		{
			name: "fallback error: zero cpu",
			regionInfo: types.RegionInfo{
				RegionName:   "share-xxx",
				RegionType:   v1alpha1.QoSRegionTypeShare,
				BindingNumas: machine.NewCPUSet(0),
			},
			controlEssentials: types.ControlEssentials{
				Indicators: types.Indicator{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): {
						Target:  0.6,
						Current: 0.4,
					},
				},
			},
			reclaimPoolExist: false,
			zeroCPU:          true,
			metricsData: map[string]metricutil.MetricData{
				common.GetReclaimRelativeRootCgroupPath("/kubepods/besteffort", 0): {
					Value: 2.0,
					Time:  &now,
				},
			},
			wantErr: true,
		},
		{
			name: "update skip: not numa binding",
			regionInfo: types.RegionInfo{
				RegionName:   "share-xxx",
				RegionType:   v1alpha1.QoSRegionTypeShare,
				BindingNumas: machine.NewCPUSet(0),
			},
			controlEssentials: types.ControlEssentials{
				Indicators: types.Indicator{
					string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): {
						Target:  0.6,
						Current: 0.4,
					},
				},
			},
			notNUMABinding: true,
			wantErr:        false,
			// wantControlKnob is nil/empty
		},
	}

	// registryMu serializes the parallel subtests below: they all mutate the
	// process-global reclaim consumer registry (generic + per-subtest
	// consumers) via newTestPolicyDynamicQuota. Without this, concurrent
	// registrations of the "generic" name collide.
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dqRegistryMu.Lock()
			defer dqRegistryMu.Unlock()

			checkpointDir, err := os.MkdirTemp("", "checkpoint")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(checkpointDir) }()

			stateFileDir, err := os.MkdirTemp("", "statefile")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(stateFileDir) }()

			checkpointManagerDir, err := os.MkdirTemp("", "checkpointmanager")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(checkpointManagerDir) }()

			metricFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			for path, data := range tt.metricsData {
				metricFetcher.(*metric.FakeMetricsFetcher).SetCgroupMetric(path, consts.MetricCPUUsageCgroup, data)
			}

			policy := newTestPolicyDynamicQuota(t, checkpointDir, stateFileDir,
				checkpointManagerDir, tt.regionInfo, metricFetcher).(*PolicyDynamicQuota)
			assert.NotNil(t, policy)

			// Set machine info for fallback case
			if tt.zeroCPU {
				policy.metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
					CPUTopology: &machine.CPUTopology{
						NUMAToCPUs:   map[int]machine.CPUSet{},
						CPUDetails:   machine.CPUDetails{0: machine.CPUTopoInfo{NUMANodeID: 0}},
						NumNUMANodes: 1,
						NumCPUs:      44,
					},
				}
			} else {
				policy.metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
					CPUTopology: &machine.CPUTopology{
						NUMAToCPUs: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43), // 44 CPUs
						},
						CPUDetails:   machine.CPUDetails{0: machine.CPUTopoInfo{NUMANodeID: 0}},
						NumNUMANodes: 1,
						NumCPUs:      44,
					},
				}
			}
			reclaim.UnregisterConsumer(reclaim.GenericConsumerName)
			require.NoError(t, reclaim.RegisterNamedGenericConsumer(reclaim.GenericConsumerName, policy.conf, policy.metaServer.KatalystMachineInfo))

			// Setup Reclaim Pool
			if tt.reclaimPoolExist {
				reclaimPoolInfo := &types.PoolInfo{
					PoolName:                 commonstate.PoolNameReclaim,
					TopologyAwareAssignments: tt.reclaimPoolAssignments,
				}
				policy.metaReader.(*metacache.MetaCacheImp).SetPoolInfo(commonstate.PoolNameReclaim, reclaimPoolInfo)
			}

			policy.SetEssentials(types.ResourceEssentials{}, tt.controlEssentials)
			policy.ReservedForReclaim = tt.reservedForReclaim

			if tt.notNUMABinding {
				policy.SetBindingNumas(tt.regionInfo.BindingNumas, false)
			}

			err = policy.Update()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				controlKnobUpdated, err := policy.GetControlKnobAdjusted()
				assert.NoError(t, err)
				assert.Equal(t, tt.wantControlKnob, controlKnobUpdated)
			}
		})
	}
}

func TestPolicyDynamicQuota_isCPUQuotaAsControlKnob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		isNUMABinding bool
		indicators    types.Indicator
		want          bool
	}{
		{
			name:          "not numa binding",
			isNUMABinding: false,
			indicators: types.Indicator{
				string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): {},
			},
			want: false,
		},
		{
			name:          "indicator missing",
			isNUMABinding: true,
			indicators:    types.Indicator{},
			want:          false,
		},
		{
			name:          "valid case",
			isNUMABinding: true,
			indicators: types.Indicator{
				string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): {},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Minimal setup
			p := &PolicyDynamicQuota{
				PolicyBase: &PolicyBase{
					isNUMABinding: tt.isNUMABinding,
					ControlEssentials: types.ControlEssentials{
						Indicators: tt.indicators,
					},
				},
			}

			got := p.isCPUQuotaAsControlKnob()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPolicyDynamicQuota_sanityCheck(t *testing.T) {
	t.Parallel()

	dqRegistryMu.Lock()
	defer dqRegistryMu.Unlock()

	checkpointDir, err := os.MkdirTemp("", "checkpoint")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointDir) }()

	stateFileDir, err := os.MkdirTemp("", "statefile")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(stateFileDir) }()

	checkpointManagerDir, err := os.MkdirTemp("", "checkpointmanager")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointManagerDir) }()

	metricFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})

	p := newTestPolicyDynamicQuota(t, checkpointDir, stateFileDir,
		checkpointManagerDir, types.RegionInfo{}, metricFetcher).(*PolicyDynamicQuota)

	// Default EnableReclaim is true from helper
	err = p.sanityCheck()
	assert.NoError(t, err)

	// Disable Reclaim
	p.conf.GetDynamicConfiguration().EnableReclaim = false
	err = p.sanityCheck()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reclaim disabled")
}

// TestPolicyDynamicQuota_updateForCPUQuota_multiPath exercises the multi-consumer
// usage summation. Registering the extra consumer under a fixed name is idempotent
// (same key overwrites, not accumulates) and each subtest owns its own metric
// fetcher and policy instance, so it is safe to run in parallel.
func TestPolicyDynamicQuota_updateForCPUQuota_multiPath(t *testing.T) {
	t.Parallel()

	regionInfo := types.RegionInfo{
		RegionName:   "share-xxx",
		RegionType:   v1alpha1.QoSRegionTypeShare,
		BindingNumas: machine.NewCPUSet(0),
	}
	controlEssentials := types.ControlEssentials{
		Indicators: types.Indicator{
			string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): {
				Target:  0.6,
				Current: 0.4,
			},
		},
	}

	tests := []struct {
		name  string
		usage map[string]float64
		// quota = Max(reclaimNUMACPUSize*(target-current) + sum(usage), ReservedForReclaim)
		//       = Max(4*(0.6-0.4) + sum(usage), 1.0)
		//       = Max(0.8 + sum(usage), 1.0)
		wantQuota float64
	}{
		{
			name: "usage summed across both paths",
			usage: map[string]float64{
				"/kubepods/besteffort-0": 2.0,
				"/kubesandbox-0":         1.5,
			},
			wantQuota: 4.3, // 0.8 + 3.5
		},
		{
			name: "missing path is skipped",
			usage: map[string]float64{
				// "/kubepods/besteffort-0" intentionally unset -> skipped
				"/kubesandbox-0": 1.5,
			},
			wantQuota: 2.3, // 0.8 + 1.5
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dqRegistryMu.Lock()
			defer dqRegistryMu.Unlock()

			now := time.Now()

			checkpointDir, err := os.MkdirTemp("", "checkpoint")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(checkpointDir) })

			stateFileDir, err := os.MkdirTemp("", "statefile")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(stateFileDir) })

			checkpointManagerDir, err := os.MkdirTemp("", "checkpointmanager")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(checkpointManagerDir) })

			metricFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			for path, value := range tc.usage {
				metricFetcher.(*metric.FakeMetricsFetcher).SetCgroupMetric(
					path, consts.MetricCPUUsageCgroup, metricutil.MetricData{Value: value, Time: &now})
			}

			policy := newTestPolicyDynamicQuota(t, checkpointDir, stateFileDir,
				checkpointManagerDir, regionInfo, metricFetcher).(*PolicyDynamicQuota)
			require.NotNil(t, policy)
			machineInfo := &machine.KatalystMachineInfo{
				CPUTopology: &machine.CPUTopology{
					CPUDetails:   machine.CPUDetails{0: machine.CPUTopoInfo{NUMANodeID: 0}},
					NumNUMANodes: 1,
				},
			}
			policy.metaServer.KatalystMachineInfo = machineInfo
			reclaim.UnregisterConsumer(reclaim.GenericConsumerName)
			require.NoError(t, reclaim.RegisterNamedGenericConsumer(reclaim.GenericConsumerName, policy.conf, machineInfo))

			// add a second reclaim consumer; combined with the default generic
			// consumer the registry resolves two disjoint subtrees. Same key
			// across subtests is idempotent under parallel execution.
			reclaim.UnregisterConsumer("dq-multi-sandbox")
			cSandbox := config.NewConfiguration()
			cSandbox.BaseConfiguration.ReclaimRelativeRootCgroupPath = "/kubesandbox"
			cSandbox.BaseConfiguration.GenericReclaimedResourcePercentage = 0
			require.NoError(t, reclaim.RegisterNamedGenericConsumer("dq-multi-sandbox", cSandbox, machineInfo))

			reclaimPoolInfo := &types.PoolInfo{
				PoolName:                 commonstate.PoolNameReclaim,
				TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(10, 11, 12, 13)}, // size 4
			}
			require.NoError(t, policy.metaReader.(*metacache.MetaCacheImp).SetPoolInfo(commonstate.PoolNameReclaim, reclaimPoolInfo))

			policy.SetEssentials(types.ResourceEssentials{}, controlEssentials)
			policy.ReservedForReclaim = 1.0

			require.NoError(t, policy.Update())
			got, err := policy.GetControlKnobAdjusted()
			require.NoError(t, err)
			assert.Equal(t, types.ControlKnob{
				v1alpha1.ControlKnobReclaimedCoresCPUQuota: types.ControlKnobItem{
					Value:  tc.wantQuota,
					Action: types.ControlKnobActionNone,
				},
			}, got)
		})
	}
}
