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

package dynamicpolicy

import (
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestGetFullyDropCacheBytes(t *testing.T) {
	type args struct {
		container *v1.Container
	}
	tests := []struct {
		name string
		args args
		want int64
	}{
		{
			name: "contaienr with both request and limit",
			args: args{
				container: &v1.Container{
					Name: "c1",
					Resources: v1.ResourceRequirements{
						Limits: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("3Gi"),
						},
						Requests: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("2Gi"),
						},
					},
				},
			},
			want: 3221225472,
		},
		{
			name: "contaienr only with request",
			args: args{
				container: &v1.Container{
					Name: "c1",
					Resources: v1.ResourceRequirements{
						Requests: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("2Gi"),
						},
					},
				},
			},
			want: 2147483648,
		},
		{
			name: "nil container",
			args: args{},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetFullyDropCacheBytes(tt.args.container); got != tt.want {
				t.Errorf("GetFullyDropCacheBytes() = %v, want %v", got, tt.want)
			}
		})
	}
}
