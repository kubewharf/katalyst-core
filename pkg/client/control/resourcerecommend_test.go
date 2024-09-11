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

	apis "github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	externalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPatchResourceRecommend(t *testing.T) {
	t.Parallel()

	oldRec := &apis.ResourceRecommend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rec1",
			Namespace: "default",
		},
	}

	newRec := &apis.ResourceRecommend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rec1",
			Namespace: "default",
			Annotations: map[string]string{
				"recommendation.katalyst.io/resource-recommend": "true",
			},
		},
	}

	for _, tc := range []struct {
		name   string
		oldRec *apis.ResourceRecommend
		newRec *apis.ResourceRecommend
	}{
		{
			name:   "add annotation",
			oldRec: oldRec,
			newRec: newRec,
		},
		{
			name:   "remove annotation",
			oldRec: oldRec,
			newRec: newRec,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			internalClient := externalfake.NewSimpleClientset(tc.oldRec)
			updater := NewRealResourceRecommendUpdater(internalClient)
			err := updater.PatchResourceRecommend(context.TODO(), tc.oldRec, tc.newRec)
			assert.NoError(t, err)
			rec, err := internalClient.RecommendationV1alpha1().
				ResourceRecommends("default").Get(context.TODO(), tc.oldRec.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tc.newRec, rec)
		})
	}
}

func TestCreateResourceRecommend(t *testing.T) {
	t.Parallel()

	rec := &apis.ResourceRecommend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rec1",
			Namespace: "default",
		},
	}

	for _, tc := range []struct {
		name string
		rec  *apis.ResourceRecommend
	}{
		{
			name: "create rec",
			rec:  rec,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			internalClient := externalfake.NewSimpleClientset()
			updater := NewRealResourceRecommendUpdater(internalClient)
			rec, err := updater.CreateResourceRecommend(context.TODO(), tc.rec, metav1.CreateOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tc.rec, rec)
		})
	}
}

func TestUpdateResourceRecommend(t *testing.T) {
	t.Parallel()

	oldRec := &apis.ResourceRecommend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rec1",
			Namespace: "default",
		},
	}

	newRec := &apis.ResourceRecommend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rec1",
			Namespace: "default",
			Annotations: map[string]string{
				"recommendation.katalyst.io/resource-recommend": "true",
			},
		},
	}

	for _, tc := range []struct {
		name   string
		oldRec *apis.ResourceRecommend
		newRec *apis.ResourceRecommend
	}{
		{
			name:   "add annotation",
			oldRec: oldRec,
			newRec: newRec,
		},
		{
			name:   "remove annotation",
			oldRec: oldRec,
			newRec: newRec,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			internalClient := externalfake.NewSimpleClientset(tc.oldRec)
			updater := NewRealResourceRecommendUpdater(internalClient)
			rec, err := updater.UpdateResourceRecommend(context.TODO(), tc.newRec, metav1.UpdateOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tc.newRec, rec)
		})
	}
}
