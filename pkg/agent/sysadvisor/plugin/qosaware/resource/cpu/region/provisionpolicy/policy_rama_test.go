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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8types "k8s.io/apimachinery/pkg/types"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	provisionconf "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu/provision"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	metricutil "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func generateRamaTestConfiguration(t *testing.T, checkpointDir, stateFileDir, checkpointManagerDir string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir
	conf.CheckpointManagerDir = checkpointManagerDir

	conf.RegionIndicatorTargetConfiguration = map[types.QoSRegionType][]provisionconf.IndicatorTargetConfiguration{
		types.QoSRegionTypeShare: {
			{
				Name: consts.MetricCPUSchedwait,
			},
		},
		types.QoSRegionTypeDedicatedNumaExclusive: {
			{
				Name: consts.MetricCPUCPIContainer,
			},
			{
				Name: consts.MetricMemBandwidthNuma,
			},
		},
	}

	conf.PolicyRama = &provisionconf.PolicyRamaConfiguration{
		PIDParameters: map[string]types.FirstOrderPIDParams{
			consts.MetricCPUSchedwait: {
				Kpp:                  10.0,
				Kpn:                  1.0,
				Kdp:                  0.0,
				Kdn:                  0.0,
				AdjustmentUpperBound: types.MaxRampUpStep,
				AdjustmentLowerBound: -types.MaxRampDownStep,
				DeadbandLowerPct:     0.8,
				DeadbandUpperPct:     0.05,
			},
			consts.MetricCPUCPIContainer: {
				Kpp:                  10.0,
				Kpn:                  1.0,
				Kdp:                  0.0,
				Kdn:                  0.0,
				AdjustmentUpperBound: types.MaxRampUpStep,
				AdjustmentLowerBound: -types.MaxRampDownStep,
				DeadbandLowerPct:     0.95,
				DeadbandUpperPct:     0.02,
			},
			consts.MetricMemBandwidthNuma: {
				Kpp:                  10.0,
				Kpn:                  1.0,
				Kdp:                  0.0,
				Kdn:                  0.0,
				AdjustmentUpperBound: types.MaxRampUpStep,
				AdjustmentLowerBound: -types.MaxRampDownStep,
				DeadbandLowerPct:     0.95,
				DeadbandUpperPct:     0.02,
			},
		},
	}

	conf.GetDynamicConfiguration().EnableReclaim = true

	return conf
}

func newTestPolicyRama(t *testing.T, checkpointDir string, stateFileDir string,
	checkpointManagerDir string, regionInfo types.RegionInfo, metricFetcher metrictypes.MetricsFetcher, podSet types.PodSet) ProvisionPolicy {
	conf := generateRamaTestConfiguration(t, checkpointDir, stateFileDir, checkpointManagerDir)

	metaCacheTmp, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricFetcher)
	require.NoError(t, err)
	require.NotNil(t, metaCacheTmp)

	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	require.NoError(t, err)

	metaServerTmp, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	assert.NoError(t, err)
	require.NotNil(t, metaServerTmp)

	p := NewPolicyRama(regionInfo.RegionName, regionInfo.RegionType, regionInfo.OwnerPoolName,
		conf, nil, metaCacheTmp, metaServerTmp, metrics.DummyMetrics{})
	err = metaCacheTmp.SetRegionInfo(regionInfo.RegionName, &regionInfo)
	assert.NoError(t, err)

	p.SetBindingNumas(regionInfo.BindingNumas)
	p.SetPodSet(podSet)

	return p
}

func constructPodFetcherRama(names []string) pod.PodFetcher {
	var pods []*v1.Pod
	for _, name := range names {
		pods = append(pods, &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				UID:  k8types.UID(name),
			},
		})
	}

	return &pod.PodFetcherStub{PodList: pods}
}

