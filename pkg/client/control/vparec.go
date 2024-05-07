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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	clientset "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// VPARecommendationUpdater is used to update VPARecommendation CR
type VPARecommendationUpdater interface {
	UpdateVPARecommendation(ctx context.Context, vpaRec *apis.VerticalPodAutoscalerRecommendation,
		opts metav1.UpdateOptions) (*apis.VerticalPodAutoscalerRecommendation, error)
	UpdateVPARecommendationStatus(ctx context.Context, vpaRec *apis.VerticalPodAutoscalerRecommendation,
		opts metav1.UpdateOptions) (*apis.VerticalPodAutoscalerRecommendation, error)

	PatchVPARecommendation(ctx context.Context, oldVpaRec, newVpaRec *apis.VerticalPodAutoscalerRecommendation) error
	PatchVPARecommendationStatus(ctx context.Context, oldVpaRec, newVpaRec *apis.VerticalPodAutoscalerRecommendation) error

	CreateVPARecommendation(ctx context.Context, vpaRec *apis.VerticalPodAutoscalerRecommendation,
		opts metav1.CreateOptions) (*apis.VerticalPodAutoscalerRecommendation, error)
	DeleteVPARecommendation(ctx context.Context, vpaRec *apis.VerticalPodAutoscalerRecommendation, opts metav1.DeleteOptions) error
}

type DummyVPARecommendationUpdater struct{}

func (d *DummyVPARecommendationUpdater) UpdateVPARecommendation(_ context.Context, _ *apis.VerticalPodAutoscalerRecommendation, _ metav1.UpdateOptions) (*apis.VerticalPodAutoscalerRecommendation, error) {
	return nil, nil
}

func (d *DummyVPARecommendationUpdater) UpdateVPARecommendationStatus(_ context.Context, _ *apis.VerticalPodAutoscalerRecommendation, _ metav1.UpdateOptions) (*apis.VerticalPodAutoscalerRecommendation, error) {
	return nil, nil
}

func (d *DummyVPARecommendationUpdater) PatchVPARecommendation(ctx context.Context, oldVparec, newVparec *apis.VerticalPodAutoscalerRecommendation) error {
	return nil
}

func (d *DummyVPARecommendationUpdater) PatchVPARecommendationStatus(ctx context.Context, oldVparec, newVparec *apis.VerticalPodAutoscalerRecommendation) error {
	return nil
}

func (d *DummyVPARecommendationUpdater) CreateVPARecommendation(_ context.Context, _ *apis.VerticalPodAutoscalerRecommendation, _ metav1.CreateOptions) (*apis.VerticalPodAutoscalerRecommendation, error) {
	return nil, nil
}

func (d *DummyVPARecommendationUpdater) DeleteVPARecommendation(_ context.Context, _ *apis.VerticalPodAutoscalerRecommendation, _ metav1.DeleteOptions) error {
	return nil
}

type RealVPARecommendationUpdater struct {
	client clientset.Interface
}

func NewRealVPARecommendationUpdater(client clientset.Interface) *RealVPARecommendationUpdater {
	return &RealVPARecommendationUpdater{
		client: client,
	}
}

func (r *RealVPARecommendationUpdater) UpdateVPARecommendation(ctx context.Context, vpaRec *apis.VerticalPodAutoscalerRecommendation, opts metav1.UpdateOptions) (*apis.VerticalPodAutoscalerRecommendation, error) {
	if vpaRec == nil {
		return nil, fmt.Errorf("can't update a nil verticalPodAutoscalerRecommendation")
	}

	return r.client.AutoscalingV1alpha1().VerticalPodAutoscalerRecommendations(vpaRec.Namespace).Update(ctx, vpaRec, opts)
}

