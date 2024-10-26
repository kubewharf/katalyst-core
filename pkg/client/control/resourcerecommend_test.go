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

	apis "github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	externalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
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
		gotErr bool
	}{
		{
			name:   "add annotation",
			oldRec: oldRec,
			newRec: newRec,
			gotErr: false,
		},
		{
			name:   "remove annotation",
			oldRec: oldRec,
			newRec: newRec,
			gotErr: false,
		},
		{
			name:   "missing new rec",
			oldRec: oldRec,
			newRec: nil,
			gotErr: true,
		},
		{
			name:   "same rec",
			oldRec: oldRec,
			newRec: oldRec,
			gotErr: false,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			internalClient := externalfake.NewSimpleClientset(tc.oldRec)
			updater := NewRealResourceRecommendUpdater(internalClient)
			err := updater.PatchResourceRecommend(context.TODO(), tc.oldRec, tc.newRec)
			assert.Equal(t, tc.gotErr, err != nil)
			rec, err := internalClient.RecommendationV1alpha1().
				ResourceRecommends("default").Get(context.TODO(), tc.oldRec.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			if !tc.gotErr {
				assert.Equal(t, tc.newRec, rec)
			}
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
		name   string
		rec    *apis.ResourceRecommend
		gotErr bool
	}{
		{
			name:   "create rec",
			rec:    rec,
			gotErr: false,
		},
		{
			name:   "missing rec",
			rec:    nil,
			gotErr: true,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			internalClient := externalfake.NewSimpleClientset()
			updater := NewRealResourceRecommendUpdater(internalClient)
			rec, err := updater.CreateResourceRecommend(context.TODO(), tc.rec, metav1.CreateOptions{})
			assert.Equal(t, tc.gotErr, err != nil)
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
		gotErr bool
	}{
		{
			name:   "add annotation",
			oldRec: oldRec,
			newRec: newRec,
			gotErr: false,
		},
		{
			name:   "remove annotation",
			oldRec: oldRec,
			newRec: newRec,
			gotErr: false,
		},
		{
			name:   "new rec is nil",
			oldRec: oldRec,
			newRec: nil,
			gotErr: true,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			internalClient := externalfake.NewSimpleClientset(tc.oldRec)
			updater := NewRealResourceRecommendUpdater(internalClient)
			rec, err := updater.UpdateResourceRecommend(context.TODO(), tc.newRec, metav1.UpdateOptions{})
			assert.Equal(t, tc.gotErr, err != nil)
			assert.Equal(t, tc.newRec, rec)
		})
	}
}

func TestDummyUpdater(t *testing.T) {
	t.Parallel()
	updater := DummyResourceRecommendUpdater{}

	assert.NoError(t, updater.PatchResourceRecommend(nil, nil, nil))

	_, err := updater.UpdateResourceRecommend(nil, nil, metav1.UpdateOptions{})
	assert.NoError(t, err)

	_, err = updater.CreateResourceRecommend(nil, nil, metav1.CreateOptions{})
	assert.NoError(t, err)
}
