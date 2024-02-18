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

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
)

func TestConvertAndGetResource(t *testing.T) {
	type args struct {
		ctx       context.Context
		client    client.Client
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
				client:    fake.NewClientBuilder().Build(),
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
				client:    fake.NewClientBuilder().Build(),
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
			CreateMockUnstructured(nil, nil, tt.env.name, tt.env.namespace, tt.env.apiVersion, tt.env.kind, tt.args.client)
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

func TestPatchUpdateResourceRecommend(t *testing.T) {
	type args struct {
		client            client.Client
		namespaceName     types.NamespacedName
		resourceRecommend *v1alpha1.ResourceRecommend
	}
	tests := []struct {
		name                  string
		args                  args
		wantErr               bool
		wantResourceRecommend *v1alpha1.ResourceRecommend
	}{
		{
			name: "all right 1",
			args: args{
				client: fake.NewClientBuilder().Build(),
				namespaceName: types.NamespacedName{
					Name:      "mockName",
					Namespace: "mockNamespace",
				},
				resourceRecommend: &v1alpha1.ResourceRecommend{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ResourceRecommend",
						APIVersion: "autoscaling.katalyst.kubewharf.io/v1alpha1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mockName",
						Namespace: "mockNamespace",
					},
					Status: v1alpha1.ResourceRecommendStatus{},
				},
			},
			wantResourceRecommend: &v1alpha1.ResourceRecommend{
				Status: v1alpha1.ResourceRecommendStatus{
					RecommendResources: &v1alpha1.RecommendResources{
						ContainerRecommendations: []v1alpha1.ContainerResources{
							{
								ContainerName: "ContainerName1",
								Requests:      &v1alpha1.ContainerResourceList{},
							},
							{
								ContainerName: "ContainerName2",
								Requests:      &v1alpha1.ContainerResourceList{},
							},
							{
								ContainerName: "ContainerName3",
								Requests:      &v1alpha1.ContainerResourceList{},
							},
							{
								ContainerName: "ContainerName4",
								Requests:      &v1alpha1.ContainerResourceList{},
							},
						},
					},
					Conditions: []v1alpha1.ResourceRecommendCondition{
						{
							Type:    v1alpha1.Validated,
							Reason:  "reason1",
							Status:  v1.ConditionTrue,
							Message: "Message",
						},
						{
							Type:    v1alpha1.Validated,
							Reason:  "reason2",
							Status:  v1.ConditionTrue,
							Message: "Message",
						},
						{
							Type:    v1alpha1.Validated,
							Reason:  "reason3",
							Status:  v1.ConditionTrue,
							Message: "Message",
						},
						{
							Type:    v1alpha1.Validated,
							Reason:  "reason4",
							Status:  v1.ConditionTrue,
							Message: "Message",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "all right 2",
			args: args{
				client: fake.NewClientBuilder().Build(),
				namespaceName: types.NamespacedName{
					Name:      "mockName",
					Namespace: "mockNamespace",
				},
				resourceRecommend: &v1alpha1.ResourceRecommend{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ResourceRecommend",
						APIVersion: "autoscaling.katalyst.kubewharf.io/v1alpha1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mockName",
						Namespace: "mockNamespace",
					},
					Status: v1alpha1.ResourceRecommendStatus{
						RecommendResources: &v1alpha1.RecommendResources{
							ContainerRecommendations: []v1alpha1.ContainerResources{
								{
									ContainerName: "ContainerName0",
								},
								{
									ContainerName: "ContainerName1",
								},
								{
									ContainerName: "ContainerName2",
								},
								{
									ContainerName: "ContainerName3",
								},
							},
						},
						Conditions: []v1alpha1.ResourceRecommendCondition{
							{
								Type:    v1alpha1.Validated,
								Reason:  "reason0",
								Status:  v1.ConditionTrue,
								Message: "Message",
							},
							{
								Type:    v1alpha1.Validated,
								Reason:  "reason1",
								Status:  v1.ConditionTrue,
								Message: "Message",
							},
							{
								Type:    v1alpha1.Validated,
								Reason:  "reason2",
								Status:  v1.ConditionTrue,
								Message: "Message",
							},
							{
								Type:    v1alpha1.Validated,
								Reason:  "reason3",
								Status:  v1.ConditionTrue,
								Message: "Message",
							},
						},
					},
				},
			},
			wantResourceRecommend: &v1alpha1.ResourceRecommend{
				Status: v1alpha1.ResourceRecommendStatus{
					RecommendResources: &v1alpha1.RecommendResources{
						ContainerRecommendations: []v1alpha1.ContainerResources{
							{
								ContainerName: "ContainerName1",
								Requests:      &v1alpha1.ContainerResourceList{},
							},
							{
								ContainerName: "ContainerName2",
								Requests:      &v1alpha1.ContainerResourceList{},
							},
							{
								ContainerName: "ContainerName3",
								Requests:      &v1alpha1.ContainerResourceList{},
							},
							{
								ContainerName: "ContainerName4",
								Requests:      &v1alpha1.ContainerResourceList{},
							},
						},
					},
					Conditions: []v1alpha1.ResourceRecommendCondition{
						{
							Type:    v1alpha1.Validated,
							Reason:  "reason1",
							Status:  v1.ConditionTrue,
							Message: "Message",
						},
						{
							Type:    v1alpha1.Validated,
							Reason:  "reason2",
							Status:  v1.ConditionTrue,
							Message: "Message",
						},
						{
							Type:    v1alpha1.Validated,
							Reason:  "reason3",
							Status:  v1.ConditionTrue,
							Message: "Message",
						},
						{
							Type:    v1alpha1.Validated,
							Reason:  "reason4",
							Status:  v1.ConditionTrue,
							Message: "Message",
						},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v1alpha1.AddToScheme(tt.args.client.Scheme())
			tt.args.client.Create(context.TODO(), tt.args.resourceRecommend)
			if err := PatchUpdateResourceRecommend(tt.args.client, tt.args.namespaceName, tt.args.resourceRecommend); (err != nil) != tt.wantErr {
				t.Errorf("PatchUpdateResourceRecommend() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err := PatchUpdateResourceRecommend(tt.args.client, tt.args.namespaceName, tt.wantResourceRecommend); (err != nil) != tt.wantErr {
				t.Errorf("PatchUpdateResourceRecommend() error = %v, wantErr %v", err, tt.wantErr)
			}
			gotResourceRecommend := &v1alpha1.ResourceRecommend{}
			tt.args.client.Get(context.TODO(), client.ObjectKey{
				Name:      tt.args.namespaceName.Name,
				Namespace: tt.args.namespaceName.Namespace,
			}, gotResourceRecommend)
			if !reflect.DeepEqual(gotResourceRecommend.Status, tt.wantResourceRecommend.Status) {
				t.Errorf("PatchUpdateResourceRecommend() gotResourceRecommend.Status = %v, wantResourceRecommend.Status = %v",
					gotResourceRecommend.Status, tt.wantResourceRecommend.Status)
			}
		})
	}
}
