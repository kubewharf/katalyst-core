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

package state

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestAllocationResourcesMap_GetRatioOfAccompanyResourceToTargetResource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		accompanyResourceName string
		targetResourceName    string
		arm                   AllocationResourcesMap
		want                  float64
	}{
		{
			name:                  "normal case",
			accompanyResourceName: "accompanyResource",
			targetResourceName:    "targetResource",
			arm: AllocationResourcesMap{
				v1.ResourceName("accompanyResource"): {
					"accompany1": {},
					"accompany2": {},
					"accompany3": {},
					"accompany4": {},
				},
				v1.ResourceName("targetResource"): {
					"target1": {},
					"target2": {},
				},
			},
			want: 2.0,
		},
		{
			name:                  "got a ratio that is a fraction",
			accompanyResourceName: "accompanyResource",
			targetResourceName:    "targetResource",
			arm: AllocationResourcesMap{
				v1.ResourceName("accompanyResource"): {
					"accompany1": {},
					"accompany2": {},
				},
				v1.ResourceName("targetResource"): {
					"target1": {},
					"target2": {},
					"target3": {},
					"target4": {},
				},
			},
			want: 0.5,
		},
		{
			name:                  "no devices for target resource",
			accompanyResourceName: "accompanyResource",
			targetResourceName:    "targetResource",
			arm: AllocationResourcesMap{
				v1.ResourceName("accompanyResource"): {
					"accompany1": {},
					"accompany2": {},
				},
			},
			want: 0,
		},
		{
			name:                  "no devices for accompany resource and target resource",
			accompanyResourceName: "accompanyResource",
			targetResourceName:    "targetResource",
			arm:                   AllocationResourcesMap{},
			want:                  0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.arm.GetRatioOfAccompanyResourceToTargetResource(tt.accompanyResourceName, tt.targetResourceName)
			if got != tt.want {
				t.Errorf("GetRatioOfAccompanyResourceToTargetResource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPodResourceEntries_GetTotalAllocatedResourceOfContainer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                        string
		resourceName                v1.ResourceName
		podUID                      string
		containerName               string
		pre                         PodResourceEntries
		wantTotalAllocationQuantity int
		wantAllocationIDs           sets.String
	}{
		{
			name:          "normal case",
			resourceName:  v1.ResourceName("testResource"),
			podUID:        "podUID",
			containerName: "containerName",
			pre: PodResourceEntries{
				v1.ResourceName("testResource"): {
					"podUID2": {
						"containerName": {
							AllocatedAllocation: Allocation{
								Quantity: 2,
							},
							TopologyAwareAllocations: map[string]Allocation{
								"test-1": {
									Quantity: 1,
								},
								"test-2": {
									Quantity: 1,
								},
							},
						},
					},
					"podUID": {
						"containerName": {
							AllocatedAllocation: Allocation{
								Quantity: 2,
							},
							TopologyAwareAllocations: map[string]Allocation{
								"test-3": {
									Quantity: 1,
								},
								"test-4": {
									Quantity: 1,
								},
							},
						},
					},
				},
			},
			wantTotalAllocationQuantity: 2,
			wantAllocationIDs:           sets.NewString("test-3", "test-4"),
		},
		{
			name:          "no allocation",
			resourceName:  v1.ResourceName("testResource"),
			podUID:        "podUID",
			containerName: "containerName",
			pre: PodResourceEntries{
				v1.ResourceName("testResource"): {
					"podUID2": {
						"containerName": {
							AllocatedAllocation: Allocation{
								Quantity: 2,
							},
							TopologyAwareAllocations: map[string]Allocation{
								"test-1": {
									Quantity: 1,
								},
								"test-2": {
									Quantity: 1,
								},
							},
						},
					},
				},
			},
			wantTotalAllocationQuantity: 0,
			wantAllocationIDs:           sets.NewString(),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotTotalAllocationQuantity, gotAllocationIDs := tt.pre.GetTotalAllocatedResourceOfContainer(tt.resourceName, tt.podUID, tt.containerName)
			assert.Equal(t, tt.wantTotalAllocationQuantity, gotTotalAllocationQuantity)

			assert.ElementsMatch(t, tt.wantAllocationIDs.UnsortedList(), gotAllocationIDs.UnsortedList())
		})
	}
}
