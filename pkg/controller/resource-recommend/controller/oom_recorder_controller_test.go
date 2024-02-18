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

package controller

import (
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
)

func TestGetContainer(t *testing.T) {
	type args struct {
		pod           *v1.Pod
		containerName string
	}
	tests := []struct {
		name string
		args args
		want *v1.Container
	}{
		{
			name: "notFount",
			args: args{
				pod: &v1.Pod{
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c1",
							},
						},
					},
				},
				containerName: "cx",
			},
			want: nil,
		},
		{
			name: "Got",
			args: args{
				pod: &v1.Pod{
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name:  "c1",
								Image: "image1",
							},
						},
					},
				},
				containerName: "c1",
			},
			want: &v1.Container{
				Name:  "c1",
				Image: "image1",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetContainer(tt.args.pod, tt.args.containerName); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetContainer() = %v, want %v", got, tt.want)
			}
		})
	}
}
