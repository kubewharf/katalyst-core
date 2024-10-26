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

package recommendation

import (
	"context"
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	resourceutils "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/resource"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
)

func TestValidateAndExtractAlgorithmPolicy(t *testing.T) {
	type args struct {
		algorithmPolicyReq v1alpha1.AlgorithmPolicy
	}
	tests := []struct {
		name    string
		args    args
		want    v1alpha1.AlgorithmPolicy
		wantErr *errortypes.CustomError
	}{
		{
			name: "algorithmPolicyReq.Algorithm empty",
			args: args{
				algorithmPolicyReq: v1alpha1.AlgorithmPolicy{
					Recommender: "Recommender",
					Algorithm:   "",
					Extensions: &runtime.RawExtension{
						Raw: []byte("Data"),
					},
				},
			},
			want: v1alpha1.AlgorithmPolicy{
				Recommender: "default",
				Algorithm:   DefaultAlgorithmType,
				Extensions: &runtime.RawExtension{
					Raw: []byte("Data"),
				},
			},
			wantErr: nil,
		},
		{
			name: "algorithmPolicyReq.Algorithm unsupported",
			args: args{
				algorithmPolicyReq: v1alpha1.AlgorithmPolicy{
					Recommender: "Recommender",
					Algorithm:   "mockAlgorithm",
					Extensions: &runtime.RawExtension{
						Raw: []byte("Data"),
					},
				},
			},
			want: v1alpha1.AlgorithmPolicy{
				Recommender: "default",
			},
			wantErr: errortypes.AlgorithmUnsupportedError("mockAlgorithm", AlgorithmTypes),
		},
		{
			name: "algorithmPolicyReq.Algorithm supported",
			args: args{
				algorithmPolicyReq: v1alpha1.AlgorithmPolicy{
					Recommender: "default",
					Algorithm:   PercentileAlgorithmType,
					Extensions: &runtime.RawExtension{
						Raw: []byte("Data"),
					},
				},
			},
			want: v1alpha1.AlgorithmPolicy{
				Recommender: "default",
				Algorithm:   PercentileAlgorithmType,
				Extensions: &runtime.RawExtension{
					Raw: []byte("Data"),
				},
			},
			wantErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErr := ValidateAndExtractAlgorithmPolicy(tt.args.algorithmPolicyReq)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ValidateAndExtractAlgorithmPolicy() got = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(gotErr, tt.wantErr) {
				t.Errorf("ValidateAndExtractAlgorithmPolicy() gotErr = %v, want %v", gotErr, tt.wantErr)
			}
		})
	}
}

