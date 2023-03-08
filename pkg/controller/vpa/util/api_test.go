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

package util

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	externalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
)

func TestUpdateVPAConditions(t *testing.T) {
	oldvpa1 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			Conditions: []apis.VerticalPodAutoscalerCondition{
				{
					Type:               apis.RecommendationUpdated,
					Status:             core.ConditionTrue,
					LastTransitionTime: metav1.NewTime(time.Now()),
				},
			},
		},
	}
	newvpa1 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			Conditions: []apis.VerticalPodAutoscalerCondition{
				{
					Type:   apis.RecommendationUpdated,
					Status: core.ConditionTrue,
				},
			},
		},
	}
	newvpa2 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			Conditions: []apis.VerticalPodAutoscalerCondition{
				{
					Type:   apis.RecommendationUpdated,
					Status: core.ConditionFalse,
				},
			},
		},
	}
	for _, tc := range []struct {
		name            string
		oldvpa          *apis.KatalystVerticalPodAutoscaler
		conditionType   apis.VerticalPodAutoscalerConditionType
		conditionStatus core.ConditionStatus
		updateTime      bool
		newvpa          *apis.KatalystVerticalPodAutoscaler
	}{
		{
			name:            "status not change",
			oldvpa:          oldvpa1,
			conditionType:   apis.RecommendationUpdated,
			conditionStatus: core.ConditionTrue,
			updateTime:      false,
			newvpa:          newvpa1,
		},
		{
			name:            "update time",
			oldvpa:          oldvpa1,
			conditionType:   apis.RecommendationUpdated,
			conditionStatus: core.ConditionFalse,
			updateTime:      true,
			newvpa:          newvpa2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vpa := tc.oldvpa.DeepCopy()
			err := SetVPAConditions(vpa, tc.conditionType, tc.conditionStatus, "", "")
			assert.NoError(t, err)
			tc.newvpa.Status.Conditions[0].LastTransitionTime = tc.oldvpa.Status.Conditions[0].LastTransitionTime
			if tc.updateTime {
				assert.NotEqual(t, tc.newvpa.Status.Conditions, vpa.Status.Conditions)
				tc.newvpa.Status.Conditions[0].LastTransitionTime = vpa.Status.Conditions[0].LastTransitionTime
			}
			assert.Equal(t, vpa, tc.newvpa)
		})
	}
}

func TestUpdateAPIVPAConditions(t *testing.T) {
	oldvpa1 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			Conditions: []apis.VerticalPodAutoscalerCondition{
				{
					Type: apis.RecommendationUpdated,
				},
			},
		},
	}
	newvpa1 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			Conditions: []apis.VerticalPodAutoscalerCondition{
				{
					Type: apis.RecommendationUpdated,
				},
			},
		},
	}
	newvpa11 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			Conditions: []apis.VerticalPodAutoscalerCondition{
				{
					Type:   apis.RecommendationUpdated,
					Reason: "test",
				},
			},
		},
	}
	for _, tc := range []struct {
		name   string
		oldvpa *apis.KatalystVerticalPodAutoscaler
		newvpa *apis.KatalystVerticalPodAutoscaler
	}{
		{
			name:   "update diffrent type condition type",
			oldvpa: oldvpa1,
			newvpa: newvpa1,
		},
		{
			name:   "update same type condition type",
			oldvpa: oldvpa1,
			newvpa: newvpa11,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			InternalClient := externalfake.NewSimpleClientset()
			vpaUpdater := control.NewRealVPAUpdater(InternalClient)
			_, err := InternalClient.AutoscalingV1alpha1().KatalystVerticalPodAutoscalers(tc.oldvpa.Namespace).Create(context.TODO(), tc.oldvpa, metav1.CreateOptions{})
			assert.NoError(t, err)

			_, err = vpaUpdater.PatchVPAStatus(context.TODO(), tc.oldvpa, tc.newvpa)
			assert.NoError(t, err)

			vpa, err := InternalClient.AutoscalingV1alpha1().KatalystVerticalPodAutoscalers(tc.newvpa.Namespace).Get(context.TODO(), tc.newvpa.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tc.newvpa, vpa)
		})
	}
}

func TestUpdateVPARecConditions(t *testing.T) {
	oldvparec1 := &apis.VerticalPodAutoscalerRecommendation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
		},
		Status: apis.VerticalPodAutoscalerRecommendationStatus{
			Conditions: []apis.VerticalPodAutoscalerRecommendationCondition{
				{
					Type:               apis.RecommendationUpdatedToVPA,
					LastTransitionTime: metav1.NewTime(time.Now()),
				},
			},
		},
	}
	newvparec1 := &apis.VerticalPodAutoscalerRecommendation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
		},
		Status: apis.VerticalPodAutoscalerRecommendationStatus{
			Conditions: []apis.VerticalPodAutoscalerRecommendationCondition{
				{
					Type: apis.RecommendationUpdatedToVPA,
				},
			},
		},
	}
	for _, tc := range []struct {
		name            string
		oldvparec       *apis.VerticalPodAutoscalerRecommendation
		conditionType   apis.VerticalPodAutoscalerRecommendationConditionType
		forceUpdateTime bool
		newvparec       *apis.VerticalPodAutoscalerRecommendation
	}{
		{
			name:            "force update time",
			oldvparec:       oldvparec1,
			conditionType:   apis.RecommendationUpdatedToVPA,
			forceUpdateTime: true,
			newvparec:       newvparec1,
		},
		{
			name:            "not force update time",
			oldvparec:       newvparec1,
			conditionType:   apis.RecommendationUpdatedToVPA,
			forceUpdateTime: false,
			newvparec:       newvparec1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vparec := tc.oldvparec.DeepCopy()
			err := SetVPARecConditions(vparec, tc.conditionType, "", "", "")
			assert.NoError(t, err)
			if tc.forceUpdateTime {
				assert.NotEqual(t, tc.newvparec.Status.Conditions, vparec.Status.Conditions)
				tc.newvparec.Status.Conditions[0].LastTransitionTime = vparec.Status.Conditions[0].LastTransitionTime
			}
			assert.Equal(t, vparec, tc.newvparec)
		})
	}
}
