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
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"bou.ke/monkey"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	processormanager "github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/manager"
	resourceutils "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/resource"
	conditionstypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/conditions"
	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
	"github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
	recommendationtypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/recommendation"
)

func TestResourceRecommendController_UpdateRecommendationStatus(t *testing.T) {
	type args struct {
		namespaceName  types.NamespacedName
		recommendation *recommendationtypes.Recommendation
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "test_ObservedGeneration",
			args: args{
				recommendation: &recommendationtypes.Recommendation{
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
					ObservedGeneration: 543123451423,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer monkey.UnpatchAll()

			monkey.Patch(resourceutils.PatchUpdateResourceRecommend, func(client k8sclient.Client, namespaceName types.NamespacedName,
				resourceRecommend *v1alpha1.ResourceRecommend,
			) error {
				if resourceRecommend.Status.ObservedGeneration != tt.args.recommendation.ObservedGeneration {
					return errors.New("ObservedGeneration not update")
				}
				return nil
			})

			r := &ResourceRecommendController{}
			if err := r.UpdateRecommendationStatus(tt.args.namespaceName, tt.args.recommendation); (err != nil) != tt.wantErr {
				t.Errorf("UpdateRecommendationStatus() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

var gotProcessConfigList []processor.ProcessConfig

type mockProcessor struct {
	algorithm v1alpha1.Algorithm
	runMark   string
}

func (p *mockProcessor) Run(_ context.Context) { return }

func (p *mockProcessor) Register(processConfig *processor.ProcessConfig) *errortypes.CustomError {
	gotProcessConfigList = append(gotProcessConfigList, *processConfig)
	return nil
}

func (p *mockProcessor) Cancel(_ *processor.ProcessKey) *errortypes.CustomError { return nil }

func (p *mockProcessor) QueryProcessedValues(_ *processor.ProcessKey) (float64, error) { return 0, nil }

func TestResourceRecommendController_RegisterTasks_AssignmentTest(t *testing.T) {
	recommendation := recommendationtypes.Recommendation{
		NamespacedName: types.NamespacedName{
			Name:      "rec1",
			Namespace: "default",
		},
		Config: recommendationtypes.Config{
			TargetRef: v1alpha1.CrossVersionObjectReference{
				Name:       "demo",
				Kind:       "deployment",
				APIVersion: "app/v1",
			},
			Containers: []recommendationtypes.Container{
				{
					ContainerName: "c1",
					ContainerConfigs: []recommendationtypes.ContainerConfig{
						{
							ControlledResource:    v1.ResourceCPU,
							ResourceBufferPercent: 34,
						},
						{
							ControlledResource:    v1.ResourceMemory,
							ResourceBufferPercent: 43,
						},
					},
				},
				{
					ContainerName: "c2",
					ContainerConfigs: []recommendationtypes.ContainerConfig{
						{
							ControlledResource:    v1.ResourceMemory,
							ResourceBufferPercent: 53,
						},
					},
				},
			},
		},
	}

	wantProcessConfigList := []processor.ProcessConfig{
		{
			ProcessKey: processor.ProcessKey{
				ResourceRecommendNamespacedName: types.NamespacedName{
					Name:      "rec1",
					Namespace: "default",
				},
				Metric: &datasourcetypes.Metric{
					Namespace:     "default",
					WorkloadName:  "demo",
					Kind:          "deployment",
					APIVersion:    "app/v1",
					ContainerName: "c1",
					Resource:      v1.ResourceCPU,
				},
			},
		},
		{
			ProcessKey: processor.ProcessKey{
				ResourceRecommendNamespacedName: types.NamespacedName{
					Name:      "rec1",
					Namespace: "default",
				},
				Metric: &datasourcetypes.Metric{
					Namespace:     "default",
					WorkloadName:  "demo",
					Kind:          "deployment",
					APIVersion:    "app/v1",
					ContainerName: "c1",
					Resource:      v1.ResourceMemory,
				},
			},
		},
		{
			ProcessKey: processor.ProcessKey{
				ResourceRecommendNamespacedName: types.NamespacedName{
					Name:      "rec1",
					Namespace: "default",
				},
				Metric: &datasourcetypes.Metric{
					Namespace:     "default",
					WorkloadName:  "demo",
					Kind:          "deployment",
					APIVersion:    "app/v1",
					ContainerName: "c2",
					Resource:      v1.ResourceMemory,
				},
			},
		},
	}

	t.Run("AssignmentTest", func(t *testing.T) {
		defer monkey.UnpatchAll()

		processor1 := &mockProcessor{}
		manager := &processormanager.Manager{}
		manager.ProcessorRegister(v1alpha1.AlgorithmPercentile, processor1)
		r := &ResourceRecommendController{
			ProcessorManager: manager,
		}

		if err := r.RegisterTasks(recommendation); err != nil {
			t.Errorf("RegisterTasks Assignment failed")
		}

		got, err1 := json.Marshal(gotProcessConfigList)
		if err1 != nil {
			t.Errorf("RegisterTasks Assignment got json.Marshal failed")
		}
		want, err2 := json.Marshal(wantProcessConfigList)
		if err2 != nil {
			t.Errorf("RegisterTasks Assignment want json.Marshal failed")
		}

		if string(got) != string(want) {
			t.Errorf("RegisterTasks Assignment processConfig failed, got: %s, want: %s", string(got), string(want))
		}
	})
}
