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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	externalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
)

func TestPatchVPARec(t *testing.T) {
	t.Parallel()

	oldvparec1 := &apis.VerticalPodAutoscalerRecommendation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
		},
	}
	newvparec1 := &apis.VerticalPodAutoscalerRecommendation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
			Annotations: map[string]string{
				"a1": "a1",
			},
		},
	}

	for _, tc := range []struct {
		name      string
		oldVPARec *apis.VerticalPodAutoscalerRecommendation
		newVPARec *apis.VerticalPodAutoscalerRecommendation
	}{
		{
			name:      "add annotation",
			oldVPARec: oldvparec1,
			newVPARec: newvparec1,
		},
		{
			name:      "delete annotation",
			oldVPARec: newvparec1,
			newVPARec: oldvparec1,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			internalClient := externalfake.NewSimpleClientset(tc.oldVPARec)
			updater := NewRealVPARecommendationUpdater(internalClient)
			err := updater.PatchVPARecommendation(context.TODO(), tc.oldVPARec, tc.newVPARec)
			assert.NoError(t, err)
			vpa, err := internalClient.AutoscalingV1alpha1().VerticalPodAutoscalerRecommendations("default").
				Get(context.TODO(), tc.oldVPARec.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tc.newVPARec, vpa)
		})
	}
}

func TestPatchVPARecStatus(t *testing.T) {
	t.Parallel()

	oldvparec1 := &apis.VerticalPodAutoscalerRecommendation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
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
		name      string
		oldVPARec *apis.VerticalPodAutoscalerRecommendation
		newVPARec *apis.VerticalPodAutoscalerRecommendation
	}{
		{
			name:      "add annotation",
			oldVPARec: oldvparec1,
			newVPARec: newvparec1,
		},
		{
			name:      "delete annotation",
			oldVPARec: newvparec1,
			newVPARec: oldvparec1,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			internalClient := externalfake.NewSimpleClientset(tc.oldVPARec)
			updater := NewRealVPARecommendationUpdater(internalClient)
			err := updater.PatchVPARecommendationStatus(context.TODO(), tc.oldVPARec, tc.newVPARec)
			assert.NoError(t, err)
			vpa, err := internalClient.AutoscalingV1alpha1().VerticalPodAutoscalerRecommendations("default").
				Get(context.TODO(), tc.oldVPARec.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tc.newVPARec, vpa)
		})
	}
}
