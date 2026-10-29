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

package vpa

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/controller/vpa/util"
)

func TestSetRecommendationAppliedCondition(t *testing.T) {
	t.Parallel()
	vs := &vpaStatusController{}

	for _, tc := range []struct {
		name            string
		pods            []*v1.Pod
		expectedStatus  v1.ConditionStatus
		expectedReason  string
		expectedMessage string
	}{
		{
			name: "all pods updated",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							apiconsts.PodAnnotationInplaceUpdateResourcesKey: `{"c1":{"requests":{"cpu":"1","memory":"1Gi"}}}`,
						},
					},
					Status: v1.PodStatus{
						Phase: v1.PodRunning,
					},
				},
			},
			expectedStatus:  v1.ConditionTrue,
			expectedReason:  util.VPAConditionReasonPodSpecUpdated,
			expectedMessage: "",
		},
		{
			name: "skip inactive pods",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							// This pod is not updated, but it is inactive so it should be skipped
						},
					},
					Status: v1.PodStatus{
						Phase: v1.PodSucceeded,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							apiconsts.PodAnnotationInplaceUpdateResourcesKey: `{"c1":{"requests":{"cpu":"1","memory":"1Gi"}}}`,
						},
					},
					Status: v1.PodStatus{
						Phase: v1.PodRunning,
					},
				},
			},
			expectedStatus:  v1.ConditionTrue,
			expectedReason:  util.VPAConditionReasonPodSpecUpdated,
			expectedMessage: "",
		},
		{
			name: "failed to update pods with inactive ones",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							apiconsts.PodAnnotationInplaceUpdateResourcesKey: `{"c1":{"requests":{"cpu":"1","memory":"1Gi"}}}`,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c1",
								Resources: v1.ResourceRequirements{
									Requests: v1.ResourceList{
										// Different from annotation to make CheckPodSpecUpdated return false
									},
								},
							},
						},
					},
					Status: v1.PodStatus{
						Phase: v1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							apiconsts.PodAnnotationInplaceUpdateResourcesKey: `{"c1":{"requests":{"cpu":"1","memory":"1Gi"}}}`,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c1",
								Resources: v1.ResourceRequirements{
									Requests: v1.ResourceList{},
								},
							},
						},
					},
					Status: v1.PodStatus{
						Phase: v1.PodFailed,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							apiconsts.PodAnnotationInplaceUpdateResourcesKey: `{"c1":{"requests":{"cpu":"1","memory":"1Gi"}}}`,
						},
						DeletionTimestamp: &metav1.Time{},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c1",
								Resources: v1.ResourceRequirements{
									Requests: v1.ResourceList{},
								},
							},
						},
					},
					Status: v1.PodStatus{
						Phase: v1.PodRunning,
						ContainerStatuses: []v1.ContainerStatus{
							{
								State: v1.ContainerState{
									Terminated: &v1.ContainerStateTerminated{},
								},
							},
						},
					},
				},
			},
			expectedStatus:  v1.ConditionFalse,
			expectedReason:  util.VPAConditionReasonPodSpecNoUpdate,
			expectedMessage: "failed to update 1 pods, active 1 pods, total 3 pods",
		},
		{
			name: "all active pods failed to update",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							apiconsts.PodAnnotationInplaceUpdateResourcesKey: `{"c1":{"requests":{"cpu":"1","memory":"1Gi"}}}`,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c1",
								Resources: v1.ResourceRequirements{
									Requests: v1.ResourceList{
										// Different from annotation to make CheckPodSpecUpdated return false
									},
								},
							},
						},
					},
					Status: v1.PodStatus{
						Phase: v1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							apiconsts.PodAnnotationInplaceUpdateResourcesKey: `{"c1":{"requests":{"cpu":"1","memory":"1Gi"}}}`,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "c1",
								Resources: v1.ResourceRequirements{
									Requests: v1.ResourceList{
										// Different from annotation to make CheckPodSpecUpdated return false
									},
								},
							},
						},
					},
					Status: v1.PodStatus{
						Phase: v1.PodPending,
					},
				},
			},
			expectedStatus:  v1.ConditionFalse,
			expectedReason:  util.VPAConditionReasonPodSpecNoUpdate,
			expectedMessage: "failed to update 2 pods, active 2 pods, total 2 pods",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vpa := &apis.KatalystVerticalPodAutoscaler{
				Status: apis.KatalystVerticalPodAutoscalerStatus{},
			}

			err := vs.setRecommendationAppliedCondition(vpa, tc.pods)
			assert.NoError(t, err)

			assert.Len(t, vpa.Status.Conditions, 1)
			assert.Equal(t, tc.expectedStatus, vpa.Status.Conditions[0].Status)
			assert.Equal(t, tc.expectedReason, vpa.Status.Conditions[0].Reason)
			assert.Equal(t, tc.expectedMessage, vpa.Status.Conditions[0].Message)
			assert.Equal(t, apis.RecommendationApplied, vpa.Status.Conditions[0].Type)
		})
	}
}
