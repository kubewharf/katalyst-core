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
	"time"

	"bou.ke/monkey"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	conditionstypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/conditions"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
)

func TestRecommendation_AsStatus(t *testing.T) {
	fakeTime1 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	fakeMetaTime1 := metav1.NewTime(fakeTime1)
	tests := []struct {
		name           string
		recommendation *Recommendation
		want           v1alpha1.ResourceRecommendStatus
	}{
		{
			name: "notRecommend",
			recommendation: &Recommendation{
				Conditions: &conditionstypes.ResourceRecommendConditionsMap{
					v1alpha1.Validated: {
						Type:               v1alpha1.Validated,
						Status:             v1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(time.Date(2023, 3, 3, 3, 0, 0, 0, time.UTC)),
					},
					v1alpha1.Initialized: {
						Type:               v1alpha1.Initialized,
						Status:             v1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Date(2023, 4, 4, 4, 0, 0, 0, time.UTC)),
						Reason:             "reason4",
						Message:            "test msg4",
					},
				},
			},
			want: v1alpha1.ResourceRecommendStatus{
				Conditions: []v1alpha1.ResourceRecommendCondition{
					{
						Type:               v1alpha1.Initialized,
						Status:             v1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Date(2023, 4, 4, 4, 0, 0, 0, time.UTC)),
						Reason:             "reason4",
						Message:            "test msg4",
					},
					{
						Type:               v1alpha1.Validated,
						Status:             v1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(time.Date(2023, 3, 3, 3, 0, 0, 0, time.UTC)),
					},
				},
			},
		},
		{
			name: "recommended",
			recommendation: &Recommendation{
				Conditions: &conditionstypes.ResourceRecommendConditionsMap{
					v1alpha1.Validated: {
						Type:               v1alpha1.Validated,
						Status:             v1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(time.Date(2023, 3, 3, 3, 0, 0, 0, time.UTC)),
					},
					v1alpha1.Initialized: {
						Type:               v1alpha1.Initialized,
						Status:             v1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Date(2023, 4, 4, 4, 0, 0, 0, time.UTC)),
						Reason:             "reason4",
						Message:            "test msg4",
					},
				},
				Recommendations: []v1alpha1.ContainerResources{
					{
						ContainerName: "c1",
					},
				},
			},
			want: v1alpha1.ResourceRecommendStatus{
				Conditions: []v1alpha1.ResourceRecommendCondition{
					{
						Type:               v1alpha1.Initialized,
						Status:             v1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Date(2023, 4, 4, 4, 0, 0, 0, time.UTC)),
						Reason:             "reason4",
						Message:            "test msg4",
					},
					{
						Type:               v1alpha1.Validated,
						Status:             v1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(time.Date(2023, 3, 3, 3, 0, 0, 0, time.UTC)),
					},
				},
				LastRecommendationTime: &fakeMetaTime1,
				RecommendResources: &v1alpha1.RecommendResources{
					ContainerRecommendations: []v1alpha1.ContainerResources{
						{
							ContainerName: "c1",
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer monkey.UnpatchAll()

			monkey.Patch(time.Now, func() time.Time { return fakeTime1 })

			if got := tt.recommendation.AsStatus(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AsStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecommendation_SetConfig(t *testing.T) {
	type args struct {
		targetRef       v1alpha1.CrossVersionObjectReference
		customErr1      *errortypes.CustomError
		algorithmPolicy v1alpha1.AlgorithmPolicy
		customErr2      *errortypes.CustomError
		containers      []Container
		customErr3      *errortypes.CustomError
	}
	tests := []struct {
		name    string
		args    args
		wantErr *errortypes.CustomError
	}{
		{
			name: "targetRef_Validate_err",
			args: args{
				customErr1: &errortypes.CustomError{
					Phase:   errortypes.Validated,
					Code:    errortypes.WorkloadNameIsEmpty,
					Message: "err_msg1",
				},
			},
			wantErr: &errortypes.CustomError{
				Phase:   errortypes.Validated,
				Code:    errortypes.WorkloadNameIsEmpty,
				Message: "err_msg1",
			},
		},
		{
			name: "targetRef_Validate_err",
			args: args{
				customErr2: &errortypes.CustomError{
					Phase:   errortypes.Validated,
					Code:    errortypes.AlgorithmUnsupported,
					Message: "err_msg1",
				},
			},
			wantErr: &errortypes.CustomError{
				Phase:   errortypes.Validated,
				Code:    errortypes.AlgorithmUnsupported,
				Message: "err_msg1",
			},
		},
		{
			name: "targetRef_Validate_err",
			args: args{
				customErr3: &errortypes.CustomError{
					Phase:   errortypes.Validated,
					Code:    errortypes.WorkloadNotFound,
					Message: "err_msg1",
				},
			},
			wantErr: &errortypes.CustomError{
				Phase:   errortypes.Validated,
				Code:    errortypes.WorkloadNotFound,
				Message: "err_msg1",
			},
		},
		{
			name: "paas",
			args: args{
				targetRef: v1alpha1.CrossVersionObjectReference{
					Kind: "Deployment",
					Name: "demo",
				},
				algorithmPolicy: v1alpha1.AlgorithmPolicy{
					Recommender: "default",
				},
				containers: []Container{
					{
						ContainerName: "c1",
					},
				},
			},
			wantErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer monkey.UnpatchAll()

			monkey.Patch(ValidateAndExtractTargetRef, func(targetRefReq v1alpha1.CrossVersionObjectReference) (
				v1alpha1.CrossVersionObjectReference, *errortypes.CustomError,
			) {
				return tt.args.targetRef, tt.args.customErr1
			})
			monkey.Patch(ValidateAndExtractAlgorithmPolicy, func(algorithmPolicyReq v1alpha1.AlgorithmPolicy) (
				v1alpha1.AlgorithmPolicy, *errortypes.CustomError,
			) {
				return tt.args.algorithmPolicy, tt.args.customErr2
			})
			monkey.Patch(ValidateAndExtractContainers, func(ctx context.Context, client k8sclient.Client, namespace string,
				targetRef v1alpha1.CrossVersionObjectReference,
				containerPolicies []v1alpha1.ContainerResourcePolicy,
			) ([]Container, *errortypes.CustomError) {
				return tt.args.containers, tt.args.customErr3
			})

			r := NewRecommendation(&v1alpha1.ResourceRecommend{})
			if gotErr := r.SetConfig(context.Background(), fake.NewClientBuilder().Build(), &v1alpha1.ResourceRecommend{}); !reflect.DeepEqual(gotErr, tt.wantErr) {
				t.Errorf("SetConfig() = %v, want %v", gotErr, tt.wantErr)
			}
			if tt.wantErr == nil {
				config := Config{
					TargetRef:       tt.args.targetRef,
					AlgorithmPolicy: tt.args.algorithmPolicy,
					Containers:      tt.args.containers,
				}
				if !reflect.DeepEqual(config, r.Config) {
					t.Errorf("SetConfig() failed, want config: %v, got: %v", config, r.Config)
				}
			}
		})
	}
}
