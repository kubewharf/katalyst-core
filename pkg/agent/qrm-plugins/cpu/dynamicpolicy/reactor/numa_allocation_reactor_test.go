package reactor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestNumaPodAllocationWrapper_getNUMAAllocationResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		allocationInfo *state.AllocationInfo
		expectedResult string
		expectedError  bool
	}{
		{
			name: "single numa node allocation",
			allocationInfo: &state.AllocationInfo{
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 1), // NUMA node 0 with CPUs 0, 1
				},
			},
			expectedResult: "0",
			expectedError:  false,
		},
		{
			name: "multiple numa nodes allocation",
			allocationInfo: &state.AllocationInfo{
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 1), // NUMA node 0 with CPUs 0, 1
					1: machine.NewCPUSet(2, 3), // NUMA node 1 with CPUs 2, 3
				},
			},
			expectedResult: "0,1", // Should be sorted
			expectedError:  false,
		},
		{
			name: "empty topology assignments",
			allocationInfo: &state.AllocationInfo{
				TopologyAwareAssignments: map[int]machine.CPUSet{},
			},
			expectedResult: "",
			expectedError:  true,
		},
		{
			name: "nil topology assignments",
			allocationInfo: &state.AllocationInfo{
				TopologyAwareAssignments: nil,
			},
			expectedResult: "",
			expectedError:  true,
		},
		{
			name: "negative numa node id",
			allocationInfo: &state.AllocationInfo{
				TopologyAwareAssignments: map[int]machine.CPUSet{
					-1: machine.NewCPUSet(0, 1), // Negative NUMA ID should be ignored
				},
			},
			expectedResult: "",
			expectedError:  true,
		},
		{
			name: "mixed valid and invalid numa nodes",
			allocationInfo: &state.AllocationInfo{
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0:  machine.NewCPUSet(0, 1), // Valid NUMA node 0
					-1: machine.NewCPUSet(2, 3), // Invalid NUMA node -1 (should be ignored)
					1:  machine.NewCPUSet(4, 5), // Valid NUMA node 1
				},
			},
			expectedResult: "0,1", // Should only include valid NUMA nodes
			expectedError:  false,
		},
		{
			name: "topology with empty cpusets",
			allocationInfo: &state.AllocationInfo{
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(),     // Empty CPUSet should be ignored
					1: machine.NewCPUSet(2, 3), // Valid NUMA node 1
				},
			},
			expectedResult: "1", // Should only include NUMA nodes with non-empty CPUSets
			expectedError:  false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wrapper := numaPodAllocationWrapper{
				AllocationInfo: tt.allocationInfo,
			}

			// Call the private method using reflection
			result, err := wrapper.getNUMAAllocationResult()

			if tt.expectedError {
				assert.Error(t, err, "expected error for %s", tt.name)
			} else {
				assert.NoError(t, err, "expected no error for %s", tt.name)
			}
			assert.Equal(t, tt.expectedResult, result, "expected result mismatch for %s", tt.name)
		})
	}
}

func TestNumaPodAllocationWrapper_UpdateAllocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		allocationInfo     *state.AllocationInfo
		initialPod         *v1.Pod
		expectedAnnotation string
		expectedError      bool
	}{
		{
			name: "update pod with single numa allocation",
			allocationInfo: &state.AllocationInfo{
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 1),
				},
			},
			initialPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-namespace",
					Annotations: map[string]string{
						"existing-key": "existing-value",
					},
				},
			},
			expectedAnnotation: "0",
			expectedError:      false,
		},
		{
			name: "update pod with multiple numa allocation",
			allocationInfo: &state.AllocationInfo{
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 1),
					1: machine.NewCPUSet(2, 3),
				},
			},
			initialPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-pod",
					Namespace:   "test-namespace",
					Annotations: map[string]string{},
				},
			},
			expectedAnnotation: "0,1",
			expectedError:      false,
		},
		{
			name: "update pod with empty topology (should fail)",
			allocationInfo: &state.AllocationInfo{
				TopologyAwareAssignments: map[int]machine.CPUSet{},
			},
			initialPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-pod",
					Namespace:   "test-namespace",
					Annotations: map[string]string{},
				},
			},
			expectedAnnotation: "",
			expectedError:      true,
		},
		{
			name: "update pod with nil annotations",
			allocationInfo: &state.AllocationInfo{
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 1),
				},
			},
			initialPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-pod",
					Namespace:   "test-namespace",
					Annotations: nil, // Nil annotations should be initialized
				},
			},
			expectedAnnotation: "0",
			expectedError:      false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wrapper := numaPodAllocationWrapper{
				AllocationInfo: tt.allocationInfo,
			}

			// Create a copy of the initial pod to avoid test interference
			podCopy := tt.initialPod.DeepCopy()

			err := wrapper.UpdateAllocation(podCopy)

			if tt.expectedError {
				assert.Error(t, err, "expected error for %s", tt.name)
			} else {
				assert.NoError(t, err, "expected no error for %s", tt.name)

				// Verify the annotation was set correctly
				assert.Equal(t, tt.expectedAnnotation,
					podCopy.Annotations[apiconsts.PodAnnotationNUMABindResultKey],
					"expected annotation mismatch for %s", tt.name)

				// Verify existing annotations are preserved
				if tt.initialPod.Annotations != nil {
					for key, value := range tt.initialPod.Annotations {
						if key != apiconsts.PodAnnotationNUMABindResultKey {
							assert.Equal(t, value, podCopy.Annotations[key],
								"expected existing annotation %s to be preserved", key)
						}
					}
				}
			}
		})
	}
}
