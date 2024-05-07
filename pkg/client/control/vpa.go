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

// VPAUpdater is used to update VPA CR
// todo: use patch instead of update to avoid conflict
type VPAUpdater interface {
	UpdateVPA(ctx context.Context, vpa *apis.KatalystVerticalPodAutoscaler,
		opts metav1.UpdateOptions) (*apis.KatalystVerticalPodAutoscaler, error)
	UpdateVPAStatus(ctx context.Context, vpa *apis.KatalystVerticalPodAutoscaler,
		opts metav1.UpdateOptions) (*apis.KatalystVerticalPodAutoscaler, error)
	PatchVPA(ctx context.Context, oldVPA,
		newVPA *apis.KatalystVerticalPodAutoscaler) (*apis.KatalystVerticalPodAutoscaler, error)
	PatchVPAStatus(ctx context.Context, oldVPA,
		newVPA *apis.KatalystVerticalPodAutoscaler) (*apis.KatalystVerticalPodAutoscaler, error)
}

type DummyVPAUpdater struct{}

func (d *DummyVPAUpdater) UpdateVPA(_ context.Context,
	_ *apis.KatalystVerticalPodAutoscaler, _ metav1.UpdateOptions,
) (*apis.KatalystVerticalPodAutoscaler, error) {
	return nil, nil
}

func (d *DummyVPAUpdater) UpdateVPAStatus(_ context.Context,
	_ *apis.KatalystVerticalPodAutoscaler, _ metav1.UpdateOptions,
) (*apis.KatalystVerticalPodAutoscaler, error) {
	return nil, nil
}

func (d *DummyVPAUpdater) PatchVPA(_ context.Context, _,
	newVPA *apis.KatalystVerticalPodAutoscaler,
) (*apis.KatalystVerticalPodAutoscaler, error) {
	return newVPA, nil
}

func (d *DummyVPAUpdater) PatchVPAStatus(_ context.Context, _,
	newVPA *apis.KatalystVerticalPodAutoscaler,
) (*apis.KatalystVerticalPodAutoscaler, error) {
	return newVPA, nil
}

type RealVPAUpdater struct {
	client clientset.Interface
}

func NewRealVPAUpdater(client clientset.Interface) VPAUpdater {
	return &RealVPAUpdater{
		client: client,
	}
}

func (r *RealVPAUpdater) UpdateVPA(ctx context.Context, vpa *apis.KatalystVerticalPodAutoscaler,
	opts metav1.UpdateOptions,
) (*apis.KatalystVerticalPodAutoscaler, error) {
	if vpa == nil {
		return nil, fmt.Errorf("can't update a nil vpa")
	}

	return r.client.AutoscalingV1alpha1().KatalystVerticalPodAutoscalers(vpa.Namespace).Update(
		ctx, vpa, opts)
}

func (r *RealVPAUpdater) UpdateVPAStatus(ctx context.Context, vpa *apis.KatalystVerticalPodAutoscaler,
	opts metav1.UpdateOptions,
) (*apis.KatalystVerticalPodAutoscaler, error) {
	if vpa == nil {
		return nil, fmt.Errorf("can't update a nil vpa's status")
	}

	return r.client.AutoscalingV1alpha1().KatalystVerticalPodAutoscalers(vpa.Namespace).UpdateStatus(
		ctx, vpa, opts)
}

func (r *RealVPAUpdater) PatchVPA(ctx context.Context, oldVPA,
	newVPA *apis.KatalystVerticalPodAutoscaler,
) (*apis.KatalystVerticalPodAutoscaler, error) {
	if oldVPA == nil || newVPA == nil {
		return nil, fmt.Errorf("can't patch a nil vpa")
	}

	oldData, err := json.Marshal(oldVPA)
	if err != nil {
		return nil, err
	}

	newData, err := json.Marshal(newVPA)
	if err != nil {
		return nil, err
	}

	patchBytes, err := jsonmergepatch.CreateThreeWayJSONMergePatch(oldData, newData, oldData)
	if err != nil {
		return nil, fmt.Errorf("failed to create merge patch for vpa %q/%q: %v",
			oldVPA.Namespace, oldVPA.Name, err)
	} else if general.JsonPathEmpty(patchBytes) {
		return newVPA, nil
	}

	return r.client.AutoscalingV1alpha1().KatalystVerticalPodAutoscalers(oldVPA.Namespace).
		Patch(ctx, oldVPA.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
}

func (r *RealVPAUpdater) PatchVPAStatus(ctx context.Context, oldVPA,
	newVPA *apis.KatalystVerticalPodAutoscaler,
) (*apis.KatalystVerticalPodAutoscaler, error) {
	if oldVPA == nil || newVPA == nil {
		return nil, fmt.Errorf("can't patch a nil vpa")
	}

	oldData, err := json.Marshal(apis.KatalystVerticalPodAutoscaler{Status: oldVPA.Status})
	if err != nil {
		return nil, err
	}

	newData, err := json.Marshal(apis.KatalystVerticalPodAutoscaler{Status: newVPA.Status})
	if err != nil {
		return nil, err
	}

	if klog.V(5).Enabled() {
		d, _ := json.Marshal(&oldVPA.Status)
		dn, _ := json.Marshal(&newVPA.Status)
		klog.Infof("vpa %s status updated: %v => %v", oldVPA.Name, string(d), string(dn))
	}

	patchBytes, err := jsonmergepatch.CreateThreeWayJSONMergePatch(oldData, newData, oldData)
	if err != nil {
		return nil, fmt.Errorf("failed to create merge patch for vpa %q/%q: %v",
			oldVPA.Namespace, oldVPA.Name, err)
	} else if general.JsonPathEmpty(patchBytes) {
		return newVPA, nil
	}

	return r.client.AutoscalingV1alpha1().KatalystVerticalPodAutoscalers(oldVPA.Namespace).
		Patch(ctx, oldVPA.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
}

// RealVPAUpdaterWithMetric todo: implement with emitting metrics on updating
type RealVPAUpdaterWithMetric struct {
	client clientset.Interface
	RealVPAUpdater
}