func (r *RealVPARecommendationUpdater) UpdateVPARecommendationStatus(ctx context.Context, vpaRec *apis.VerticalPodAutoscalerRecommendation, opts metav1.UpdateOptions) (*apis.VerticalPodAutoscalerRecommendation, error) {
	if vpaRec == nil {
		return nil, fmt.Errorf("can't update a nil verticalPodAutoscalerRecommendation's status")
	}

	return r.client.AutoscalingV1alpha1().VerticalPodAutoscalerRecommendations(vpaRec.Namespace).UpdateStatus(ctx, vpaRec, opts)
}

func (r *RealVPARecommendationUpdater) PatchVPARecommendation(ctx context.Context, oldVpaRec, newVpaRec *apis.VerticalPodAutoscalerRecommendation) error {
	if oldVpaRec == nil || newVpaRec == nil {
		return fmt.Errorf("can't update a nil VPARec")
	}

	oldData, err := json.Marshal(oldVpaRec)
	if err != nil {
		return err
	}

	newData, err := json.Marshal(newVpaRec)
	if err != nil {
		return err
	}
	patchBytes, err := jsonmergepatch.CreateThreeWayJSONMergePatch(oldData, newData, oldData)

	if err != nil {
		return fmt.Errorf("failed to create merge patch for VPARec %q/%q: %v", oldVpaRec.Namespace, oldVpaRec.Name, err)
	} else if general.JsonPathEmpty(patchBytes) {
		return nil
	}

	_, err = r.client.AutoscalingV1alpha1().VerticalPodAutoscalerRecommendations(oldVpaRec.Namespace).Patch(ctx, oldVpaRec.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	return err
}

func (r *RealVPARecommendationUpdater) PatchVPARecommendationStatus(ctx context.Context, oldVpaRec, newVpaRec *apis.VerticalPodAutoscalerRecommendation) error {
	if oldVpaRec == nil || newVpaRec == nil {
		return fmt.Errorf("can't update a nil VPARec")
	}

	oldData, err := json.Marshal(apis.VerticalPodAutoscalerRecommendation{Status: oldVpaRec.Status})
	if err != nil {
		return err
	}

	newData, err := json.Marshal(apis.VerticalPodAutoscalerRecommendation{Status: newVpaRec.Status})
	if err != nil {
		return err
	}

	if klog.V(5).Enabled() {
		d, _ := json.Marshal(&oldVpaRec.Status)
		dn, _ := json.Marshal(&newVpaRec.Status)
		klog.Infof("vparec %s status updated: %v => %v", oldVpaRec.Name, string(d), string(dn))
	}

	patchBytes, err := jsonmergepatch.CreateThreeWayJSONMergePatch(oldData, newData, oldData)
	if err != nil {
		return fmt.Errorf("failed to create merge patch for VPARec %q/%q: %v", oldVpaRec.Namespace, oldVpaRec.Name, err)
	} else if general.JsonPathEmpty(patchBytes) {
		return nil
	}

	_, err = r.client.AutoscalingV1alpha1().VerticalPodAutoscalerRecommendations(oldVpaRec.Namespace).Patch(ctx, oldVpaRec.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	return err
}

func (r *RealVPARecommendationUpdater) CreateVPARecommendation(ctx context.Context, vpaRec *apis.VerticalPodAutoscalerRecommendation,
	opts metav1.CreateOptions,
) (*apis.VerticalPodAutoscalerRecommendation, error) {
	return r.client.AutoscalingV1alpha1().VerticalPodAutoscalerRecommendations(vpaRec.Namespace).Create(ctx, vpaRec, opts)
}

func (r *RealVPARecommendationUpdater) DeleteVPARecommendation(ctx context.Context, vpaRec *apis.VerticalPodAutoscalerRecommendation, opts metav1.DeleteOptions) error {
	return r.client.AutoscalingV1alpha1().VerticalPodAutoscalerRecommendations(vpaRec.Namespace).Delete(ctx, vpaRec.Name, opts)
}

// RealVPARecommendationUpdaterWithMetric todo: implement with emitting metrics on updating
type RealVPARecommendationUpdaterWithMetric struct {
	client clientset.Interface
	RealVPARecommendationUpdater
}
