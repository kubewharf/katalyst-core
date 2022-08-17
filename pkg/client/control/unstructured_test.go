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

package control

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func toTestUnstructured(t *testing.T, obj interface{}) *unstructured.Unstructured {
	ret, err := native.ToUnstructured(obj)
	assert.NoError(t, err)
	return ret
}

func Test_prepareUnstructuredPatchBytes(t *testing.T) {
	type args struct {
		oldObj *unstructured.Unstructured
		newObj *unstructured.Unstructured
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "label patch bytes",
			args: args{
				oldObj: toTestUnstructured(t, &corev1.Node{
					ObjectMeta: v1.ObjectMeta{
						Labels: map[string]string{
							"aa": "bb",
						},
					},
				}),
				newObj: toTestUnstructured(t, &corev1.Node{
					ObjectMeta: v1.ObjectMeta{
						Labels: map[string]string{
							"aa": "bb",
							"cc": "dd",
						},
					},
				}),
			},
			want: "{\"metadata\":{\"labels\":{\"cc\":\"dd\"}}}",
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				return true
			},
		},
		{
			name: "spec patch bytes",
			args: args{
				oldObj: toTestUnstructured(t, &corev1.Node{
					Spec: corev1.NodeSpec{
						PodCIDR: "aa",
					},
				}),
				newObj: toTestUnstructured(t, &corev1.Node{
					Spec: corev1.NodeSpec{
						PodCIDR: "bb",
					},
				}),
			},
			want: "{\"spec\":{\"podCIDR\":\"bb\"}}",
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := prepareUnstructuredPatchBytes(tt.args.oldObj, tt.args.newObj)
			if !tt.wantErr(t, err, fmt.Sprintf("prepareUnstructuredPatchBytes(%v, %v)", tt.args.oldObj, tt.args.newObj)) {
				return
			}
			assert.Equalf(t, tt.want, string(got), "prepareUnstructuredPatchBytes(%v, %v)", tt.args.oldObj, tt.args.newObj)
		})
	}
}

func Test_prepareUnstructuredStatusPatchBytes(t *testing.T) {
	type args struct {
		oldObj *unstructured.Unstructured
		newObj *unstructured.Unstructured
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "label patch bytes",
			args: args{
				oldObj: toTestUnstructured(t, &corev1.Node{
					Status: corev1.NodeStatus{
						Allocatable: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					},
				}),
				newObj: toTestUnstructured(t, &corev1.Node{
					Status: corev1.NodeStatus{
						Allocatable: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("20"),
						},
					},
				}),
			},
			want: "{\"status\":{\"allocatable\":{\"cpu\":\"20\"}}}",
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := prepareUnstructuredStatusPatchBytes(tt.args.oldObj, tt.args.newObj)
			if !tt.wantErr(t, err, fmt.Sprintf("prepareUnstructuredStatusPatchBytes(%v, %v)", tt.args.oldObj, tt.args.newObj)) {
				return
			}
			assert.Equalf(t, tt.want, string(got), "prepareUnstructuredStatusPatchBytes(%v, %v)", tt.args.oldObj, tt.args.newObj)
		})
	}
}