func TestValidateAndExtractContainers(t *testing.T) {
	type args struct {
		ctx               context.Context
		dynamicClient     dynamic.Interface
		coreClient        corev1.CoreV1Interface
		namespace         string
		targetRef         v1alpha1.CrossVersionObjectReference
		containerPolicies []v1alpha1.ContainerResourcePolicy
	}
	type env struct {
		podLabels                map[string]string
		podAnnotations           map[string]string
		matchLabelKey            string
		matchLabelValue          string
		podName                  string
		podNodeName              string
		unstructuredName         string
		unstructuredTemplateSpec map[string]interface{}
		namespace                string
		kind                     string
		apiVersion               string
	}
	tests := []struct {
		name    string
		args    args
		env     env
		want    []Container
		wantErr *errortypes.CustomError
	}{
		{
			name: errortypes.WorkloadNotFoundMessage,
			args: args{
				ctx:           context.TODO(),
				dynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
				coreClient:    k8sfake.NewSimpleClientset().CoreV1(),
				namespace:     "mockNamespace",
				targetRef: v1alpha1.CrossVersionObjectReference{
					Kind:       "Kind",
					Name:       "Name",
					APIVersion: "apps/v1",
				},
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "*",
					},
				},
			},
			want:    nil,
			wantErr: errortypes.WorkloadNotFoundError(errortypes.WorkloadNotFoundMessage),
		},
		{
			name: errortypes.WorkloadMatchedErrorMessage,
			args: args{
				ctx:           context.TODO(),
				dynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
				coreClient:    k8sfake.NewSimpleClientset().CoreV1(),
				namespace:     "mockNamespace",
				targetRef: v1alpha1.CrossVersionObjectReference{
					Kind:       "",
					Name:       "",
					APIVersion: "",
				},
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "*",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName: v1.ResourceCPU,
							},
						},
					},
				},
			},
			want:    nil,
			wantErr: errortypes.WorkloadMatchedError(errortypes.WorkloadMatchedErrorMessage),
		},
		{
			name: "all right",
			args: args{
				ctx:           context.TODO(),
				dynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
				coreClient:    k8sfake.NewSimpleClientset().CoreV1(),
				namespace:     "mockNamespace",
				targetRef: v1alpha1.CrossVersionObjectReference{
					Name:       "mockName5",
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "container-1",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName: v1.ResourceCPU,
							},
						},
					},
					{
						ContainerName: "container-2",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName: v1.ResourceCPU,
							},
						},
					},
				},
			},
			env: env{
				podLabels: map[string]string{
					"app": "mockPodLabels5",
				},
				matchLabelKey:    "app",
				matchLabelValue:  "mockPodLabels5",
				podName:          "mockPodName5",
				podNodeName:      "mockPodNodeName5",
				unstructuredName: "mockName5",
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
				namespace:  "mockNamespace",
				kind:       "Deployment",
				apiVersion: "apps/v1",
			},
			want: []Container{
				{
					ContainerName: "container-1",
					ContainerConfigs: []ContainerConfig{
						{
							ControlledResource:    v1.ResourceCPU,
							ResourceBufferPercent: DefaultUsageBuffer,
						},
					},
				},
				{
					ContainerName: "container-2",
					ContainerConfigs: []ContainerConfig{
						{
							ControlledResource:    v1.ResourceCPU,
							ResourceBufferPercent: DefaultUsageBuffer,
						},
					},
				},
			},
			wantErr: nil,
		},
		{
			name: "controlled resources is empty",
			args: args{
				ctx:           context.TODO(),
				dynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
				coreClient:    k8sfake.NewSimpleClientset().CoreV1(),
				namespace:     "mockNamespace",
				targetRef: v1alpha1.CrossVersionObjectReference{
					Name:       "mockName5",
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "*",
					},
				},
			},
			env: env{
				podLabels: map[string]string{
					"app": "mockPodLabels5",
				},
				matchLabelKey:    "app",
				matchLabelValue:  "mockPodLabels5",
				podName:          "mockPodName5",
				podNodeName:      "mockPodNodeName5",
				unstructuredName: "mockName5",
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
				namespace:  "mockNamespace",
				kind:       "Deployment",
				apiVersion: "apps/v1",
			},
			want:    nil,
			wantErr: errortypes.ControlledResourcesPoliciesEmptyError(errortypes.ControlledResourcesPoliciesEmptyMessage, "*"),
		},
		{
			name: errortypes.ContainersMatchedErrorMessage,
			args: args{
				ctx:           context.TODO(),
				dynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
				coreClient:    k8sfake.NewSimpleClientset().CoreV1(),
				namespace:     "mockNamespace",
				targetRef: v1alpha1.CrossVersionObjectReference{
					Name:       "mockName5",
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "container-1",
					},
					{
						ContainerName: "container-2",
					},
				},
			},
			env: env{
				podLabels: map[string]string{
					"app": "mockPodLabels5",
				},
				podAnnotations:   map[string]string{},
				matchLabelKey:    "app",
				matchLabelValue:  "mockPodLabels5",
				podName:          "mockPodName5",
				podNodeName:      "mockPodNodeName5",
				unstructuredName: "mockName5",
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
				namespace:  "mockNamespace",
				kind:       "Deployment",
				apiVersion: "apps/v1",
			},
			want:    nil,
			wantErr: errortypes.ContainersMatchedError(errortypes.ContainersMatchedErrorMessage),
		},
		{
			name: "validate containers err",
			args: args{
				ctx:           context.TODO(),
				dynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
				coreClient:    k8sfake.NewSimpleClientset().CoreV1(),
				namespace:     "mockNamespace",
				targetRef: v1alpha1.CrossVersionObjectReference{
					Name:       "mockName5",
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "container-1",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName: v1.ResourceCPU,
							},
						},
					},
					{
						ContainerName: "container-1",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName: v1.ResourceCPU,
							},
						},
					},
				},
			},
			env: env{
				podLabels: map[string]string{
					"app": "mockPodLabels5",
				},
				podAnnotations:   map[string]string{},
				matchLabelKey:    "app",
				matchLabelValue:  "mockPodLabels5",
				podName:          "mockPodName5",
				podNodeName:      "mockPodNodeName5",
				unstructuredName: "mockName5",
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
				namespace:  "mockNamespace",
				kind:       "Deployment",
				apiVersion: "apps/v1",
			},
			want:    nil,
			wantErr: errortypes.ContainerDuplicateError(errortypes.ContainerDuplicateMessage, "container-1"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matchLabels := map[string]interface{}{
				tt.env.matchLabelKey: tt.env.matchLabelValue,
			}
			if err := resourceutils.CreateMockUnstructured(matchLabels, tt.env.unstructuredTemplateSpec, tt.env.unstructuredName, tt.env.namespace, tt.env.apiVersion, tt.env.kind, tt.args.dynamicClient); err != nil {
				t.Errorf("CreateMockUnstructured() gotErr = %v", err)
			}
			if err := resourceutils.CreateMockPod(tt.env.podLabels, tt.env.podAnnotations, tt.env.podName, tt.env.namespace, tt.env.podNodeName, nil, tt.args.coreClient); err != nil {
				t.Errorf("CreateMockPod() gotErr = %v", err)
			}

			got, gotErr := ValidateAndExtractContainers(tt.args.ctx, tt.args.dynamicClient, tt.args.namespace, tt.args.targetRef, tt.args.containerPolicies, resourceutils.CreateMockRESTMapper())
			SortContainersByContainerName(got)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ValidateAndExtractContainers() got = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(gotErr, tt.wantErr) {
				t.Errorf("ValidateAndExtractContainers() gotErr = %v, wantErr = %v", gotErr, tt.wantErr)
			}
		})
	}
}