func TestPolicyRama(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		regionInfo          types.RegionInfo
		containerInfo       map[string]map[string]types.ContainerInfo
		containerMetricData map[string]map[string]map[string]metricutil.MetricData
		resourceEssentials  types.ResourceEssentials
		controlEssentials   types.ControlEssentials
		wantResult          types.ControlKnob
	}{
		{
			name: "share_ramp_up",
			containerInfo: map[string]map[string]types.ContainerInfo{
				"pod0": {
					"container0": types.ContainerInfo{
						PodUID:        "pod0",
						PodName:       "pod0",
						ContainerName: "container0",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
						CPURequest:    4.0,
						RampUp:        true,
					},
				},
			},
			containerMetricData: map[string]map[string]map[string]metricutil.MetricData{
				"pod0": {
					"container0": {
						consts.MetricCPUUsageContainer: metricutil.MetricData{
							Value: 2,
						},
					},
				},
			},
			regionInfo: types.RegionInfo{
				RegionName:   "share-xxx",
				RegionType:   types.QoSRegionTypeShare,
				BindingNumas: machine.NewCPUSet(0),
			},
			resourceEssentials: types.ResourceEssentials{
				EnableReclaim:       true,
				ResourceUpperBound:  90,
				ResourceLowerBound:  4,
				ReservedForAllocate: 0,
			},
			controlEssentials: types.ControlEssentials{
				ControlKnobs: types.ControlKnob{
					types.ControlKnobNonReclaimedCPUSize: {
						Value:  40,
						Action: types.ControlKnobActionNone,
					},
				},
				Indicators: types.Indicator{
					consts.MetricCPUSchedwait: {
						Current: 800,
						Target:  400,
					},
				},
				ReclaimOverlap: false,
			},
			wantResult: types.ControlKnob{
				types.ControlKnobNonReclaimedCPUSize: {
					Value:  46.93147180559946,
					Action: types.ControlKnobActionNone,
				},
			},
		},
		{
			name: "share_ramp_down",
			containerInfo: map[string]map[string]types.ContainerInfo{
				"pod0": {
					"container0": types.ContainerInfo{
						PodUID:        "pod0",
						PodName:       "pod0",
						ContainerName: "container0",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
						CPURequest:    4.0,
						RampUp:        false,
					},
				},
			},
			containerMetricData: map[string]map[string]map[string]metricutil.MetricData{
				"pod0": {
					"container0": {
						consts.MetricCPUUsageContainer: metricutil.MetricData{
							Value: 2,
						},
					},
				},
			},
			regionInfo: types.RegionInfo{
				RegionName: "share-xxx",
				RegionType: types.QoSRegionTypeShare,
			},
			resourceEssentials: types.ResourceEssentials{
				EnableReclaim:       true,
				ResourceUpperBound:  90,
				ResourceLowerBound:  4,
				ReservedForAllocate: 0,
			},
			controlEssentials: types.ControlEssentials{
				ControlKnobs: types.ControlKnob{
					types.ControlKnobNonReclaimedCPUSize: {
						Value:  40,
						Action: types.ControlKnobActionNone,
					},
				},
				Indicators: types.Indicator{
					consts.MetricCPUSchedwait: {
						Current: 4,
						Target:  400,
					},
				},
				ReclaimOverlap: false,
			},
			wantResult: types.ControlKnob{
				types.ControlKnobNonReclaimedCPUSize: {
					Value:  38,
					Action: types.ControlKnobActionNone,
				},
			},
		},
		{
			name: "share_deadband",
			containerInfo: map[string]map[string]types.ContainerInfo{
				"pod0": {
					"container0": types.ContainerInfo{
						PodUID:        "pod0",
						PodName:       "pod0",
						ContainerName: "container0",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
						CPURequest:    4.0,
						RampUp:        false,
					},
				},
			},
			containerMetricData: map[string]map[string]map[string]metricutil.MetricData{
				"pod0": {
					"container0": {
						consts.MetricCPUUsageContainer: metricutil.MetricData{
							Value: 2,
						},
					},
				},
			},
			regionInfo: types.RegionInfo{
				RegionName: "share-xxx",
				RegionType: types.QoSRegionTypeShare,
			},
			resourceEssentials: types.ResourceEssentials{
				EnableReclaim:       true,
				ResourceUpperBound:  90,
				ResourceLowerBound:  4,
				ReservedForAllocate: 0,
			},
			controlEssentials: types.ControlEssentials{
				ControlKnobs: types.ControlKnob{
					types.ControlKnobNonReclaimedCPUSize: {
						Value:  40,
						Action: types.ControlKnobActionNone,
					},
				},
				Indicators: types.Indicator{
					consts.MetricCPUSchedwait: {
						Current: 401,
						Target:  400,
					},
				},
				ReclaimOverlap: false,
			},
			wantResult: types.ControlKnob{
				types.ControlKnobNonReclaimedCPUSize: {
					Value:  40,
					Action: types.ControlKnobActionNone,
				},
			},
		},
		{
			name: "dedicated_numa_exclusive",
			containerInfo: map[string]map[string]types.ContainerInfo{
				"pod0": {
					"container0": types.ContainerInfo{
						PodUID:        "pod0",
						PodName:       "pod0",
						ContainerName: "container0",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
						CPURequest:    4.0,
						RampUp:        false,
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0, 1, 2, 3),
						},
					},
				},
			},
			regionInfo: types.RegionInfo{
				RegionName:   "dedicated-numa-exclusive-xxx",
				RegionType:   types.QoSRegionTypeDedicatedNumaExclusive,
				BindingNumas: machine.NewCPUSet(0),
			},
			resourceEssentials: types.ResourceEssentials{
				EnableReclaim:       true,
				ResourceUpperBound:  90,
				ResourceLowerBound:  4,
				ReservedForAllocate: 0,
			},
			controlEssentials: types.ControlEssentials{
				ControlKnobs: types.ControlKnob{
					types.ControlKnobNonReclaimedCPUSize: {
						Value:  40,
						Action: types.ControlKnobActionNone,
					},
				},
				Indicators: types.Indicator{
					consts.MetricCPUCPIContainer: {
						Current: 2.0,
						Target:  1.0,
					},
					consts.MetricMemBandwidthNuma: {
						Current: 4,
						Target:  40,
					},
				},
				ReclaimOverlap: true,
			},
			wantResult: types.ControlKnob{
				types.ControlKnobNonReclaimedCPUSize: {
					Value:  90,
					Action: types.ControlKnobActionNone,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkpointDir, err := os.MkdirTemp("", "checkpoint")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(checkpointDir) }()

			stateFileDir, err := os.MkdirTemp("", "statefile")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(stateFileDir) }()

			checkpointManagerDir, err := os.MkdirTemp("", "checkpointmanager")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(checkpointManagerDir) }()

			podSet := make(types.PodSet)
			for podUID, info := range tt.containerInfo {
				for containerName := range info {
					podSet.Insert(podUID, containerName)
				}
			}

			metricFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			for podUID, m := range tt.containerMetricData {
				for containerName, c := range m {
					for name, d := range c {
						metricFetcher.(*metric.FakeMetricsFetcher).SetContainerMetric(podUID, containerName, name, d)
					}
				}
			}

			policy := newTestPolicyRama(t, checkpointDir, stateFileDir,
				checkpointManagerDir, tt.regionInfo, metricFetcher, podSet).(*PolicyRama)
			assert.NotNil(t, policy)

			podNames := []string{}
			for podName, containerSet := range tt.containerInfo {
				podNames = append(podNames, podName)
				for containerName, info := range containerSet {
					err = policy.metaReader.(*metacache.MetaCacheImp).AddContainer(podName, containerName, &info)
					assert.Nil(t, err)
				}
			}
			policy.metaServer.MetaAgent.SetPodFetcher(constructPodFetcherRama(podNames))

			policy.SetEssentials(tt.resourceEssentials, tt.controlEssentials)
			_ = policy.Update()
			controlKnobUpdated, err := policy.GetControlKnobAdjusted()

			assert.NoError(t, err)
			assert.Equal(t, tt.wantResult, controlKnobUpdated)
		})
	}
}
