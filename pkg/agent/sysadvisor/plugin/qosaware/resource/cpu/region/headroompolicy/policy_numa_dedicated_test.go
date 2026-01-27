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

package headroompolicy

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8types "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	metaCacheNumaExclusive  *metacache.MetaCacheImp
	metaServerNumaExclusive *metaserver.MetaServer
)

func generateNumaExclusiveTestConfiguration(t *testing.T, checkpointDir, stateFileDir, checkpointManagerDir string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir
	conf.CheckpointManagerDir = checkpointManagerDir

	conf.GetDynamicConfiguration().EnableReclaim = true

	return conf
}

func newTestPolicyNumaExclusive(t *testing.T, checkpointDir string, stateFileDir string, checkpointManagerDir string, regionInfo types.RegionInfo, podSet types.PodSet) HeadroomPolicy {
	conf := generateNumaExclusiveTestConfiguration(t, checkpointDir, stateFileDir, checkpointManagerDir)

	metaCacheTmp, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	metaCacheNumaExclusive = metaCacheTmp
	require.NoError(t, err)
	require.NotNil(t, metaCacheNumaExclusive)

	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	require.NoError(t, err)

	metaServerTmp, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	metaServerNumaExclusive = metaServerTmp
	assert.NoError(t, err)
	assert.NotNil(t, metaServerNumaExclusive)

	p := NewPolicyNUMADedicated(regionInfo.RegionName, regionInfo.RegionType, regionInfo.OwnerPoolName, conf, nil, metaCacheNumaExclusive, metaServerNumaExclusive, metrics.DummyMetrics{})
	metaCacheNumaExclusive.SetRegionInfo(regionInfo.RegionName, &regionInfo)

	p.SetCPUAffinityNUMAs(regionInfo.BindingNumas)
	p.SetPodSet(podSet)

	return p
}

func constructPodFetcherNumaExclusive(names []string) pod.PodFetcher {
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

func TestPolicyNumaExclusive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		regionInfo         types.RegionInfo
		podSet             types.PodSet
		resourceEssentials types.ResourceEssentials
		controlEssentials  types.ControlEssentials
		wantResult         float64
	}{
		{
			name: "share-no-reserved",
			podSet: types.PodSet{
				"pod0": sets.String{
					"container0": struct{}{},
				},
			},
			regionInfo: types.RegionInfo{
				RegionName:   "share-xxx",
				RegionType:   configapi.QoSRegionTypeShare,
				BindingNumas: machine.NewCPUSet(0),
			},
			resourceEssentials: types.ResourceEssentials{
				EnableReclaim:       true,
				ResourceUpperBound:  90,
				ResourceLowerBound:  4,
				ReservedForAllocate: 0,
				ReservedForReclaim:  0,
			},
			wantResult: 90,
		},
		{
			name: "share-reserved-for-allocate",
			podSet: types.PodSet{
				"pod0": sets.String{
					"container0": struct{}{},
				},
			},
			regionInfo: types.RegionInfo{
				RegionName:   "share-xxx",
				RegionType:   configapi.QoSRegionTypeShare,
				BindingNumas: machine.NewCPUSet(0),
			},
			resourceEssentials: types.ResourceEssentials{
				EnableReclaim:       true,
				ResourceUpperBound:  90,
				ResourceLowerBound:  4,
				ReservedForAllocate: 10,
				ReservedForReclaim:  0,
			},
			wantResult: 80,
		},
		{
			name: "share-reserved-for-reclaim",
			podSet: types.PodSet{
				"pod0": sets.String{
					"container0": struct{}{},
				},
			},
			regionInfo: types.RegionInfo{
				RegionName:   "share-xxx",
				RegionType:   configapi.QoSRegionTypeShare,
				BindingNumas: machine.NewCPUSet(0),
			},
			resourceEssentials: types.ResourceEssentials{
				EnableReclaim:       true,
				ResourceUpperBound:  90,
				ResourceLowerBound:  4,
				ReservedForAllocate: 0,
				ReservedForReclaim:  10,
			},
			wantResult: 100,
		},
		{
			name: "dedicated-numa-exclusive-no-reserved",
			podSet: types.PodSet{
				"pod0": sets.String{
					"container0": struct{}{},
				},
			},
			regionInfo: types.RegionInfo{
				RegionName:   "dedicated-numa-exclusive-xxx",
				RegionType:   configapi.QoSRegionTypeDedicated,
				BindingNumas: machine.NewCPUSet(0),
			},
			resourceEssentials: types.ResourceEssentials{
				EnableReclaim:       true,
				ResourceUpperBound:  90,
				ResourceLowerBound:  4,
				ReservedForAllocate: 0,
				ReservedForReclaim:  0,
			},
			wantResult: 90,
		},
		{
			name: "dedicated-numa-exclusive-reserved-for-allocate",
			podSet: types.PodSet{
				"pod0": sets.String{
					"container0": struct{}{},
				},
			},
			regionInfo: types.RegionInfo{
				RegionName:   "dedicated-numa-exclusive-xxx",
				RegionType:   configapi.QoSRegionTypeDedicated,
				BindingNumas: machine.NewCPUSet(0),
			},
			resourceEssentials: types.ResourceEssentials{
				EnableReclaim:       true,
				ResourceUpperBound:  90,
				ResourceLowerBound:  4,
				ReservedForAllocate: 10,
				ReservedForReclaim:  0,
			},
			wantResult: 80,
		},
		{
			name: "dedicated-numa-exclusive-reserved-for-reclaim",
			podSet: types.PodSet{
				"pod0": sets.String{
					"container0": struct{}{},
				},
			},
			regionInfo: types.RegionInfo{
				RegionName:   "dedicated-numa-exclusive-xxx",
				RegionType:   configapi.QoSRegionTypeDedicated,
				BindingNumas: machine.NewCPUSet(0),
			},
			resourceEssentials: types.ResourceEssentials{
				EnableReclaim:       true,
				ResourceUpperBound:  90,
				ResourceLowerBound:  4,
				ReservedForAllocate: 0,
				ReservedForReclaim:  10,
			},
			wantResult: 100,
		},
	}

	checkpointDir, err := os.MkdirTemp("", "checkpoint")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointDir) }()

	stateFileDir, err := os.MkdirTemp("", "statefile")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(stateFileDir) }()

	checkpointManagerDir, err := os.MkdirTemp("", "checkpointmanager")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(checkpointManagerDir) }()

	for _, tt := range tests {
		policy := newTestPolicyNumaExclusive(t, checkpointDir, stateFileDir, checkpointManagerDir, tt.regionInfo, tt.podSet).(*PolicyNUMADedicated)
		assert.NotNil(t, policy)

		podNames := []string{}
		for podName, containerSet := range tt.podSet {
			podNames = append(podNames, podName)
			for containerName := range containerSet {
				err = metaCacheNumaExclusive.AddContainer(podName, containerName, &types.ContainerInfo{})
				assert.Nil(t, err)
			}
		}
		policy.metaServer.MetaAgent.SetPodFetcher(constructPodFetcherNumaExclusive(podNames))

		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			policy.SetEssentials(tt.resourceEssentials)
			err := policy.Update()
			assert.NoError(t, err)

			headroom, err := policy.GetHeadroom()

			assert.NoError(t, err)
			assert.Equal(t, tt.wantResult, headroom)
		})
	}
}
