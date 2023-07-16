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

func TestPatchVPA(t *testing.T) {
	t.Parallel()

	oldvpa1 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
		},
	}
	newvpa1 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
			Annotations: map[string]string{
				"a1": "a1",
			},
		},
	}

	for _, tc := range []struct {
		name   string
		oldVPA *apis.KatalystVerticalPodAutoscaler
		newVPA *apis.KatalystVerticalPodAutoscaler
	}{
		{
			name:   "add annotation",
			oldVPA: oldvpa1,
			newVPA: newvpa1,
		},
		{
			name:   "delete annotation",
			oldVPA: newvpa1,
			newVPA: oldvpa1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			internalClient := externalfake.NewSimpleClientset(tc.oldVPA)
			updater := NewRealVPAUpdater(internalClient)
			_, err := updater.PatchVPA(context.TODO(), tc.oldVPA, tc.newVPA)
			assert.NoError(t, err)
			vpa, err := internalClient.AutoscalingV1alpha1().KatalystVerticalPodAutoscalers("default").
				Get(context.TODO(), tc.oldVPA.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tc.newVPA, vpa)
		})
	}
}

func TestPatchVPAStatus(t *testing.T) {
	t.Parallel()

	oldvpa1 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vpa1",
			Namespace: "default",
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

	for _, tc := range []struct {
		name   string
		oldVPA *apis.KatalystVerticalPodAutoscaler
		newVPA *apis.KatalystVerticalPodAutoscaler
	}{
		{
			name:   "add condition",
			oldVPA: oldvpa1,
			newVPA: newvpa1,
		},
		{
			name:   "delete condition",
			oldVPA: newvpa1,
			newVPA: oldvpa1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			internalClient := externalfake.NewSimpleClientset(tc.oldVPA)
			updater := NewRealVPAUpdater(internalClient)
			_, err := updater.PatchVPAStatus(context.TODO(), tc.oldVPA, tc.newVPA)
			assert.NoError(t, err)
			vpa, err := internalClient.AutoscalingV1alpha1().KatalystVerticalPodAutoscalers("default").
				Get(context.TODO(), tc.oldVPA.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tc.newVPA, vpa)
		})
	}
}
