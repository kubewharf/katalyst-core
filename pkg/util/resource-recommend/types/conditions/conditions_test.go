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

package conditions

import (
	"reflect"
	"testing"
	"time"

	"github.com/bytedance/mockey"
	"github.com/smartystreets/goconvey/convey"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
)

func TestResourceRecommendConditionsMap_Set(t *testing.T) {
	case1ConditionsMap := NewResourceRecommendConditionsMap()
	fakeTime1 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	case1WantConditionsMap := ResourceRecommendConditionsMap{
		v1alpha1.Validated: v1alpha1.ResourceRecommendCondition{
			Type:               v1alpha1.Validated,
			Status:             v1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(fakeTime1),
			Reason:             "reason1",
			Message:            "test msg1",
		},
	}

	case2ConditionsMap := ResourceRecommendConditionsMap{
		v1alpha1.Validated: v1alpha1.ResourceRecommendCondition{
			Type:               v1alpha1.Validated,
			Status:             v1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)),
			Reason:             "reason1",
			Message:            "test msg1",
		},
	}
	fakeTime2 := time.Date(2023, 2, 2, 2, 0, 0, 0, time.UTC)
	case2WantConditionsMap := ResourceRecommendConditionsMap{
		v1alpha1.Validated: v1alpha1.ResourceRecommendCondition{
			Type:               v1alpha1.Validated,
			Status:             v1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(time.Date(2023, 2, 2, 2, 0, 0, 0, time.UTC)),
			Reason:             "reason2",
			Message:            "test msg2",
		},
	}

	case3ConditionsMap := ResourceRecommendConditionsMap{
		v1alpha1.Validated: v1alpha1.ResourceRecommendCondition{
			Type:               v1alpha1.Validated,
			Status:             v1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(time.Date(2023, 3, 3, 3, 0, 0, 0, time.UTC)),
			Reason:             "reason3",
			Message:            "test msg3",
		},
	}
	fakeTime3 := time.Date(2023, 3, 3, 3, 0, 0, 0, time.UTC)
	case3WantConditionsMap := ResourceRecommendConditionsMap{
		v1alpha1.Validated: v1alpha1.ResourceRecommendCondition{
			Type:               v1alpha1.Validated,
			Status:             v1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(time.Date(2023, 3, 3, 3, 0, 0, 0, time.UTC)),
			Reason:             "reason3",
			Message:            "test msg3",
		},
	}

	case4ConditionsMap := ResourceRecommendConditionsMap{
		v1alpha1.Validated: v1alpha1.ResourceRecommendCondition{
			Type:               v1alpha1.Validated,
			Status:             v1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(time.Date(2023, 3, 3, 3, 0, 0, 0, time.UTC)),
		},
	}
	fakeTime4 := time.Date(2023, 4, 4, 4, 0, 0, 0, time.UTC)
	case4WantConditionsMap := ResourceRecommendConditionsMap{
		v1alpha1.Validated: {
			Type:               v1alpha1.Validated,
			Status:             v1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(time.Date(2023, 3, 3, 3, 0, 0, 0, time.UTC)),
		},
		v1alpha1.Initialized: {
			Type:               v1alpha1.Initialized,
			Status:             v1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(fakeTime4),
			Reason:             "reason4",
			Message:            "test msg4",
		},
	}

	type args struct {
		condition v1alpha1.ResourceRecommendCondition
		fakeTime  time.Time
	}
	tests := []struct {
		name          string
		conditionsMap *ResourceRecommendConditionsMap
		args          args
		want          *ResourceRecommendConditionsMap
	}{
		{
			name:          "notExist",
			conditionsMap: case1ConditionsMap,
			args: args{
				condition: v1alpha1.ResourceRecommendCondition{
					Type:    v1alpha1.Validated,
					Status:  v1.ConditionFalse,
					Reason:  "reason1",
					Message: "test msg1",
				},
				fakeTime: fakeTime1,
			},
			want: &case1WantConditionsMap,
		},
		{
			name:          "update",
			conditionsMap: &case2ConditionsMap,
			args: args{
				condition: v1alpha1.ResourceRecommendCondition{
					Type:    v1alpha1.Validated,
					Status:  v1.ConditionFalse,
					Reason:  "reason2",
					Message: "test msg2",
				},
				fakeTime: fakeTime2,
			},
			want: &case2WantConditionsMap,
		},
		{
			name:          "same",
			conditionsMap: &case3ConditionsMap,
			args: args{
				condition: v1alpha1.ResourceRecommendCondition{
					Type:    v1alpha1.Validated,
					Status:  v1.ConditionFalse,
					Reason:  "reason3",
					Message: "test msg3",
				},
				fakeTime: fakeTime3,
			},
			want: &case3WantConditionsMap,
		},
		{
			name:          "add",
			conditionsMap: &case4ConditionsMap,
			args: args{
				condition: v1alpha1.ResourceRecommendCondition{
					Type:    v1alpha1.Initialized,
					Status:  v1.ConditionFalse,
					Reason:  "reason4",
					Message: "test msg4",
				},
				fakeTime: fakeTime4,
			},
			want: &case4WantConditionsMap,
		},
	}
	for _, tt := range tests {
		mockey.PatchConvey(tt.name, t, func() {
			defer mockey.UnPatchAll()
			mockey.Mock(time.Now).Return(tt.args.fakeTime).Build()

			tt.conditionsMap.Set(tt.args.condition)
			convey.So(tt.conditionsMap, convey.ShouldResemble, tt.want)
		})
	}
}

