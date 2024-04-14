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
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/util/bitmask"
)

type policyMergeTestCase struct {
	name     string
	hp       []HintProvider
	expected map[string]TopologyHint
}

func commonPolicyMergeTestCases(numaNodes []int) []policyMergeTestCase {
	return []policyMergeTestCase{
		{
			name: "Two providers, 1 hint each, same mask, both preferred 1/2",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
			},
		},
		{
			name: "Two providers, 1 hint each, same mask, both preferred 2/2",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        true,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        true,
				},
			},
		},
		{
			name: "Two providers, 1 no hints, 1 single hint preferred 1/2",
			hp: []HintProvider{
				&mockHintProvider{},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				defaultResourceKey: {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
				"resource": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
			},
		},
		{
			name: "Two providers, 1 no hints, 1 single hint preferred 2/2",
			hp: []HintProvider{
				&mockHintProvider{},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				defaultResourceKey: {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        true,
				},
				"resource": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        true,
				},
			},
		},
		{
			name: "Two providers, 1 with 2 hints, 1 with single hint matching 1/2",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
			},
		},
		{
			name: "Two providers, 1 with 2 hints, 1 with single hint matching 2/2",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        true,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        true,
				},
			},
		},
		{
			name: "Two providers, both with 2 hints, matching narrower preferred hint from both",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
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
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
			},
		},
		{
			name: "Ensure less narrow preferred hints are chosen over narrower non-preferred hints",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
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
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
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
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        true,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        true,
				},
			},
		},
		{
			name: "Multiple resources, same provider",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
						"resource2": {
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
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        true,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        true,
				},
			},
		},
	}
}

func (p *bestEffortPolicy) mergeTestCases(numaNodes []int) []policyMergeTestCase {
	return []policyMergeTestCase{
		{
			name: "Two providers, 2 hints each, same mask (some with different bits), same preferred",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 2),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 2),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(0, 1),
					Preferred:        true,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(0, 1),
					Preferred:        true,
				},
			},
		},
		{
			name: "TopologyHint not set",
			hp:   []HintProvider{},
			expected: map[string]TopologyHint{
				defaultResourceKey: {
					NUMANodeAffinity: NewTestBitMask(numaNodes...),
					Preferred:        true,
				},
			},
		},
		{
			name: "HintProvider returns empty non-nil map[string][]TopologyHint",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{},
				},
			},
			expected: map[string]TopologyHint{
				defaultResourceKey: {
					NUMANodeAffinity: NewTestBitMask(numaNodes...),
					Preferred:        true,
				},
			},
		},
		{
			name: "HintProvider returns -nil map[string][]TopologyHint from provider",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": nil,
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource": {
					NUMANodeAffinity: NewTestBitMask(numaNodes...),
					Preferred:        true,
				},
			},
		},
		{
			name: "HintProvider returns empty non-nil map[string][]TopologyHint from provider", hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource": {
					NUMANodeAffinity: NewTestBitMask(numaNodes...),
					Preferred:        false,
				},
			},
		},
		{
			name: "Single TopologyHint with Preferred as true and NUMANodeAffinity as nil",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: nil,
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource": {
					NUMANodeAffinity: NewTestBitMask(numaNodes...),
					Preferred:        true,
				},
			},
		},
		{
			name: "Single TopologyHint with Preferred as false and NUMANodeAffinity as nil",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: nil,
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource": {
					NUMANodeAffinity: NewTestBitMask(numaNodes...),
					Preferred:        false,
				},
			},
		},
		{
			name: "Two providers, 1 hint each, no common mask",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(numaNodes...),
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(numaNodes...),
					Preferred:        false,
				},
			},
		},
		{
			name: "Two providers, 1 hint each, same mask, 1 preferred, 1 not 1/2",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        false,
				},
			},
		},
		{
			name: "Two providers, 1 hint each, same mask, 1 preferred, 1 not 2/2",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        false,
				},
			},
		},
		{
			name: "Two providers, 1 hint each, 1 wider mask, both preferred 1/2",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        false,
				},
			},
		},
		{
			name: "Two providers, 1 with 2 hints, 1 with single non-preferred hint matching",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        false,
				},
			},
		},
		{
			name: "Two providers, 1 hint each, 1 wider mask, both preferred 2/2",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        false,
				},
			},
		},
		{
			name: "bestNonPreferredAffinityCount (1)",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1, 2, 3),
								Preferred:        false,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(0, 1),
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(0, 1),
					Preferred:        false,
				},
			},
		},
		{
			name: "bestNonPreferredAffinityCount (2)",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1, 2, 3),
								Preferred:        false,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 3),
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(0, 3),
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(0, 3),
					Preferred:        false,
				},
			},
		},
		{
			name: "bestNonPreferredAffinityCount (3)",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1, 2, 3),
								Preferred:        false,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(1, 2),
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(1, 2),
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(1, 2),
					Preferred:        false,
				},
			},
		},
		{
			name: "bestNonPreferredAffinityCount (4)",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1, 2, 3),
								Preferred:        false,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(2, 3),
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(2, 3),
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(2, 3),
					Preferred:        false,
				},
			},
		},
		{
			name: "bestNonPreferredAffinityCount (5)",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1, 2, 3),
								Preferred:        false,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(1, 2),
								Preferred:        false,
							},
							{
								NUMANodeAffinity: NewTestBitMask(2, 3),
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(1, 2),
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(1, 2),
					Preferred:        false,
				},
			},
		},
		{
			name: "bestNonPreferredAffinityCount (6)",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1, 2, 3),
								Preferred:        false,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(1, 2, 3),
								Preferred:        false,
							},
							{
								NUMANodeAffinity: NewTestBitMask(1, 2),
								Preferred:        false,
							},
							{
								NUMANodeAffinity: NewTestBitMask(1, 3),
								Preferred:        false,
							},
							{
								NUMANodeAffinity: NewTestBitMask(2, 3),
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(1, 2),
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(1, 2),
					Preferred:        false,
				},
			},
		},
	}
}

