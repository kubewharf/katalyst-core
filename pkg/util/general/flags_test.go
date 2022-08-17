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

package general

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestResourceList_Set(t *testing.T) {
	type args struct {
		value string
	}
	tests := []struct {
		name         string
		args         args
		wantResource ResourceList
		wantErr      bool
	}{
		{
			name: "string to cpu and memory",
			args: args{
				value: "cpu=10,memory=10Gi",
			},
			wantResource: ResourceList{
				v1.ResourceCPU:    resource.MustParse("10"),
				v1.ResourceMemory: resource.MustParse("10Gi"),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		r := ResourceList{}
		t.Run(tt.name, func(t *testing.T) {
			err := r.Set(tt.args.value)
			assert.Equalf(t, tt.wantErr, err != nil, "Set(%v) return err", tt.args.value)
			assert.Equal(t, tt.wantResource, r)
		})
	}
}

func TestResourceList_String(t *testing.T) {
	tests := []struct {
		name string
		r    ResourceList
		want string
	}{
		{
			name: "cpu and memory to string",
			r: ResourceList{
				v1.ResourceCPU:    resource.MustParse("10"),
				v1.ResourceMemory: resource.MustParse("10Gi"),
			},
			want: "cpu=10,memory=10Gi",
		},
		{
			name: "cpu and memory to string",
			r: ResourceList{
				v1.ResourceCPU:    resource.MustParse("10m"),
				v1.ResourceMemory: resource.MustParse("15Gi"),
			},
			want: "cpu=10m,memory=15Gi",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, tt.r.String(), "String()")
		})
	}
}