func TestResourceRecommendConditionsMap_AsList(t *testing.T) {
	tests := []struct {
		name          string
		conditionsMap ResourceRecommendConditionsMap
		want          []v1alpha1.ResourceRecommendCondition
	}{
		{
			name: "case",
			conditionsMap: ResourceRecommendConditionsMap{
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
			want: []v1alpha1.ResourceRecommendCondition{
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.conditionsMap.AsList(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AsList() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResourceRecommendConditionsMap_ConditionActive(t *testing.T) {
	conditionsMap := ResourceRecommendConditionsMap{
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
	}
	type args struct {
		conditionType v1alpha1.ResourceRecommendConditionType
	}
	tests := []struct {
		name          string
		conditionsMap ResourceRecommendConditionsMap
		args          args
		want          bool
	}{
		{
			name:          "notFound",
			conditionsMap: conditionsMap,
			args: args{
				conditionType: v1alpha1.RecommendationProvided,
			},
			want: false,
		},
		{
			name:          "true",
			conditionsMap: conditionsMap,
			args: args{
				conditionType: v1alpha1.Validated,
			},
			want: true,
		},
		{
			name:          "false",
			conditionsMap: conditionsMap,
			args: args{
				conditionType: v1alpha1.Initialized,
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.conditionsMap.ConditionActive(tt.args.conditionType); got != tt.want {
				t.Errorf("ConditionActive() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidationSucceededCondition(t *testing.T) {
	tests := []struct {
		name string
		want *v1alpha1.ResourceRecommendCondition
	}{
		{
			name: "case",
			want: &v1alpha1.ResourceRecommendCondition{
				Type:   "Validated",
				Status: "True",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidationSucceededCondition(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ValidationSucceededCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInitializationSucceededCondition(t *testing.T) {
	tests := []struct {
		name string
		want *v1alpha1.ResourceRecommendCondition
	}{
		{
			name: "case",
			want: &v1alpha1.ResourceRecommendCondition{
				Type:   "Initialized",
				Status: "True",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InitializationSucceededCondition(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("InitializationSucceededCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecommendationReadyCondition(t *testing.T) {
	tests := []struct {
		name string
		want *v1alpha1.ResourceRecommendCondition
	}{
		{
			name: "case",
			want: &v1alpha1.ResourceRecommendCondition{
				Type:   "RecommendationProvided",
				Status: "True",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RecommendationReadyCondition(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RecommendationReadyCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConvertCustomErrorToCondition(t *testing.T) {
	type args struct {
		err errortypes.CustomError
	}
	tests := []struct {
		name string
		args args
		want *v1alpha1.ResourceRecommendCondition
	}{
		{
			name: "Validated_err",
			args: args{
				err: errortypes.CustomError{
					Phase:   errortypes.Validated,
					Code:    "code1",
					Message: "err_msg1",
				},
			},
			want: &v1alpha1.ResourceRecommendCondition{
				Type:    v1alpha1.Validated,
				Status:  v1.ConditionFalse,
				Reason:  "code1",
				Message: "err_msg1",
			},
		},
		{
			name: "ProcessRegister_err",
			args: args{
				err: errortypes.CustomError{
					Phase:   errortypes.ProcessRegister,
					Code:    "code2",
					Message: "err_msg2",
				},
			},
			want: &v1alpha1.ResourceRecommendCondition{
				Type:    v1alpha1.Initialized,
				Status:  v1.ConditionFalse,
				Reason:  "code2",
				Message: "err_msg2",
			},
		},
		{
			name: "RecommendationProvided_err",
			args: args{
				err: errortypes.CustomError{
					Phase:   errortypes.RecommendationProvided,
					Code:    "code3",
					Message: "err_msg3",
				},
			},
			want: &v1alpha1.ResourceRecommendCondition{
				Type:    v1alpha1.RecommendationProvided,
				Status:  v1.ConditionFalse,
				Reason:  "code3",
				Message: "err_msg3",
			},
		},
		{
			name: "Unknown_err",
			args: args{
				err: errortypes.CustomError{
					Phase:   "testPhase",
					Code:    "code4",
					Message: "err_msg4",
				},
			},
			want: &v1alpha1.ResourceRecommendCondition{
				Type:    "testPhase",
				Status:  v1.ConditionFalse,
				Reason:  "code4",
				Message: "err_msg4",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConvertCustomErrorToCondition(tt.args.err); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ConvertCustomErrorToCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}
