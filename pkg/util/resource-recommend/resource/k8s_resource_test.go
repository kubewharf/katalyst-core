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

package resource

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
)

func TestConvertAndGetResource(t *testing.T) {
	type args struct {
		ctx       context.Context
		client    dynamic.Interface
		namespace string
		targetRef v1alpha1.CrossVersionObjectReference
	}
	type env struct {
		namespace  string
		kind       string
		name       string
		apiVersion string
	}
	tests := []struct {
		name    string
		args    args
		env     env
		want    *unstructured.Unstructured
		wantErr bool
	}{
		{
			name: "Get resource failed",
			args: args{
				ctx:       context.TODO(),
				client:    dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
				namespace: "fakeNamespace",
				targetRef: v1alpha1.CrossVersionObjectReference{
					Kind:       "Deployment",
					Name:       "mockResourceName",
					APIVersion: "apps/v1",
				},
			},
			env: env{
				namespace:  "fakeNamespace",
				kind:       "Deployment",
				name:       "mockResourceName1",
				apiVersion: "apps/v1",
			},
			wantErr: true,
		},
		{
			name: "Get resource failed",
			args: args{
				ctx:       context.TODO(),
				client:    dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
				namespace: "fakeNamespace",
				targetRef: v1alpha1.CrossVersionObjectReference{
					Kind:       "Deployment",
					Name:       "mockResourceName",
					APIVersion: "apps/v1",
				},
			},
			env: env{
				namespace:  "fakeNamespace",
				kind:       "Deployment",
				name:       "mockResourceName",
				apiVersion: "apps/v1",
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CreateMockUnstructured(nil, nil, tt.env.name, tt.env.namespace, tt.env.apiVersion, tt.env.kind, tt.args.client); err != nil {
				t.Errorf("CreateMockUnstructured() gotErr = %v", err)
			}
			_, err := ConvertAndGetResource(tt.args.ctx, tt.args.client, tt.args.namespace, tt.args.targetRef, CreateMockRESTMapper())
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertAndGetResource() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestGetAllClaimedContainers(t *testing.T) {
	type args struct {
		controller *unstructured.Unstructured
	}
	type env struct {
		needCreate               bool
		unstructuredTemplateSpec map[string]interface{}
	}
	tests := []struct {
		name    string
		args    args
		env     env
		want    []string
		wantErr bool
	}{
		{
			name: "spec.template.spec not found",
			args: args{
				controller: &unstructured.Unstructured{},
			},
			env: env{
				needCreate:               false,
				unstructuredTemplateSpec: map[string]interface{}{},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "nestedMap failed",
			args: args{
				controller: &unstructured.Unstructured{},
			},
			env: env{
				needCreate: true,
				unstructuredTemplateSpec: map[string]interface{}{
					"container": []interface{}{
						map[string]interface{}{
							"name":  "container-1",
							"image": "image:latest",
						},
						map[string]interface{}{
							"name":  "container-2",
							"image": "image:latest",
						},
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "container name not found",
			args: args{
				controller: &unstructured.Unstructured{},
			},
			env: env{
				needCreate: true,
				unstructuredTemplateSpec: map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"image": "image:latest",
						},
						map[string]interface{}{
							"image": "image:latest",
						},
					},
				},
			},
			want:    []string{},
			wantErr: false,
		},
		{
			name: "all right",
			args: args{
				controller: &unstructured.Unstructured{},
			},
			env: env{
				needCreate: true,
				unstructuredTemplateSpec: map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"name":  "container-1",
							"image": "image:latest",
						},
						map[string]interface{}{
							"name":  "container-2",
							"image": "image:latest",
						},
					},
				},
			},
			want:    []string{"container-1", "container-2"},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env.needCreate {
				tt.args.controller.SetName("mockName")
				unstructured.SetNestedMap(tt.args.controller.Object, tt.env.unstructuredTemplateSpec, "spec", "template", "spec")
			}
			got, err := GetAllClaimedContainers(tt.args.controller)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetAllClaimedContainers() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetAllClaimedContainers() got = %v, want %v", got, tt.want)
			}
		})
	}
}
