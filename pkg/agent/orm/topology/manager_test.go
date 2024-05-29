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

package topology

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/kubelet/lifecycle"

	"github.com/kubewharf/katalyst-core/pkg/util/bitmask"
)

func NewTestBitMask(sockets ...int) bitmask.BitMask {
	s, _ := bitmask.NewBitMask(sockets...)
	return s
}

type mockHintProvider struct {
	th map[string][]TopologyHint
	// TODO: Add this field and add some tests to make sure things error out
	// appropriately on allocation errors.
	// allocateError error
}

func (m *mockHintProvider) GetTopologyHints(pod *v1.Pod, container *v1.Container) map[string][]TopologyHint {
	return m.th
}

func (m *mockHintProvider) GetPodTopologyHints(pod *v1.Pod) map[string][]TopologyHint {
	return m.th
}

func (m *mockHintProvider) Allocate(pod *v1.Pod, container *v1.Container) error {
	// return allocateError
	return nil
}

func TestNewManager(t *testing.T) {
	t.Parallel()
	tcases := []struct {
		description    string
		policyName     string
		expectedPolicy string
		expectedError  error
	}{
		{
			description:    "Policy is set to none",
			policyName:     "none",
			expectedPolicy: "none",
		},
		{
			description:    "Policy is set to best-effort",
			policyName:     "best-effort",
			expectedPolicy: "best-effort",
		},
		{
			description:    "Policy is set to restricted",
			policyName:     "restricted",
			expectedPolicy: "restricted",
		},
		{
			description:    "Policy is set to single-numa-node",
			policyName:     "single-numa-node",
			expectedPolicy: "single-numa-node",
		},
		{
			description:   "Policy is set to unknown",
			policyName:    "unknown",
			expectedError: fmt.Errorf("unknown policy: \"unknown\""),
		},
	}

	for _, tc := range tcases {
		mngr, err := NewManager(nil, tc.policyName, nil)

		if tc.expectedError != nil {
			if !strings.Contains(err.Error(), tc.expectedError.Error()) {
				t.Errorf("Unexpected error message. Have: %s wants %s", err.Error(), tc.expectedError.Error())
			}
		} else {
			rawMgr := mngr.(*manager)
			if rawMgr.policy.Name() != tc.expectedPolicy {
				t.Errorf("Unexpected policy name. Have: %q wants %q", rawMgr.policy.Name(), tc.expectedPolicy)
			}
		}
	}
}

func TestAddHintProvider(t *testing.T) {
	t.Parallel()
	tcases := []struct {
		name string
		hp   []HintProvider
	}{
		{
			name: "Add HintProvider",
			hp: []HintProvider{
				&mockHintProvider{},
				&mockHintProvider{},
				&mockHintProvider{},
			},
		},
	}
	mngr := manager{
		hintProviders: make([]HintProvider, 0),
	}
	for _, tc := range tcases {
		for _, hp := range tc.hp {
			mngr.AddHintProvider(hp)
		}
		if len(tc.hp) != len(mngr.hintProviders) {
			t.Errorf("error")
		}
	}
}

func TestGetAffinity(t *testing.T) {
	t.Parallel()
	tcases := []struct {
		name          string
		resourceName  string
		containerName string
		podUID        string
		expected      TopologyHint
	}{
		{
			name:          "case1",
			resourceName:  "*",
			containerName: "nginx",
			podUID:        "0aafa4c4-38e8-11e9-bcb1-a4bf01040474",
			expected:      TopologyHint{},
		},
		{
			name:          "case2",
			containerName: "preferredContainer",
			resourceName:  "cpu",
			podUID:        "testpoduid",
			expected: TopologyHint{
				Preferred:        true,
				NUMANodeAffinity: NewTestBitMask(0),
			},
		},
		{
			name:          "case3",
			resourceName:  "cpu",
			containerName: "notpreferedContainer",
			podUID:        "testpoduid",
			expected: TopologyHint{
				Preferred:        false,
				NUMANodeAffinity: NewTestBitMask(0, 1),
			},
		},
	}

	mngr := manager{
		podTopologyHints: map[string]podTopologyHints{},
	}
	mngr.setTopologyHints("testpoduid", "preferredContainer", map[string]TopologyHint{
		"cpu": {
			Preferred:        true,
			NUMANodeAffinity: NewTestBitMask(0),
		},
	})
	mngr.setTopologyHints("testpoduid", "notpreferedContainer", map[string]TopologyHint{
		"cpu": {
			Preferred:        false,
			NUMANodeAffinity: NewTestBitMask(0, 1),
		},
	})

	for _, tc := range tcases {
		actual := mngr.GetAffinity(tc.podUID, tc.containerName, tc.resourceName)
		if !reflect.DeepEqual(actual, tc.expected) {
			t.Errorf("Expected Affinity in result to be %v, got %v", tc.expected, actual)
		}
	}
}

