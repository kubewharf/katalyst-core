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

package commonstate

import (
	"testing"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
)

func TestGetSpecifiedPoolName(t *testing.T) {
	t.Parallel()

	type args struct {
		qosLevel               string
		cpusetEnhancementValue string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "shared_cores with empty cpusetEnhancementValue",
			args: args{
				qosLevel: consts.PodAnnotationQoSLevelSharedCores,
			},
			want: PoolNameShare,
		},
		{
			name: "shared_cores with non-empty cpusetEnhancementValue",
			args: args{
				qosLevel:               consts.PodAnnotationQoSLevelSharedCores,
				cpusetEnhancementValue: "offline",
			},
			want: "offline",
		},
		{
			name: "dedicated_cores with empty cpusetEnhancementValue",
			args: args{
				qosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
			},
			want: PoolNameDedicated,
		},
		{
			name: "reclaimed_cores with empty cpusetEnhancementValue",
			args: args{
				qosLevel: consts.PodAnnotationQoSLevelReclaimedCores,
			},
			want: PoolNameReclaim,
		},
		{
			name: "system_cores with empty cpusetEnhancementValue",
			args: args{
				qosLevel:               consts.PodAnnotationQoSLevelSystemCores,
				cpusetEnhancementValue: "reserve",
			},
			want: "reserve",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := GetSpecifiedPoolName(tt.args.qosLevel, tt.args.cpusetEnhancementValue); got != tt.want {
				t.Errorf("GetSpecifiedPoolName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckNUMABindingSharedCoresAntiAffinity(t *testing.T) {
	t.Parallel()
	testName := "test"
	type args struct {
		am          *AllocationMeta
		annotations map[string]string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "anti affinity with dedicated numa_binding pods",
			args: args{
				am: &AllocationMeta{
					PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
					PodNamespace:   testName,
					PodName:        testName,
					ContainerName:  testName,
					ContainerType:  pluginapi.ContainerType_MAIN.String(),
					ContainerIndex: 0,
					OwnerPoolName:  PoolNameShare,
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
					QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			want: true,
		},
		{
			name: "anti affinity with shared_cores numa_binding pods with same specified pool name",
			args: args{
				am: &AllocationMeta{
					PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
					PodNamespace:   testName,
					PodName:        testName,
					ContainerName:  testName,
					ContainerType:  pluginapi.ContainerType_MAIN.String(),
					ContainerIndex: 0,
					OwnerPoolName:  PoolNameShare,
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationCPUEnhancementCPUSet:         "batch",
					},
					QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
				},
				annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementCPUSet:         "bmq",
				},
			},
			want: true,
		},
		{
			name: "not anti affinity with shared_cores numa_binding pods with same specified pool name",
			args: args{
				am: &AllocationMeta{
					PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
					PodNamespace:   testName,
					PodName:        testName,
					ContainerName:  testName,
					ContainerType:  pluginapi.ContainerType_MAIN.String(),
					ContainerIndex: 0,
					OwnerPoolName:  PoolNameShare,
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationCPUEnhancementCPUSet:         "batch",
					},
					QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
				},
				annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementCPUSet:         "batch",
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CheckNUMABindingSharedCoresAntiAffinity(tt.args.am, tt.args.annotations); got != tt.want {
				t.Errorf("CheckNUMABindingSharedCoresAntiAffinity() = %v, want %v", got, tt.want)
			}
		})
	}
}
