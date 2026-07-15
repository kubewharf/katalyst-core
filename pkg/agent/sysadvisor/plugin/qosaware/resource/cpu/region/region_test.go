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

package region

import (
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/reclaim"
)

func TestGetRegionNameFromMetaCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		container  *types.ContainerInfo
		numaID     int
		region     *types.RegionInfo
		regionName string
	}{
		{
			name: "shared-region",
			container: &types.ContainerInfo{
				QoSLevel:    consts.PodAnnotationQoSLevelSharedCores,
				RegionNames: sets.NewString("share-t"),
			},
			region: &types.RegionInfo{
				RegionName: "share-t",
				RegionType: configapi.QoSRegionTypeShare,
			},
			regionName: "share-t",
		},
		{
			name: "dedicated-region",
			container: &types.ContainerInfo{
				QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
				Annotations: map[string]string{
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
				RegionNames: sets.NewString("dedicated-t"),
			},
			region: &types.RegionInfo{
				RegionName:   "dedicated-t",
				RegionType:   configapi.QoSRegionTypeDedicated,
				BindingNumas: machine.NewCPUSet(1),
			},
			numaID:     1,
			regionName: "dedicated-t",
		},
		{
			name: "dedicated-region-numa-miss",
			container: &types.ContainerInfo{
				QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
				Annotations: map[string]string{
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
				RegionNames: sets.NewString("dedicated-t"),
			},
			region: &types.RegionInfo{
				RegionName:   "dedicated-t",
				RegionType:   configapi.QoSRegionTypeDedicated,
				BindingNumas: machine.NewCPUSet(1),
			},
			numaID:     2,
			regionName: "",
		},
		{
			name: "isolation-region",
			container: &types.ContainerInfo{
				QoSLevel:    consts.PodAnnotationQoSLevelSharedCores,
				RegionNames: sets.NewString("isolation-t"),
				Isolated:    true,
			},
			region: &types.RegionInfo{
				RegionName: "isolation-t",
				RegionType: configapi.QoSRegionTypeIsolation,
			},
			regionName: "isolation-t",
		},
		{
			name: "isolation-region-empty",
			container: &types.ContainerInfo{
				QoSLevel:    consts.PodAnnotationQoSLevelSharedCores,
				RegionNames: sets.NewString("isolation-t"),
				Isolated:    true,
			},
			region: &types.RegionInfo{
				RegionName: "isolation-t",
				RegionType: configapi.QoSRegionTypeShare,
			},
			regionName: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf, err := options.NewOptions().Config()
			assert.NoError(t, err)
			conf.GenericSysAdvisorConfiguration.StateFileDirectory = os.TempDir()
			cache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, nil)
			assert.NoError(t, err)
			err = cache.SetRegionInfo(tt.region.RegionName, tt.region)
			assert.NoError(t, err)
			err = cache.SetContainerInfo(tt.container.PodUID, tt.container.ContainerName, tt.container)
			assert.NoError(t, err)

			assert.Equal(t, tt.regionName, getRegionNameFromMetaCache(tt.container, tt.numaID, cache))
		})
	}
}

func TestIsNumaBinding(t *testing.T) {
	t.Parallel()

	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	stateFileDir := "stateFileDir"
	checkpointDir := "checkpointDir"

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir
	conf.RestrictRefPolicy = nil

	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	require.NoError(t, err)

	metaServer, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	require.NoError(t, err)
	defer func() {
		os.RemoveAll(stateFileDir)
		os.RemoveAll(checkpointDir)
	}()

	metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	require.NoError(t, err)
	ci := types.ContainerInfo{
		QoSLevel:    consts.PodAnnotationQoSLevelSharedCores,
		RegionNames: sets.NewString("share-NUMA1"),
	}
	share := NewQoSRegionShare(&ci, conf, nil, 1, metaCache, metaServer, metrics.DummyMetrics{})
	require.True(t, share.IsNumaBinding(), "test IsNumaBinding failed")

	ci2 := types.ContainerInfo{
		QoSLevel:    consts.PodAnnotationQoSLevelSharedCores,
		RegionNames: sets.NewString("share"),
	}
	share2 := NewQoSRegionShare(&ci2, conf, nil, commonstate.FakedNUMAID, metaCache, metaServer, metrics.DummyMetrics{})
	require.False(t, share2.IsNumaBinding(), "test IsNumaBinding failed")

	ci3 := types.ContainerInfo{
		QoSLevel:    consts.PodAnnotationQoSLevelSharedCores,
		RegionNames: sets.NewString("isolation-NUMA1-1"),
		Isolated:    true,
	}
	isolation1 := NewQoSRegionIsolation(&ci3, "isolation-1", conf, nil, 1, metaCache, metaServer, metrics.DummyMetrics{})
	require.True(t, isolation1.IsNumaBinding(), "test IsNumaBinding failed")

	ci4 := types.ContainerInfo{
		QoSLevel:    consts.PodAnnotationQoSLevelSharedCores,
		RegionNames: sets.NewString("isolation-1"),
		Isolated:    true,
	}
	isolation2 := NewQoSRegionIsolation(&ci4, "isolation-1", conf, nil, commonstate.FakedNUMAID, metaCache, metaServer, metrics.DummyMetrics{})
	require.False(t, isolation2.IsNumaBinding(), "test IsNumaBinding failed")
}