func (p *singleNumaNodePolicy) mergeTestCases(numaNodes []int) []policyMergeTestCase {
	return []policyMergeTestCase{
		{
			name: "TopologyHint not set",
			hp:   []HintProvider{},
			expected: map[string]TopologyHint{
				defaultResourceKey: {
					NUMANodeAffinity: nil,
					Preferred:        true,
				},
			},
		},
		{
			name: "HintProvider returns empty non-nil map[string][]TopologyHint",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{},
				},
			},
			expected: map[string]TopologyHint{
				defaultResourceKey: {
					NUMANodeAffinity: nil,
					Preferred:        true,
				},
			},
		},
		{
			name: "HintProvider returns -nil map[string][]TopologyHint from provider",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": nil,
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource": {
					NUMANodeAffinity: nil,
					Preferred:        true,
				},
			},
		},
		{
			name: "HintProvider returns empty non-nil map[string][]TopologyHint from provider", hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource": {
					NUMANodeAffinity: nil,
					Preferred:        false,
				},
			},
		},
		{
			name: "Single TopologyHint with Preferred as true and NUMANodeAffinity as nil",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: nil,
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource": {
					NUMANodeAffinity: nil,
					Preferred:        true,
				},
			},
		},
		{
			name: "Single TopologyHint with Preferred as false and NUMANodeAffinity as nil",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {
							{
								NUMANodeAffinity: nil,
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource": {
					NUMANodeAffinity: nil,
					Preferred:        false,
				},
			},
		},
		{
			name: "Two providers, 1 hint each, no common mask",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: nil,
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: nil,
					Preferred:        false,
				},
			},
		},
		{
			name: "Two providers, 1 hint each, same mask, 1 preferred, 1 not 1/2",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: nil,
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: nil,
					Preferred:        false,
				},
			},
		},
		{
			name: "Two providers, 1 hint each, same mask, 1 preferred, 1 not 2/2",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: nil,
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: nil,
					Preferred:        false,
				},
			},
		},
		{
			name: "Two providers, 1 with 2 hints, 1 with single non-preferred hint matching",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
					},
				},
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource2": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: nil,
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: nil,
					Preferred:        false,
				},
			},
		},
		{
			name: "Single NUMA hint generation",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        true,
							},
						},
						"resource2": {
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
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: nil,
					Preferred:        false,
				},
				"resource2": {
					NUMANodeAffinity: nil,
					Preferred:        false,
				},
			},
		},
		{
			name: "One no-preference provider",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource1": {
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
				&mockHintProvider{
					nil,
				},
			},
			expected: map[string]TopologyHint{
				"resource1": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
				defaultResourceKey: {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
			},
		},
	}
}

