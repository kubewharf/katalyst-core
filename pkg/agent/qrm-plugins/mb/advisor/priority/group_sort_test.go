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

package priority

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"
)

func Test_getSortedGroups(t *testing.T) {
	t.Parallel()
	type args struct {
		groups []string
	}
	tests := []struct {
		name string
		args args
		want []sets.String
	}{
		{
			name: "happy path",
			args: args{
				groups: []string{"reclaim", "dedicated", "unknown", "system", "share"},
			},
			want: []sets.String{
				sets.NewString("dedicated"),
				sets.NewString("system"),
				sets.NewString("unknown"),
				sets.NewString("share"),
				sets.NewString("reclaim"),
			},
		},
		{
			name: "with share sub groups",
			args: args{
				groups: []string{"share-45", "dedicated", "share-50", "share-30"},
			},
			want: []sets.String{
				sets.NewString("dedicated"),
				sets.NewString("share-50"),
				sets.NewString("share-45"),
				sets.NewString("share-30"),
			},
		},
		{
			name: "physical shared and explicit share identities keep equal isolated priorities",
			args: args{
				groups: []string{"share-50", "shared-50", "shared-30", "share-30"},
			},
			want: []sets.String{
				sets.NewString("share-50"),
				sets.NewString("shared-50"),
				sets.NewString("share-30"),
				sets.NewString("shared-30"),
			},
		},
		{
			name: "physical share group stays separate from same-weight dedicated group",
			args: args{
				groups: []string{"share-4000", "dedicated"},
			},
			want: []sets.String{
				sets.NewString("dedicated"),
				sets.NewString("share-4000"),
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := GetInstance().SortGroups(tt.args.groups); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("sortGroups() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetSubWeightUsesOriginalSubgroupValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want int
	}{
		{name: "share-50", want: 50},
		{name: "shared-50", want: 50},
		{name: "share", want: 50},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := getSubWeight(tt.name); got != tt.want {
				t.Fatalf("getSubWeight(%q) = %d, want original subgroup %d", tt.name, got, tt.want)
			}
		})
	}
}

func TestPhysicalShareGroupsUseEqualWeightsAndDistinctEquivalenceKeys(t *testing.T) {
	t.Parallel()

	groupPriority := GetInstance()
	shareWeight := groupPriority.GetWeight("share-50")
	sharedWeight := groupPriority.GetWeight("shared-50")
	if shareWeight != sharedWeight {
		t.Fatalf("physical subgroup weights differ: share-50=%d, shared-50=%d", shareWeight, sharedWeight)
	}

	shareKey := groupPriority.GetEquivalenceGroupKey("share-50")
	sharedKey := groupPriority.GetEquivalenceGroupKey("shared-50")
	if shareKey == sharedKey {
		t.Fatalf("physical subgroup keys unexpectedly merged: share-50=%v, shared-50=%v", shareKey, sharedKey)
	}
}

func TestGetWeightSaturatesAtMaxInt(t *testing.T) {
	t.Parallel()

	maxInt := int(^uint(0) >> 1)
	groupPriority := &threadSafeGroupPriority{
		majorWeights: map[string]int{
			"share": maxInt,
		},
		exactWeights: map[string]int{},
	}

	if got := groupPriority.GetWeight("share-1"); got != maxInt {
		t.Fatalf("GetWeight() = %d, want saturated max int %d", got, maxInt)
	}
}

func TestGetWeightPrefersExactCLOSPriority(t *testing.T) {
	t.Parallel()

	groupPriority := GetInstance()
	groupPriority.AddWeight("share-123", 7_777)

	if got := groupPriority.GetWeight("share-123"); got != 7_777 {
		t.Fatalf("GetWeight() = %d, want exact CLOS priority %d", got, 7_777)
	}
}

func TestSortGroupsKeepsEqualEquivalenceKeysContiguous(t *testing.T) {
	t.Parallel()

	groupPriority := GetInstance()
	groupPriority.AddWeight("zzz", groupPriority.GetWeight("dedicated"))

	got := groupPriority.SortGroups([]string{"zzz", "share-4000", "dedicated"})
	want := []sets.String{
		sets.NewString("dedicated", "zzz"),
		sets.NewString("share-4000"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SortGroups() = %v, want ordinary groups merged and physical group separate: %v", got, want)
	}
}