func TestRestrictProvisionControlKnob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		originControlKnob map[types.CPUProvisionPolicyName]types.ControlKnob
		wantControlKnob   map[types.CPUProvisionPolicyName]types.ControlKnob
	}{
		{
			name:              "no restriction",
			originControlKnob: map[types.CPUProvisionPolicyName]types.ControlKnob{"p1": {"c1": types.ControlKnobItem{Value: 8}}, "p2": {"c1": types.ControlKnobItem{Value: 10}}},
			wantControlKnob:   map[types.CPUProvisionPolicyName]types.ControlKnob{"p1": {"c1": types.ControlKnobItem{Value: 8}}, "p2": {"c1": types.ControlKnobItem{Value: 10}}},
		},
		{
			name:              "restricted by p2",
			originControlKnob: map[types.CPUProvisionPolicyName]types.ControlKnob{"p1": {configapi.ControlKnobReclaimedCoresCPUQuota: types.ControlKnobItem{Value: 16}}, "p2": {configapi.ControlKnobReclaimedCoresCPUQuota: types.ControlKnobItem{Value: 10}}},
			wantControlKnob:   map[types.CPUProvisionPolicyName]types.ControlKnob{"p1": {configapi.ControlKnobReclaimedCoresCPUQuota: types.ControlKnobItem{Value: 10}}, "p2": {configapi.ControlKnobReclaimedCoresCPUQuota: types.ControlKnobItem{Value: 10}}},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf, err := options.NewOptions().Config()
			require.NoError(t, err)
			require.NotNil(t, conf)

			stateFileDir := "stateFileDir" + uuid.New().String()
			checkpointDir := "checkpointDir" + uuid.New().String()

			conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
			conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir
			conf.RestrictRefPolicy = map[types.CPUProvisionPolicyName]types.CPUProvisionPolicyName{"p1": "p2"}

			genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
			require.NoError(t, err)

			metaServer, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
			require.NoError(t, err)
			defer func() {
				os.RemoveAll(stateFileDir)
				os.RemoveAll(checkpointDir)
			}()

			metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
			require.NoError(t, err)
			ci := types.ContainerInfo{
				QoSLevel:    consts.PodAnnotationQoSLevelSharedCores,
				RegionNames: sets.NewString("share"),
			}
			share := NewQoSRegionShare(&ci, conf, nil, commonstate.FakedNUMAID, metaCache, metaServer, metrics.DummyMetrics{})
			restrictedControlKnobs := share.(*QoSRegionShare).restrictProvisionControlKnob(tt.originControlKnob)
			assert.Equal(t, tt.wantControlKnob, restrictedControlKnobs)
		})
	}
}