func testPolicyMerge(policy Policy, tcases []policyMergeTestCase, t *testing.T) {
	for _, tc := range tcases {
		var providersHints []map[string][]TopologyHint
		for _, provider := range tc.hp {
			hints := provider.GetTopologyHints(&v1.Pod{}, &v1.Container{})
			providersHints = append(providersHints, hints)
		}

		actual, _ := policy.Merge(providersHints)
		if !reflect.DeepEqual(actual, tc.expected) {
			t.Errorf("%v: Expected Topology Hint to be %v, got %v:", tc.name, tc.expected, actual)
		}
	}
}

func TestMaxOfMinAffinityCounts(t *testing.T) {
	t.Parallel()

	tcases := []struct {
		hints    [][]TopologyHint
		expected int
	}{
		{
			[][]TopologyHint{},
			0,
		},
		{
			[][]TopologyHint{
				{
					TopologyHint{NewTestBitMask(), true},
				},
			},
			0,
		},
		{
			[][]TopologyHint{
				{
					TopologyHint{NewTestBitMask(0), true},
				},
			},
			1,
		},
		{
			[][]TopologyHint{
				{
					TopologyHint{NewTestBitMask(0, 1), true},
				},
			},
			2,
		},
		{
			[][]TopologyHint{
				{
					TopologyHint{NewTestBitMask(0, 1), true},
					TopologyHint{NewTestBitMask(0, 1, 2), true},
				},
			},
			2,
		},
		{
			[][]TopologyHint{
				{
					TopologyHint{NewTestBitMask(0, 1), true},
					TopologyHint{NewTestBitMask(0, 1, 2), true},
				},
				{
					TopologyHint{NewTestBitMask(0, 1, 2), true},
				},
			},
			3,
		},
		{
			[][]TopologyHint{
				{
					TopologyHint{NewTestBitMask(0, 1), true},
					TopologyHint{NewTestBitMask(0, 1, 2), true},
				},
				{
					TopologyHint{NewTestBitMask(0, 1, 2), true},
					TopologyHint{NewTestBitMask(0, 1, 2, 3), true},
				},
			},
			3,
		},
	}

	for _, tc := range tcases {
		tc := tc
		t.Run("", func(t *testing.T) {
			t.Parallel()
			result := maxOfMinAffinityCounts(tc.hints)
			if result != tc.expected {
				t.Errorf("Expected result to be %v, got %v", tc.expected, result)
			}
		})
	}
}