func TestValidateAndExtractTargetRef(t *testing.T) {
	type args struct {
		targetRefReq v1alpha1.CrossVersionObjectReference
	}
	tests := []struct {
		name    string
		args    args
		want    v1alpha1.CrossVersionObjectReference
		wantErr *errortypes.CustomError
	}{
		{
			name: "targetRefReq.Name empty",
			args: args{
				targetRefReq: v1alpha1.CrossVersionObjectReference{
					Name: "",
					Kind: "Deployment",
				},
			},
			want:    v1alpha1.CrossVersionObjectReference{},
			wantErr: errortypes.WorkloadNameIsEmptyError(),
		},
		{
			name: "targetRefReq.Kind unsupported",
			args: args{
				targetRefReq: v1alpha1.CrossVersionObjectReference{
					Name: "test",
					Kind: "ReplicaSet",
				},
			},
			want:    v1alpha1.CrossVersionObjectReference{},
			wantErr: errortypes.WorkloadsUnsupportedError("ReplicaSet", TargetRefKinds),
		},
		{
			name: "all right",
			args: args{
				targetRefReq: v1alpha1.CrossVersionObjectReference{
					Name:       "test1",
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
			},
			want: v1alpha1.CrossVersionObjectReference{
				Name:       "test1",
				Kind:       "Deployment",
				APIVersion: "apps/v1",
			},
			wantErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErr := ValidateAndExtractTargetRef(tt.args.targetRefReq)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ValidateAndExtractTargetRef() got = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(gotErr, tt.wantErr) {
				t.Errorf("ValidateAndExtractTargetRef() gotErr = %v, want %v", gotErr, tt.wantErr)
			}
		})
	}
}

func ptrInt32(i int32) *int32 {
	return &i
}

