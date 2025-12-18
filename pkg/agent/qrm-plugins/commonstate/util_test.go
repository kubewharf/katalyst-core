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

	"github.com/stretchr/testify/assert"

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
			name: "anti affinity with dedicated numa_binding and shared_cores pod (numa_share=false)",
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
						consts.PodAnnotationCPUEnhancementNUMAShare:      consts.PodAnnotationCPUEnhancementNUMAShareDisable,
					},
					QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementNUMAShare:      consts.PodAnnotationCPUEnhancementNUMAShareDisable,
					consts.PodAnnotationCPUEnhancementCPUSet:         "bmq",
				},
			},
			want: true,
		},
		{
			name: "not anti affinity with dedicated numa_binding and shared_cores pod",
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
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementCPUSet:         "bmq",
				},
			},
			want: false,
		},
		{
			name: "not anti affinity with dedicated numa_binding pods",
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
						consts.PodAnnotationCPUEnhancementNUMAShare:      consts.PodAnnotationCPUEnhancementNUMAShareDisable,
					},
					QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			want: false,
		},
		{
			name: "anti affinity with shared_cores numa_binding pods with different specified pool name (numa_share=false)",
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
					consts.PodAnnotationCPUEnhancementNUMAShare:      consts.PodAnnotationCPUEnhancementNUMAShareDisable,
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
		{
			name: "not anti affinity with shared_cores numa_binding pods with different specified pool name",
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
			want: false,
		},
		{
			name: "not anti affinity with shared_cores numa_binding pods with same specified pool name (numa_share=false)",
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
						consts.PodAnnotationCPUEnhancementNUMAShare:      consts.PodAnnotationCPUEnhancementNUMAShareDisable,
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
			if got := CheckNUMABindingAntiAffinity(tt.args.am, tt.args.annotations); got != tt.want {
				t.Errorf("CheckNUMABindingAntiAffinity() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckNonCPUAffinityNUMA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		meta *AllocationMeta
		want bool
	}{
		{
			name: "nil meta",
			meta: nil,
			want: false,
		},
		{
			name: "empty annotations",
			meta: &AllocationMeta{
				Annotations: map[string]string{},
			},
			want: false,
		},
		{
			name: "only cpu numa affinity",
			meta: &AllocationMeta{
				Annotations: map[string]string{
					consts.PodAnnotationCPUEnhancementNumaAffinity: consts.PodAnnotationCPUEnhancementNumaAffinityEnable,
				},
			},
			want: true,
		},
		{
			name: "only memory numa binding",
			meta: &AllocationMeta{
				Annotations: map[string]string{
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			want: true,
		},
		{
			name: "both cpu and memory numa settings",
			meta: &AllocationMeta{
				Annotations: map[string]string{
					consts.PodAnnotationCPUEnhancementNumaAffinity:   consts.PodAnnotationCPUEnhancementNumaAffinityEnable,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			want: true,
		},
		{
			name: "incorrect cpu numa affinity value",
			meta: &AllocationMeta{
				Annotations: map[string]string{
					consts.PodAnnotationCPUEnhancementNumaAffinity: "false",
				},
			},
			want: false,
		},
		{
			name: "incorrect memory numa binding value",
			meta: &AllocationMeta{
				Annotations: map[string]string{
					consts.PodAnnotationMemoryEnhancementNumaBinding: "false",
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CheckNonCPUAffinityNUMA(tt.meta); got != tt.want {
				t.Errorf("CheckNonCPUAffinityNUMA() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckNonBindingCPUAffinityNUMA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		meta        *AllocationMeta
		annotations map[string]string
		want        bool
	}{
		{
			name:        "nil meta",
			meta:        nil,
			annotations: nil,
			want:        false,
		},
		{
			name: "meta has numa not share",
			meta: &AllocationMeta{
				Annotations: map[string]string{
					consts.PodAnnotationCPUEnhancementNUMAShare: consts.PodAnnotationCPUEnhancementNUMAShareDisable,
				},
			},
			annotations: nil,
			want:        false,
		},
		{
			name: "annotations has numa not share",
			meta: &AllocationMeta{
				Annotations: map[string]string{},
			},
			annotations: map[string]string{
				consts.PodAnnotationCPUEnhancementNUMAShare: consts.PodAnnotationCPUEnhancementNUMAShareDisable,
			},
			want: false,
		},
		{
			name: "both have numa not share",
			meta: &AllocationMeta{
				Annotations: map[string]string{
					consts.PodAnnotationCPUEnhancementNUMAShare: consts.PodAnnotationCPUEnhancementNUMAShareDisable,
				},
			},
			annotations: map[string]string{
				consts.PodAnnotationCPUEnhancementNUMAShare: consts.PodAnnotationCPUEnhancementNUMAShareDisable,
			},
			want: false,
		},
		{
			name: "only cpu numa affinity",
			meta: &AllocationMeta{
				Annotations: map[string]string{
					consts.PodAnnotationCPUEnhancementNumaAffinity: consts.PodAnnotationCPUEnhancementNumaAffinityEnable,
				},
			},
			annotations: nil,
			want:        true,
		},
		{
			name: "cpu numa affinity and memory numa binding",
			meta: &AllocationMeta{
				Annotations: map[string]string{
					consts.PodAnnotationCPUEnhancementNumaAffinity:   consts.PodAnnotationCPUEnhancementNumaAffinityEnable,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			annotations: nil,
			want:        false,
		},
		{
			name: "no relevant annotations",
			meta: &AllocationMeta{
				Annotations: map[string]string{},
			},
			annotations: nil,
			want:        false,
		},
		{
			name: "incorrect annotation values",
			meta: &AllocationMeta{
				Annotations: map[string]string{
					consts.PodAnnotationCPUEnhancementNumaAffinity: "false",
				},
			},
			annotations: nil,
			want:        false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CheckNonBindingCPUAffinityNUMA(tt.meta, tt.annotations); got != tt.want {
				t.Errorf("CheckNonBindingCPUAffinityNUMA() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckNUMABindingWithAffinity(t *testing.T) {
	t.Parallel()
	// case 1: meta is nil
	assert.False(t, CheckNUMABindingWithAffinity(nil, map[string]string{}))

	// case 2: annotations is empty
	meta := &AllocationMeta{}
	assert.False(t, CheckNUMABindingWithAffinity(meta, map[string]string{}))

	// case 3: QoS level not match
	meta = &AllocationMeta{}
	annotations := map[string]string{
		consts.PodAnnotationQoSLevelKey: "shared_cores",
	}
	meta.QoSLevel = "dedicated_cores"
	assert.False(t, CheckNUMABindingWithAffinity(meta, annotations))

	// case 4: NUMANotShare
	meta = &AllocationMeta{}
	meta.QoSLevel = "shared_cores"
	meta.Annotations = map[string]string{
		consts.PodAnnotationCPUEnhancementNUMAShare: consts.PodAnnotationCPUEnhancementNUMAShareDisable,
	}
	assert.False(t, CheckNUMABindingWithAffinity(meta, annotations))

	// case 5: NonNumaBinding
	meta = &AllocationMeta{}
	meta.Annotations = map[string]string{
		consts.PodAnnotationMemoryEnhancementNumaBinding: "false",
	}
	assert.False(t, CheckNUMABindingWithAffinity(meta, annotations))

	// case 6: PoolNameMatch
	meta = &AllocationMeta{}
	meta.QoSLevel = "shared_cores"
	meta.Annotations = map[string]string{
		consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		consts.PodAnnotationCPUEnhancementCPUSet:         "pool1",
	}
	annotations = map[string]string{
		consts.PodAnnotationQoSLevelKey:          "shared_cores",
		consts.PodAnnotationCPUEnhancementCPUSet: "pool1",
	}
	assert.True(t, CheckNUMABindingWithAffinity(meta, annotations))

	// case 7: PoolNameNotMatch
	meta = &AllocationMeta{}
	annotations = map[string]string{
		consts.PodAnnotationCPUEnhancementCPUSet: "pool2",
	}
	assert.False(t, CheckNUMABindingWithAffinity(meta, annotations))
}
