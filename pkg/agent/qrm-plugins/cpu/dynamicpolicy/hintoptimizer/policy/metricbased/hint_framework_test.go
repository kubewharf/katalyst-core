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
	"testing"

	"github.com/stretchr/testify/assert"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestRequestSufficientFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		request        float64
		allocated      float64
		reservedCPUs   machine.CPUSet
		expectedResult bool
	}{
		{
			name:           "sufficient resources",
			request:        2,
			allocated:      2,
			reservedCPUs:   machine.NewCPUSet(),
			expectedResult: true,
		},
		{
			name:           "insufficient resources",
			request:        2,
			allocated:      3,
			reservedCPUs:   machine.NewCPUSet(),
			expectedResult: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			policy := &dynamicpolicy.DynamicPolicy{
				reservedCPUs: tc.reservedCPUs,
			}
			machineState := state.NUMANodeMap{
				1: &state.NUMANodeState{
					DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
					PodEntries:    makeFakePodEntries(tc.allocated),
				},
			}
			filter := policy.requestSufficientFilter()
			result := filter(tc.request, 1, machineState)
			assert.Equal(t, tc.expectedResult, result)
		})
	}
}

func TestHybridRequestScorer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		request       float64
		allocated     float64
		reservedCPUs  machine.CPUSet
		totalCPUs     int
		threshold     float64
		expectedScore int
	}{
		{
			// allocatable=7, available=7-3=4, allocated=7-4+2=5
			// threshold=7*0.7=4.9
			// ratio=(5-4.9)/(7-4.9)=0.06666666666666667
			// score=50 - 50*0.06666666666666667 = 47.61 -> 47
			name:          "above threshold",
			request:       2,
			allocated:     3,
			reservedCPUs:  machine.NewCPUSet(0),
			totalCPUs:     8,
			threshold:     0.7,
			expectedScore: 47,
		},
		{
			// allocatable=7, available=7-3=4, allocated=7-4+1=4
			// threshold=7*0.7=4.9
			// ratio=4/4.9=0.8181818181818182
			// score=50 + 50*0.8181818181818182 = 90.90 -> 90
			name:          "below threshold",
			request:       1,
			allocated:     3,
			reservedCPUs:  machine.NewCPUSet(0),
			totalCPUs:     8,
			threshold:     0.7,
			expectedScore: 90,
		},
		{
			name:          "zero allocated",
			request:       0.0,
			allocated:     0.0,
			reservedCPUs:  machine.NewCPUSet(),
			totalCPUs:     8,
			threshold:     0.7,
			expectedScore: 50,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			policy := &dynamicpolicy.DynamicPolicy{
				reservedCPUs:          tc.reservedCPUs,
				requestScoreThreshold: tc.threshold,
			}
			machineState := state.NUMANodeMap{
				1: &state.NUMANodeState{
					DefaultCPUSet: createCPUSet(tc.totalCPUs),
					PodEntries:    makeFakePodEntries(tc.allocated),
				},
			}

			scorer := policy.hybridRequestScorer()
			score := scorer.ScoreFunc(tc.request, 1, machineState)
			assert.Equal(t, tc.expectedScore, score)
		})
	}
}

func createCPUSet(count int) machine.CPUSet {
	cpus := make([]int, count)
	for i := 0; i < count; i++ {
		cpus[i] = i
	}
	return machine.NewCPUSet(cpus...)
}

func makeFakePodEntries(allocated float64) state.PodEntries {
	testName := "a"

	return state.PodEntries{
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
				RequestQuantity: allocated,
			},
		},
	}
}
