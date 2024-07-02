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

	autoscaling "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha2"
	clientset "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type HPAManager interface {
	Create(ctx context.Context, new *autoscaling.HorizontalPodAutoscaler, opts metav1.CreateOptions) (*autoscaling.HorizontalPodAutoscaler, error)
	Patch(ctx context.Context, old, new *autoscaling.HorizontalPodAutoscaler, opts metav1.PatchOptions) (*autoscaling.HorizontalPodAutoscaler, error)
}

type HPAManageImpl struct {
	client kubernetes.Interface
}

func NewHPAManager(client kubernetes.Interface) HPAManager {
	return &HPAManageImpl{
		client: client,
	}
}

func (h *HPAManageImpl) Create(ctx context.Context,
	new *autoscaling.HorizontalPodAutoscaler, opts metav1.CreateOptions,
) (*autoscaling.HorizontalPodAutoscaler, error) {
	if new == nil {
		return nil, fmt.Errorf("can't create a nil HPA")
	}
	return h.client.AutoscalingV2().HorizontalPodAutoscalers(new.Namespace).Create(ctx, new, opts)
}

func (h *HPAManageImpl) Patch(ctx context.Context, old,
	new *autoscaling.HorizontalPodAutoscaler, opts metav1.PatchOptions,
) (*autoscaling.HorizontalPodAutoscaler, error) {
	if old == nil || new == nil {
		return nil, fmt.Errorf("can't patch a nil HPA")
	}

	oldData, err := json.Marshal(old)
	if err != nil {
		return nil, err
	}

	newData, err := json.Marshal(new)
	if err != nil {
		return nil, err
	}

	patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, &autoscaling.HorizontalPodAutoscaler{})
	if err != nil {
		return nil, fmt.Errorf("failed to create merge patch for hpa %q/%q: %v", old.Namespace, old.Name, err)
	} else if general.JsonPathEmpty(patchBytes) {
		return nil, nil
	}

	return h.client.AutoscalingV2().HorizontalPodAutoscalers(old.Namespace).Patch(ctx, old.Name, types.StrategicMergePatchType, patchBytes, opts)
}

type IHPAUpdater interface {
	Update(ctx context.Context, ihpa *apis.IntelligentHorizontalPodAutoscaler,
		opts metav1.UpdateOptions) (*apis.IntelligentHorizontalPodAutoscaler, error)
	UpdateStatus(ctx context.Context, ihpa *apis.IntelligentHorizontalPodAutoscaler,
		opts metav1.UpdateOptions) (*apis.IntelligentHorizontalPodAutoscaler, error)
}

type IHPAUpdateImpl struct {
	client clientset.Interface
}

func NewIHPAUpdater(client clientset.Interface) IHPAUpdater {
	return &IHPAUpdateImpl{
		client: client,
	}
}

func (i *IHPAUpdateImpl) Update(ctx context.Context, ihpa *apis.IntelligentHorizontalPodAutoscaler, opts metav1.UpdateOptions) (*apis.IntelligentHorizontalPodAutoscaler, error) {
	if ihpa == nil {
		return nil, fmt.Errorf("can't update a nil ihpa")
	}

	return i.client.AutoscalingV1alpha2().IntelligentHorizontalPodAutoscalers(ihpa.Namespace).Update(
		ctx, ihpa, opts)
}

func (i *IHPAUpdateImpl) UpdateStatus(ctx context.Context, ihpa *apis.IntelligentHorizontalPodAutoscaler, opts metav1.UpdateOptions) (*apis.IntelligentHorizontalPodAutoscaler, error) {
	if ihpa == nil {
		return nil, fmt.Errorf("can't update a nil ihpa's status")
	}

	return i.client.AutoscalingV1alpha2().IntelligentHorizontalPodAutoscalers(ihpa.Namespace).UpdateStatus(
		ctx, ihpa, opts)
}

type VirtualWorkloadManager interface {
	Create(ctx context.Context, vw *apis.VirtualWorkload,
		opts metav1.CreateOptions) (*apis.VirtualWorkload, error)
	Update(ctx context.Context, vw *apis.VirtualWorkload,
		opts metav1.UpdateOptions) (*apis.VirtualWorkload, error)
	UpdateStatus(ctx context.Context, vw *apis.VirtualWorkload,
		opts metav1.UpdateOptions) (*apis.VirtualWorkload, error)
}

type vmManageImpl struct {
	client clientset.Interface
}

func NewVirtualWorkloadManager(client clientset.Interface) VirtualWorkloadManager {
	return &vmManageImpl{
		client: client,
	}
}

func (i *vmManageImpl) Create(ctx context.Context, vw *apis.VirtualWorkload, opts metav1.CreateOptions) (*apis.VirtualWorkload, error) {
	if vw == nil {
		return nil, fmt.Errorf("can't create a nil vw")
	}

	return i.client.AutoscalingV1alpha2().VirtualWorkloads(vw.Namespace).Create(
		ctx, vw, opts)
}

func (i *vmManageImpl) Update(ctx context.Context, vw *apis.VirtualWorkload, opts metav1.UpdateOptions) (*apis.VirtualWorkload, error) {
	if vw == nil {
		return nil, fmt.Errorf("can't update a nil vw")
	}

	return i.client.AutoscalingV1alpha2().VirtualWorkloads(vw.Namespace).Update(
		ctx, vw, opts)
}

func (i *vmManageImpl) UpdateStatus(ctx context.Context, vw *apis.VirtualWorkload, opts metav1.UpdateOptions) (*apis.VirtualWorkload, error) {
	if vw == nil {
		return nil, fmt.Errorf("can't update a nil vw's status")
	}

	return i.client.AutoscalingV1alpha2().VirtualWorkloads(vw.Namespace).UpdateStatus(
		ctx, vw, opts)
}
