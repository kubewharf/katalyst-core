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
	"encoding/json"
	"fmt"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	clientset "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned"
)

// ResourceRecommendUpdater is used to update ResourceRecommend CR
type ResourceRecommendUpdater interface {
	UpdateResourceRecommend(ctx context.Context, rec *apis.ResourceRecommend,
		opts v1.UpdateOptions) (*apis.ResourceRecommend, error)

	PatchResourceRecommend(ctx context.Context, oldRec *apis.ResourceRecommend,
		newRec *apis.ResourceRecommend) error

	CreateResourceRecommend(ctx context.Context, rec *apis.ResourceRecommend,
		opts v1.CreateOptions) (*apis.ResourceRecommend, error)
}
type DummyResourceRecommendUpdater struct{}

func (d *DummyResourceRecommendUpdater) UpdateResourceRecommend(_ context.Context, _ *apis.ResourceRecommend,
	_ v1.UpdateOptions,
) (*apis.ResourceRecommend, error) {
	return nil, nil
}

func (d *DummyResourceRecommendUpdater) PatchResourceRecommend(_ context.Context, _ *apis.ResourceRecommend,
	_ *apis.ResourceRecommend,
) error {
	return nil
}

func (d *DummyResourceRecommendUpdater) CreateResourceRecommend(_ context.Context, _ *apis.ResourceRecommend,
	_ v1.CreateOptions,
) (*apis.ResourceRecommend, error) {
	return nil, nil
}

type RealResourceRecommendUpdater struct {
	client clientset.Interface
}

func NewRealResourceRecommendUpdater(client clientset.Interface) *RealResourceRecommendUpdater {
	return &RealResourceRecommendUpdater{
		client: client,
	}
}

func (r *RealResourceRecommendUpdater) UpdateResourceRecommend(ctx context.Context, rec *apis.ResourceRecommend,
	opts v1.UpdateOptions,
) (*apis.ResourceRecommend, error) {
	if rec == nil {
		return nil, fmt.Errorf("can't update a nil ResourceRecommend")
	}

	return r.client.RecommendationV1alpha1().ResourceRecommends(rec.Namespace).Update(ctx, rec, opts)
}

func (r *RealResourceRecommendUpdater) PatchResourceRecommend(ctx context.Context, oldRec *apis.ResourceRecommend,
	newRec *apis.ResourceRecommend,
) error {
	if oldRec == nil || newRec == nil {
		return fmt.Errorf("can't patch a nil ResourceRecommend")
	}

	oldData, err := json.Marshal(oldRec)
	if err != nil {
		return err
	}
	newData, err := json.Marshal(newRec)
	if err != nil {
		return err
	}

	patchBytes, err := jsonmergepatch.CreateThreeWayJSONMergePatch(oldData, newData, oldData)
	if err != nil {
		return fmt.Errorf("failed to create merge patch for ResourceRecommend %q/%q: %v", oldRec.Namespace, oldRec.Name, err)
	}

	_, err = r.client.RecommendationV1alpha1().ResourceRecommends(oldRec.Namespace).Patch(ctx, oldRec.Name, types.MergePatchType, patchBytes, v1.PatchOptions{}, "status")
	return err
}

func (r *RealResourceRecommendUpdater) CreateResourceRecommend(ctx context.Context, rec *apis.ResourceRecommend, opts v1.CreateOptions) (*apis.ResourceRecommend, error) {
	if rec == nil {
		return nil, fmt.Errorf("can't update a nil ResourceRecommend")
	}

	return r.client.RecommendationV1alpha1().ResourceRecommends(rec.Namespace).Create(ctx, rec, opts)
}
