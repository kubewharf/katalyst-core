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

package native

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var makePod = func(name string, request, limits v1.ResourceList) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "c1",
					Resources: v1.ResourceRequirements{
						Limits:   limits,
						Requests: request,
					},
				},
			},
		},
	}
	return pod
}

func TestNeedUpdateResources(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                       string
		pod                        *v1.Pod
		containerResourcesToUpdate map[string]v1.ResourceRequirements
		want                       bool
	}{
		{
			name: "same resource",
			pod: makePod("pod1",
				map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU: *resource.NewQuantity(2, resource.DecimalSI),
				},
				nil),
			containerResourcesToUpdate: map[string]v1.ResourceRequirements{
				"c1": {
					Requests: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU: *resource.NewQuantity(2, resource.DecimalSI),
					},
				},
			},
			want: false,
		},
		{
			name: "same resource2",
			pod: makePod("pod1",
				map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
					v1.ResourceMemory: *resource.NewQuantity(2, resource.DecimalSI),
				},
				nil),
			containerResourcesToUpdate: map[string]v1.ResourceRequirements{
				"c1": {
					Requests: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU: *resource.NewQuantity(2, resource.DecimalSI),
					},
				},
			},
			want: false,
		},
		{
			name: "diff resource",
			pod: makePod("pod1",
				map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU: *resource.NewQuantity(2, resource.DecimalSI),
				},
				nil),
			containerResourcesToUpdate: map[string]v1.ResourceRequirements{
				"c1": {
					Requests: map[v1.ResourceName]resource.Quantity{
						v1.ResourceCPU: *resource.NewQuantity(1, resource.DecimalSI),
					},
				},
			},
			want: true,
		},
		{
			name: "new resource",
			pod: makePod("pod1",
				map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU: *resource.NewQuantity(2, resource.DecimalSI),
				},
				nil),
			containerResourcesToUpdate: map[string]v1.ResourceRequirements{
				"c1": {
					Requests: map[v1.ResourceName]resource.Quantity{
						v1.ResourceMemory: *resource.NewQuantity(2, resource.DecimalSI),
					},
				},
			},
			want: true,
		},
		{
			name: "pod not match",
			pod: makePod("pod1",
				map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU: *resource.NewQuantity(2, resource.DecimalSI),
				},
				nil),
			containerResourcesToUpdate: map[string]v1.ResourceRequirements{
				"c2": {
					Requests: map[v1.ResourceName]resource.Quantity{
						v1.ResourceMemory: *resource.NewQuantity(2, resource.DecimalSI),
					},
				},
			},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PodResourceDiff(tc.pod, tc.containerResourcesToUpdate))
		})
	}
}

func TestMultiplyResourceQuantity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		resourceName v1.ResourceName
		quant        resource.Quantity
		factor       float64
		res          resource.Quantity
		want         bool
	}{
		{
			name:         "resource CPU",
			resourceName: v1.ResourceCPU,
			quant:        *resource.NewQuantity(2, resource.DecimalSI),
			factor:       1.5,
			res:          *resource.NewQuantity(3, resource.DecimalSI),
			want:         true,
		},
		{
			name:         "resource memory",
			resourceName: v1.ResourceMemory,
			quant:        resource.MustParse("200Gi"),
			factor:       1.5,
			res:          resource.MustParse("300Gi"),
			want:         true,
		},
		{
			name:         "zero value",
			resourceName: v1.ResourceCPU,
			quant:        *resource.NewQuantity(0, resource.DecimalSI),
			factor:       2,
			res:          *resource.NewQuantity(0, resource.DecimalSI),
			want:         true,
		},
		{
			name:         "zero factor",
			resourceName: v1.ResourceCPU,
			quant:        *resource.NewQuantity(2, resource.DecimalSI),
			factor:       0,
			res:          *resource.NewQuantity(0, resource.DecimalSI),
			want:         true,
		},
		{
			name:         "round down",
			resourceName: v1.ResourceCPU,
			quant:        *resource.NewQuantity(2, resource.DecimalSI),
			factor:       1.23456,
			res:          *resource.NewMilliQuantity(2469, resource.DecimalSI),
			want:         true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			quant := MultiplyResourceQuantity(c.resourceName, c.quant, c.factor)
			assert.Equal(t, c.want, quant.Equal(c.res))
		})
	}
}
