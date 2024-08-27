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
	apiappsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	appsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
)

func TestConvertAndGetResource(t *testing.T) {
	type args struct {
		ctx       context.Context
		client    appsv1.AppsV1Interface
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
				client:    fake.NewSimpleClientset().AppsV1(),
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
				client:    fake.NewSimpleClientset().AppsV1(),
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
			if err := CreateMockDeployment(nil, nil, tt.env.name, tt.env.namespace, tt.env.apiVersion, tt.env.kind, tt.args.client); err != nil {
				t.Errorf("CreateMockDeployment() gotErr = %v", err)
			}
			_, err := ConvertAndGetResource(tt.args.ctx, tt.args.client, tt.args.namespace, tt.args.targetRef)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertAndGetResource() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestGetAllClaimedContainers(t *testing.T) {
	type args struct {
		deployment *v1.Deployment
	}
	type env struct {
		needCreate bool
		containers []corev1.Container
	}
	tests := []struct {
		name    string
		args    args
		env     env
		want    []string
		wantErr bool
	}{
		{
			name: "deployment is nil",
			args: args{
				deployment: nil,
			},
			env: env{
				needCreate: false,
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "all right",
			args: args{
				deployment: &v1.Deployment{},
			},
			env: env{
				needCreate: true,
				containers: []corev1.Container{
					{
						Name:  "container-1",
						Image: "image:latest",
					},
					{
						Name:  "container-2",
						Image: "image:latest",
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
				tt.args.deployment.SetName("mockName")
				tt.args.deployment.Spec = apiappsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: tt.env.containers,
						},
					},
				}
			}
			got, err := GetAllClaimedContainers(tt.args.deployment)
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