func TestCompareHints(t *testing.T) {
	t.Parallel()

	tcases := []struct {
		description                   string
		bestNonPreferredAffinityCount int
		current                       *TopologyHint
		candidate                     *TopologyHint
		expected                      string
	}{
		{
			"candidate.NUMANodeAffinity.Count() == 0 (1)",
			-1,
			nil,
			&TopologyHint{bitmask.NewEmptyBitMask(), false},
			"current",
		},
		{
			"candidate.NUMANodeAffinity.Count() == 0 (2)",
			-1,
			&TopologyHint{NewTestBitMask(), true},
			&TopologyHint{NewTestBitMask(), false},
			"current",
		},
		{
			"current == nil (1)",
			-1,
			nil,
			&TopologyHint{NewTestBitMask(0), true},
			"candidate",
		},
		{
			"current == nil (2)",
			-1,
			nil,
			&TopologyHint{NewTestBitMask(0), false},
			"candidate",
		},
		{
			"!current.Preferred && candidate.Preferred",
			-1,
			&TopologyHint{NewTestBitMask(0), false},
			&TopologyHint{NewTestBitMask(0), true},
			"candidate",
		},
		{
			"current.Preferred && !candidate.Preferred",
			-1,
			&TopologyHint{NewTestBitMask(0), true},
			&TopologyHint{NewTestBitMask(0), false},
			"current",
		},
		{
			"current.Preferred && candidate.Preferred (1)",
			-1,
			&TopologyHint{NewTestBitMask(0), true},
			&TopologyHint{NewTestBitMask(0), true},
			"current",
		},
		{
			"current.Preferred && candidate.Preferred (2)",
			-1,
			&TopologyHint{NewTestBitMask(0, 1), true},
			&TopologyHint{NewTestBitMask(0), true},
			"candidate",
		},
		{
			"current.Preferred && candidate.Preferred (3)",
			-1,
			&TopologyHint{NewTestBitMask(0), true},
			&TopologyHint{NewTestBitMask(0, 1), true},
			"current",
		},
		{
			"!current.Preferred && !candidate.Preferred (1.1)",
			1,
			&TopologyHint{NewTestBitMask(0, 1), false},
			&TopologyHint{NewTestBitMask(0, 1), false},
			"current",
		},
		{
			"!current.Preferred && !candidate.Preferred (1.2)",
			1,
			&TopologyHint{NewTestBitMask(1, 2), false},
			&TopologyHint{NewTestBitMask(0, 1), false},
			"candidate",
		},
		{
			"!current.Preferred && !candidate.Preferred (1.3)",
			1,
			&TopologyHint{NewTestBitMask(0, 1), false},
			&TopologyHint{NewTestBitMask(1, 2), false},
			"current",
		},
		{
			"!current.Preferred && !candidate.Preferred (2.1)",
			2,
			&TopologyHint{NewTestBitMask(0, 1), false},
			&TopologyHint{NewTestBitMask(0), false},
			"current",
		},
		{
			"!current.Preferred && !candidate.Preferred (2.2)",
			2,
			&TopologyHint{NewTestBitMask(0, 1), false},
			&TopologyHint{NewTestBitMask(0, 1), false},
			"current",
		},
		{
			"!current.Preferred && !candidate.Preferred (2.3)",
			2,
			&TopologyHint{NewTestBitMask(1, 2), false},
			&TopologyHint{NewTestBitMask(0, 1), false},
			"candidate",
		},
		{
			"!current.Preferred && !candidate.Preferred (2.4)",
			2,
			&TopologyHint{NewTestBitMask(0, 1), false},
			&TopologyHint{NewTestBitMask(1, 2), false},
			"current",
		},
		{
			"!current.Preferred && !candidate.Preferred (3a)",
			2,
			&TopologyHint{NewTestBitMask(0), false},
			&TopologyHint{NewTestBitMask(0, 1, 2), false},
			"current",
		},
		{
			"!current.Preferred && !candidate.Preferred (3b)",
			2,
			&TopologyHint{NewTestBitMask(0), false},
			&TopologyHint{NewTestBitMask(0, 1), false},
			"candidate",
		},
		{
			"!current.Preferred && !candidate.Preferred (3ca.1)",
			3,
			&TopologyHint{NewTestBitMask(0), false},
			&TopologyHint{NewTestBitMask(0, 1), false},
			"candidate",
		},
		{
			"!current.Preferred && !candidate.Preferred (3ca.2)",
			3,
			&TopologyHint{NewTestBitMask(0), false},
			&TopologyHint{NewTestBitMask(1, 2), false},
			"candidate",
		},
		{
			"!current.Preferred && !candidate.Preferred (3ca.3)",
			4,
			&TopologyHint{NewTestBitMask(0, 1), false},
			&TopologyHint{NewTestBitMask(1, 2, 3), false},
			"candidate",
		},
		{
			"!current.Preferred && !candidate.Preferred (3cb)",
			4,
			&TopologyHint{NewTestBitMask(1, 2, 3), false},
			&TopologyHint{NewTestBitMask(0, 1), false},
			"current",
		},
		{
			"!current.Preferred && !candidate.Preferred (3cc.1)",
			4,
			&TopologyHint{NewTestBitMask(0, 1, 2), false},
			&TopologyHint{NewTestBitMask(0, 1, 2), false},
			"current",
		},
		{
			"!current.Preferred && !candidate.Preferred (3cc.2)",
			4,
			&TopologyHint{NewTestBitMask(0, 1, 2), false},
			&TopologyHint{NewTestBitMask(1, 2, 3), false},
			"current",
		},
		{
			"!current.Preferred && !candidate.Preferred (3cc.3)",
			4,
			&TopologyHint{NewTestBitMask(1, 2, 3), false},
			&TopologyHint{NewTestBitMask(0, 1, 2), false},
			"candidate",
		},
	}

	for _, tc := range tcases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			result := compareHints(tc.bestNonPreferredAffinityCount, tc.current, tc.candidate)
			if result != tc.current && result != tc.candidate {
				t.Errorf("Expected result to be either 'current' or 'candidate' hint")
			}
			if tc.expected == "current" && result != tc.current {
				t.Errorf("Expected result to be %v, got %v", tc.current, result)
			}
			if tc.expected == "candidate" && result != tc.candidate {
				t.Errorf("Expected result to be %v, got %v", tc.candidate, result)
			}
		})
	}
}