func Test_validateAndExtractContainers(t *testing.T) {
	errControlledValues := v1alpha1.ContainerControlledValues("errControlledValues")
	type args struct {
		containerPolicies  []v1alpha1.ContainerResourcePolicy
		existContainerList []string
	}
	tests := []struct {
		name    string
		args    args
		want    []Container
		wantErr *errortypes.CustomError
	}{
		{
			name: errortypes.ContainerDuplicateMessage,
			args: args{
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "mockContainerName-1",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName:  v1.ResourceCPU,
								BufferPercent: ptrInt32(10),
							},
							{
								ResourceName:  v1.ResourceMemory,
								BufferPercent: ptrInt32(10),
							},
						},
					},
					{
						ContainerName: "mockContainerName-1",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName:  v1.ResourceCPU,
								BufferPercent: ptrInt32(10),
							},
							{
								ResourceName:  v1.ResourceMemory,
								BufferPercent: ptrInt32(10),
							},
						},
					},
				},
				existContainerList: []string{"mockContainerName-1"},
			},
			want:    nil,
			wantErr: errortypes.ContainerDuplicateError(errortypes.ContainerDuplicateMessage, "mockContainerName-1"),
		},
		{
			name: errortypes.ContainerNameEmptyMessage,
			args: args{
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "mockContainerName-1",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName:  v1.ResourceCPU,
								BufferPercent: ptrInt32(10),
							},
							{
								ResourceName:  v1.ResourceMemory,
								BufferPercent: ptrInt32(10),
							},
						},
					},
					{
						ContainerName: "",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName:  v1.ResourceCPU,
								BufferPercent: ptrInt32(10),
							},
							{
								ResourceName:  v1.ResourceMemory,
								BufferPercent: ptrInt32(10),
							},
						},
					},
				},
				existContainerList: []string{"mockContainerName-1"},
			},
			want:    nil,
			wantErr: errortypes.ContainerNameEmptyError(errortypes.ContainerNameEmptyMessage),
		},
		{
			name: errortypes.ContainerDuplicateMessage,
			args: args{
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "mockContainerName-1",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName:  v1.ResourceCPU,
								BufferPercent: ptrInt32(10),
							},
							{
								ResourceName:  v1.ResourceMemory,
								BufferPercent: ptrInt32(10),
							},
						},
					},
					{
						ContainerName: "mockContainerName-2",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName:  v1.ResourceCPU,
								BufferPercent: ptrInt32(10),
							},
							{
								ResourceName:  v1.ResourceMemory,
								BufferPercent: ptrInt32(10),
							},
						},
					},
				},
				existContainerList: []string{"mockContainerName-1"},
			},
			want:    nil,
			wantErr: errortypes.ContainersNotFoundError(errortypes.ContainerNotFoundMessage, "mockContainerName-2"),
		},
		{
			name: "Container name is only *",
			args: args{
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "*",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName:  v1.ResourceCPU,
								BufferPercent: ptrInt32(10),
							},
							{
								ResourceName:  v1.ResourceMemory,
								BufferPercent: ptrInt32(20),
							},
						},
					},
				},
				existContainerList: []string{"mockContainerName-1", "mockContainerName-2"},
			},
			want: []Container{
				{
					ContainerName: "mockContainerName-1",
					ContainerConfigs: []ContainerConfig{
						{
							ControlledResource:    v1.ResourceCPU,
							ResourceBufferPercent: 10,
						},
						{
							ControlledResource:    v1.ResourceMemory,
							ResourceBufferPercent: 20,
						},
					},
				},
				{
					ContainerName: "mockContainerName-2",
					ContainerConfigs: []ContainerConfig{
						{
							ControlledResource:    v1.ResourceCPU,
							ResourceBufferPercent: 10,
						},
						{
							ControlledResource:    v1.ResourceMemory,
							ResourceBufferPercent: 20,
						},
					},
				},
			},
			wantErr: nil,
		},
		{
			name: "Container name list includes *",
			args: args{
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "*",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName:  v1.ResourceCPU,
								BufferPercent: ptrInt32(20),
							},
							{
								ResourceName:  v1.ResourceMemory,
								BufferPercent: ptrInt32(20),
							},
						},
					},
					{
						ContainerName: "mockContainerName-1",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName:  v1.ResourceCPU,
								BufferPercent: ptrInt32(10),
							},
							{
								ResourceName:  v1.ResourceMemory,
								BufferPercent: ptrInt32(10),
							},
						},
					},
				},
				existContainerList: []string{"mockContainerName-1", "mockContainerName-2", "mockContainerName-3"},
			},
			want: []Container{
				{
					ContainerName: "mockContainerName-1",
					ContainerConfigs: []ContainerConfig{
						{
							ControlledResource:    v1.ResourceCPU,
							ResourceBufferPercent: 10,
						},
						{
							ControlledResource:    v1.ResourceMemory,
							ResourceBufferPercent: 10,
						},
					},
				},
				{
					ContainerName: "mockContainerName-2",
					ContainerConfigs: []ContainerConfig{
						{
							ControlledResource:    v1.ResourceCPU,
							ResourceBufferPercent: 20,
						},
						{
							ControlledResource:    v1.ResourceMemory,
							ResourceBufferPercent: 20,
						},
					},
				},
				{
					ContainerName: "mockContainerName-3",
					ContainerConfigs: []ContainerConfig{
						{
							ControlledResource:    v1.ResourceCPU,
							ResourceBufferPercent: 20,
						},
						{
							ControlledResource:    v1.ResourceMemory,
							ResourceBufferPercent: 20,
						},
					},
				},
			},
			wantErr: nil,
		},
		{
			name: errortypes.ResourceNameUnsupportedMessage,
			args: args{
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "*",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName:  v1.ResourceCPU,
								BufferPercent: ptrInt32(10),
							},
							{
								ResourceName:  v1.ResourceMemory,
								BufferPercent: ptrInt32(20),
							},
							{
								ResourceName:  "errResource",
								BufferPercent: ptrInt32(20),
							},
						},
					},
				},
				existContainerList: []string{"mockContainerName-1", "mockContainerName-2"},
			},
			want:    []Container{},
			wantErr: errortypes.ResourceNameUnsupportedError(errortypes.ResourceNameUnsupportedMessage, ResourceNames),
		},
		{
			name: errortypes.ResourceBuffersUnsupportedMessage,
			args: args{
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "*",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName:  v1.ResourceCPU,
								BufferPercent: ptrInt32(101),
							},
							{
								ResourceName:  v1.ResourceMemory,
								BufferPercent: ptrInt32(20),
							},
						},
					},
				},
				existContainerList: []string{"mockContainerName-1", "mockContainerName-2"},
			},
			want:    []Container{},
			wantErr: errortypes.ResourceBuffersUnsupportedError(errortypes.ResourceBuffersUnsupportedMessage),
		},
		{
			name: errortypes.ResourceBuffersUnsupportedMessage,
			args: args{
				containerPolicies: []v1alpha1.ContainerResourcePolicy{
					{
						ContainerName: "*",
						ControlledResourcesPolicies: []v1alpha1.ContainerControlledResourcesPolicy{
							{
								ResourceName:     v1.ResourceCPU,
								BufferPercent:    ptrInt32(101),
								ControlledValues: &errControlledValues,
							},
							{
								ResourceName:  v1.ResourceMemory,
								BufferPercent: ptrInt32(20),
							},
						},
					},
				},
				existContainerList: []string{"mockContainerName-1", "mockContainerName-2"},
			},
			want:    []Container{},
			wantErr: errortypes.ControlledValuesUnsupportedError(errortypes.ResourceNameUnsupportedMessage, SupportControlledValues),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErr := validateAndExtractContainers(tt.args.containerPolicies, tt.args.existContainerList)
			SortContainersByContainerName(got)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("validateAndExtractContainers() got = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(gotErr, tt.wantErr) {
				t.Errorf("validateAndExtractContainers() gotErr = %v, want %v", gotErr, tt.wantErr)
			}
		})
	}
}
