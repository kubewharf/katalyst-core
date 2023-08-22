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
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
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
				RegionType: types.QoSRegionTypeShare,
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
				RegionType:   types.QoSRegionTypeDedicatedNumaExclusive,
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
				RegionType:   types.QoSRegionTypeDedicatedNumaExclusive,
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
				RegionType: types.QoSRegionTypeIsolation,
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
				RegionType: types.QoSRegionTypeShare,
			},
			regionName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
