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

package calculator

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/allocation"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/state"
)

func Test_greedyCalculator_CalculateNUMABindingResult(t *testing.T) {
	t.Parallel()
	type args struct {
		current         allocation.PodAllocations
		numaAllocatable state.NUMAResource
	}
	tests := []struct {
		name    string
		args    args
		want    allocation.PodAllocations
		want1   bool
		wantErr bool
	}{
		{
			name: "test1",
			args: args{
				current: allocation.PodAllocations{
					"pod1": &allocation.Allocation{
						NamespacedName: types.NamespacedName{
							Name:      "pod1",
							Namespace: "default",
						},
						Request: allocation.Request{
							CPUMilli: 1000,
							Memory:   1000,
						},
						BindingNUMA: -1,
					},
					"pod2": &allocation.Allocation{
						NamespacedName: types.NamespacedName{
							Name:      "pod2",
							Namespace: "default",
						},
						Request: allocation.Request{
							CPUMilli: 2000,
							Memory:   1000,
						},
						BindingNUMA: -1,
					},
					"pod3": &allocation.Allocation{
						NamespacedName: types.NamespacedName{
							Name:      "pod3",
							Namespace: "default",
						},
						Request: allocation.Request{
							CPUMilli: 3000,
							Memory:   1000,
						},
						BindingNUMA: -1,
					},
					"pod4": &allocation.Allocation{
						NamespacedName: types.NamespacedName{
							Name:      "pod4",
							Namespace: "default",
						},
						Request: allocation.Request{
							CPUMilli: 4000,
							Memory:   1000,
						},
						BindingNUMA: -1,
					},
				},
				numaAllocatable: state.NUMAResource{
					0: &state.Resource{
						CPU:    5,
						Memory: 10000,
					},
					1: &state.Resource{
						CPU:    5,
						Memory: 10000,
					},
					2: &state.Resource{
						CPU:    5,
						Memory: 10000,
					},
					3: &state.Resource{
						CPU:    5,
						Memory: 10000,
					},
				},
			},
			want: allocation.PodAllocations{
				"pod1": &allocation.Allocation{
					NamespacedName: types.NamespacedName{
						Name:      "pod1",
						Namespace: "default",
					},
					Request: allocation.Request{
						CPUMilli: 1000,
						Memory:   1000,
					},
					BindingNUMA: 0,
				},
				"pod2": &allocation.Allocation{
					NamespacedName: types.NamespacedName{
						Name:      "pod2",
						Namespace: "default",
					},
					Request: allocation.Request{
						CPUMilli: 2000,
						Memory:   1000,
					},
					BindingNUMA: 1,
				},
				"pod3": &allocation.Allocation{
					NamespacedName: types.NamespacedName{
						Name:      "pod3",
						Namespace: "default",
					},
					Request: allocation.Request{
						CPUMilli: 3000,
						Memory:   1000,
					},
					BindingNUMA: 1,
				},
				"pod4": &allocation.Allocation{
					NamespacedName: types.NamespacedName{
						Name:      "pod4",
						Namespace: "default",
					},
					Request: allocation.Request{
						CPUMilli: 4000,
						Memory:   1000,
					},
					BindingNUMA: 0,
				},
			},
			want1: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := greedyCalculator{}
			got, got1, err := g.CalculateNUMABindingResult(tt.args.current, tt.args.numaAllocatable)
			if (err != nil) != tt.wantErr {
				t.Errorf("CalculateNUMABindingResult() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CalculateNUMABindingResult() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("CalculateNUMABindingResult() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}
