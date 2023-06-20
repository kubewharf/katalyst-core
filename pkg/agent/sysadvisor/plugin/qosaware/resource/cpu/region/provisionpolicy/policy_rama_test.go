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
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	provisionconf "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu/provision"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func generateTestConfiguration(t *testing.T, checkpointDir, stateFileDir string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir

	conf.PolicyRama = &provisionconf.PolicyRamaConfiguration{
		IndicatorMetrics: map[types.QoSRegionType][]string{
			types.QoSRegionTypeShare: {
				consts.MetricCPUSchedwait,
			},
			types.QoSRegionTypeDedicatedNumaExclusive: {
				consts.MetricCPUCPIContainer,
				consts.MetricMemBandwidthNuma,
			},
		},
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

	return conf
}

func newTestPolicyRama(t *testing.T, checkpointDir string, stateFileDir string, regionInfo types.RegionInfo) ProvisionPolicy {
	conf := generateTestConfiguration(t, checkpointDir, stateFileDir)

	metaCache, err := metacache.NewMetaCacheImp(conf, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	require.NoError(t, err)

	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	require.NoError(t, err)

	metaServer, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	require.NoError(t, err)

	p := NewPolicyRama(regionInfo.RegionName, regionInfo.RegionType, regionInfo.OwnerPoolName, conf, nil, metaCache, metaServer, metrics.DummyMetrics{})
	metaCache.SetRegionInfo(regionInfo.RegionName, &regionInfo)

	return p
}

func TestPolicyRama(t *testing.T) {
	tests := []struct {
		name               string
		regionInfo         types.RegionInfo
		resourceEssentials types.ResourceEssentials
		controlEssentials  types.ControlEssentials
		wantResult         types.ControlKnob
	}{
		{
			name: "share_ramp_up",
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
						Current: 800,
						Target:  400,
					},
				},
			},
			wantResult: types.ControlKnob{
				types.ControlKnobNonReclaimedCPUSize: {
					Value:  48,
					Action: types.ControlKnobActionNone,
				},
			},
		},
		{
			name: "share_ramp_down",
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
			regionInfo: types.RegionInfo{
				RegionName: "dedicated-numa-exclusive-xxx",
				RegionType: types.QoSRegionTypeDedicatedNumaExclusive,
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
			},
			wantResult: types.ControlKnob{
				types.ControlKnobNonReclaimedCPUSize: {
					Value:  48,
					Action: types.ControlKnobActionNone,
				},
			},
		},
	}

	checkpointDir, err := ioutil.TempDir("", "checkpoint")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointDir) }()

	stateFileDir, err := ioutil.TempDir("", "statefile")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(stateFileDir) }()

	for _, tt := range tests {
		policy := newTestPolicyRama(t, checkpointDir, stateFileDir, tt.regionInfo)

		t.Run(tt.name, func(t *testing.T) {
			policy.SetEssentials(tt.resourceEssentials, tt.controlEssentials)
			policy.Update()
			controlKnobUpdated, err := policy.GetControlKnobAdjusted()

			assert.NoError(t, err)
			assert.Equal(t, tt.wantResult, controlKnobUpdated)
		})
	}
}