func TestRemovePod(t *testing.T) {
	t.Parallel()
	mngr := manager{
		podTopologyHints: map[string]podTopologyHints{},
	}
	mngr.setTopologyHints("testpoduid", "testContainer", map[string]TopologyHint{
		"cpu": {
			Preferred:        true,
			NUMANodeAffinity: NewTestBitMask(0),
		},
	})

	mngr.RemovePod("none")
	assert.Equal(t, 1, len(mngr.podTopologyHints))
	mngr.RemovePod("testpoduid")
	assert.Equal(t, 0, len(mngr.podTopologyHints))
}

func TestAdmit(t *testing.T) {
	t.Parallel()
	numaNodes := []int{0, 1}

	tcases := []struct {
		name        string
		result      lifecycle.PodAdmitResult
		policy      Policy
		hp          []HintProvider
		expectedErr error
		expected    TopologyHint
	}{
		{
			name:        "None Policy. No Hints.",
			policy:      NewNonePolicy(),
			hp:          []HintProvider{},
			expectedErr: nil,
			expected:    TopologyHint{},
		},
		{
			name:        "None Policy. No Hints.",
			policy:      NewNonePolicy(),
			hp:          []HintProvider{},
			expectedErr: nil,
			expected:    TopologyHint{},
		},
		{
			name:   "single-numa-node Policy. No Hints.",
			policy: NewSingleNumaNodePolicy(numaNodes),
			hp: []HintProvider{
				&mockHintProvider{},
			},
			expectedErr: nil,
			expected:    TopologyHint{},
		},
		{
			name:   "Restricted Policy. No Hints.",
			policy: NewRestrictedPolicy(numaNodes),
			hp: []HintProvider{
				&mockHintProvider{},
			},
			expectedErr: nil,
			expected:    TopologyHint{},
		},
		{
			name:   "BestEffort Policy. Preferred Affinity.",
			policy: NewBestEffortPolicy(numaNodes),
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expectedErr: nil,
			expected: TopologyHint{
				NUMANodeAffinity: NewTestBitMask(0),
				Preferred:        true,
			},
		},
		{
			name:   "BestEffort Policy. More than one Preferred Affinity.",
			policy: NewBestEffortPolicy(numaNodes),
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expectedErr: nil,
			expected: TopologyHint{
				NUMANodeAffinity: NewTestBitMask(0),
				Preferred:        true,
			},
		},
		{
			name:   "BestEffort Policy. More than one Preferred Affinity.",
			policy: NewBestEffortPolicy(numaNodes),
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expectedErr: nil,
			expected: TopologyHint{
				NUMANodeAffinity: NewTestBitMask(0),
				Preferred:        true,
			},
		},
		{
			name:   "BestEffort Policy. No Preferred Affinity.",
			policy: NewBestEffortPolicy(numaNodes),
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expectedErr: nil,
			expected: TopologyHint{
				NUMANodeAffinity: NewTestBitMask(0, 1),
				Preferred:        false,
			},
		},
		{
			name:   "Restricted Policy. Preferred Affinity.",
			policy: NewRestrictedPolicy(numaNodes),
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expectedErr: nil,
			expected: TopologyHint{
				NUMANodeAffinity: NewTestBitMask(0),
				Preferred:        true,
			},
		},
		{
			name:   "Restricted Policy. Preferred Affinity.",
			policy: NewRestrictedPolicy(numaNodes),
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expectedErr: nil,
			expected: TopologyHint{
				NUMANodeAffinity: NewTestBitMask(0),
				Preferred:        true,
			},
		},
		{
			name:   "Restricted Policy. More than one Preferred affinity.",
			policy: NewRestrictedPolicy(numaNodes),
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expectedErr: nil,
			expected: TopologyHint{
				NUMANodeAffinity: NewTestBitMask(0),
				Preferred:        true,
			},
		},
		{
			name:   "Restricted Policy. More than one Preferred affinity.",
			policy: NewRestrictedPolicy(numaNodes),
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expectedErr: nil,
			expected: TopologyHint{
				NUMANodeAffinity: NewTestBitMask(0),
				Preferred:        true,
			},
		},
		{
			name:   "Restricted Policy. No Preferred affinity.",
			policy: NewRestrictedPolicy(numaNodes),
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expectedErr: fmt.Errorf("pod: %v, containerName: %v not admit", "testPod", "testContainer"),
			expected:    TopologyHint{},
		},
		{
			name:   "Restricted Policy. No Preferred affinity.",
			policy: NewRestrictedPolicy(numaNodes),
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expectedErr: fmt.Errorf("pod: %v, containerName: %v not admit", "testPod", "testContainer"),
			expected:    TopologyHint{},
		},
	}
	for _, tc := range tcases {
		topologyManager := manager{
			policy:           tc.policy,
			hintProviders:    tc.hp,
			podTopologyHints: map[string]podTopologyHints{},
		}

		pod := &v1.Pod{
			ObjectMeta: v12.ObjectMeta{
				Name: "testPod",
				UID:  "testUID",
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name:      "testContainer",
						Resources: v1.ResourceRequirements{},
					},
				},
			},
			Status: v1.PodStatus{},
		}

		err := topologyManager.Admit(pod)
		assert.Equal(t, tc.expectedErr, err)
		if err != nil {
			assert.Equal(t, tc.expected, topologyManager.GetAffinity("testUID", "testContainer", "resource"))
		}
	}
}