func TestGetEffectiveReclaimResource(t *testing.T) {
	t.Parallel()

	reclaim.UnregisterConsumer("region-test-a")
	reclaim.UnregisterConsumer("region-test-b")
	require.NoError(t, reclaim.RegisterNamedGenericConsumer("region-test-a", "/kubepods/besteffort", 0))
	require.NoError(t, reclaim.RegisterNamedGenericConsumer("region-test-b", "/kubesandbox", 0))

	period := uint64(100000)
	stats := func(cores float64) *common.CPUStats {
		return &common.CPUStats{CpuPeriod: period, CpuQuota: int64(cores * float64(period))}
	}
	unlimited := &common.CPUStats{CpuPeriod: period, CpuQuota: common.CPUQuotaUnlimit}
	maxInt := &common.CPUStats{CpuPeriod: period, CpuQuota: math.MaxInt}

	tests := []struct {
		name          string
		isNumaBinding bool
		bindingNumas  machine.CPUSet
		gateEnabled   bool
		// cpuStats maps a reclaim cgroup path to its stats; a missing key makes
		// the mocked GetCPUWithRelativePath return an error for that path.
		cpuStats     map[string]*common.CPUStats
		reclaimPool  *types.PoolInfo
		expectQuota  float64
		expectCpuset int
	}{
		{
			name:         "non-binding sums finite quotas across consumers",
			gateEnabled:  true,
			cpuStats:     map[string]*common.CPUStats{"/kubepods/besteffort": stats(1.0), "/kubesandbox": stats(2.0)},
			expectQuota:  3.0,
			expectCpuset: 0,
		},
		{
			name:         "non-binding any unlimited path makes whole scope unlimited",
			gateEnabled:  true,
			cpuStats:     map[string]*common.CPUStats{"/kubepods/besteffort": stats(1.0), "/kubesandbox": unlimited},
			expectQuota:  common.CPUQuotaUnlimit,
			expectCpuset: 0,
		},
		{
			name:         "non-binding math.MaxInt is treated as unlimited",
			gateEnabled:  true,
			cpuStats:     map[string]*common.CPUStats{"/kubepods/besteffort": maxInt, "/kubesandbox": stats(2.0)},
			expectQuota:  common.CPUQuotaUnlimit,
			expectCpuset: 0,
		},
		{
			name:         "gate disabled yields unlimited regardless of cgroup values",
			gateEnabled:  false,
			cpuStats:     map[string]*common.CPUStats{"/kubepods/besteffort": stats(1.0), "/kubesandbox": stats(2.0)},
			expectQuota:  common.CPUQuotaUnlimit,
			expectCpuset: 0,
		},
		{
			name:         "all paths missing yields unlimited",
			gateEnabled:  true,
			cpuStats:     map[string]*common.CPUStats{},
			expectQuota:  common.CPUQuotaUnlimit,
			expectCpuset: 0,
		},
		{
			name:         "missing path is skipped and the remainder is summed",
			gateEnabled:  true,
			cpuStats:     map[string]*common.CPUStats{"/kubesandbox": stats(2.0)},
			expectQuota:  2.0,
			expectCpuset: 0,
		},
		{
			name:          "numa-binding resolves per-numa paths, sums quota and reads cpuset",
			isNumaBinding: true,
			bindingNumas:  machine.NewCPUSet(1),
			gateEnabled:   true,
			cpuStats:      map[string]*common.CPUStats{"/kubepods/besteffort-1": stats(1.0), "/kubesandbox-1": stats(1.5)},
			reclaimPool: &types.PoolInfo{
				PoolName: commonstate.PoolNameReclaim,
				TopologyAwareAssignments: types.TopologyAwareAssignment{
					1: machine.NewCPUSet(10, 11, 12),
				},
			},
			expectQuota:  2.5,
			expectCpuset: 3,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metaCache := metacache.NewDummyMetaCacheImp()
			if tt.reclaimPool != nil {
				require.NoError(t, metaCache.SetPoolInfo(commonstate.PoolNameReclaim, tt.reclaimPool))
			}

			r := &QoSRegionBase{
				isNumaBinding: tt.isNumaBinding,
				bindingNumas:  tt.bindingNumas,
				metaReader:    metaCache,
				isQuotaCtrlKnobEnabled: func(metacache.MetaReader) (bool, error) {
					return tt.gateEnabled, nil
				},
				getCPUWithRelativePath: func(path string) (*common.CPUStats, error) {
					if s, ok := tt.cpuStats[path]; ok {
						return s, nil
					}
					return nil, fmt.Errorf("cgroup not found: %s", path)
				},
			}

			quota, cpusetSize, err := r.getEffectiveReclaimResource()
			require.NoError(t, err)
			assert.InDelta(t, tt.expectQuota, quota, 1e-9)
			assert.Equal(t, tt.expectCpuset, cpusetSize)
		})
	}
}
