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

import "testing"

func TestPolicyNumericMerge(t *testing.T) {
	policy := NewNumericPolicy(defaultAlignResourceNames)

	tcases := []policyMergeTestCase{
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
				"resource": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
				defaultResourceKey: {
					NUMANodeAffinity: nil,
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
				"resource": {
					NUMANodeAffinity: NewTestBitMask(1),
					Preferred:        true,
				},
				defaultResourceKey: {
					NUMANodeAffinity: nil,
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
			name: "HintProvider returns empty non-nil map[string][]TopologyHint from provider",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"resource": {},
					},
				},
			},
			expected: nil,
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
			expected: nil,
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
					Preferred:        true,
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
					Preferred:        true,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(1),
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
					Preferred:        true,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(0, 1),
					Preferred:        false,
				},
			},
		},
		{
			name: "Numeric hint generation, two resource",
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
					NUMANodeAffinity: NewTestBitMask(0, 1),
					Preferred:        true,
				},
				"resource2": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
			},
		},
		{
			name: "Align cpu, memory, cpu with 2/2 hit, memory with 1/2, 2/2 hint",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"cpu": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        true,
							},
						},
						"memory": {
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
						"gpu": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"cpu": {
					NUMANodeAffinity: NewTestBitMask(0, 1),
					Preferred:        true,
				},
				"memory": {
					NUMANodeAffinity: NewTestBitMask(0, 1),
					Preferred:        false,
				},
				"gpu": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
			},
		},
		{
			name: "Align cpu, memory, cpu with 2/2 hit, memory with 1/2, 2/2 hint",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"cpu": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        true,
							},
						},
						"memory": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
						"gpu": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: nil,
		},
		{
			name: "Align cpu, cpu with 2/2 hit",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"cpu": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
						},
						"gpu": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: nil,
		},
		{
			name: "Align cpu, cpu with 1/2, 2/2 hit",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"cpu": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
						"gpu": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        false,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"cpu": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
				"gpu": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        false,
				},
			},
		},
		{
			name: "Align cpu, cpu with 1/2 hit, gpu with 1/2, 2/2 hint",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"cpu": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
						"gpu": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"cpu": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
				"gpu": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
			},
		},
		{
			name: "Align cpu, memory, cpu with 2/2 hit, memory with 1/2, 2/2 hint",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"gpu": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        false,
							},
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
						},
						"cpu": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        true,
							},
						},
						"memory": {
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
				"cpu": {
					NUMANodeAffinity: NewTestBitMask(0, 1),
					Preferred:        true,
				},
				"memory": {
					NUMANodeAffinity: NewTestBitMask(0, 1),
					Preferred:        false,
				},
				"gpu": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
			},
		},
		{
			name: "Align cpu, memory, nil, nil provider to test append bug",
			hp: []HintProvider{
				&mockHintProvider{},
				&mockHintProvider{},
				&mockHintProvider{
					map[string][]TopologyHint{
						"cpu": {
							{
								NUMANodeAffinity: NewTestBitMask(0),
								Preferred:        true,
							},
							{
								NUMANodeAffinity: NewTestBitMask(1),
								Preferred:        true,
							},
						},
						"memory": {
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
			},
			expected: map[string]TopologyHint{
				defaultResourceKey: {
					NUMANodeAffinity: nil,
					Preferred:        true,
				},
				"cpu": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
				"memory": {
					NUMANodeAffinity: NewTestBitMask(0),
					Preferred:        true,
				},
			},
		},
		{
			name: "Align cpu, memory. cpu nil preferred nil hint, memory with 2/2 hint",
			hp: []HintProvider{
				&mockHintProvider{
					map[string][]TopologyHint{
						"memory": {
							{
								NUMANodeAffinity: NewTestBitMask(0, 1),
								Preferred:        true,
							},
						},
						"cpu": {
							{
								NUMANodeAffinity: nil,
								Preferred:        true,
							},
						},
					},
				},
			},
			expected: map[string]TopologyHint{
				"memory": {
					NUMANodeAffinity: NewTestBitMask(0, 1),
					Preferred:        true,
				},
				"cpu": {
					NUMANodeAffinity: nil,
					Preferred:        true,
				},
			},
		},
	}
	testPolicyMerge(policy, tcases, t)
}
