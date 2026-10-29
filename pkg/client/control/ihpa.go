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

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	autoscalingv2beta2 "k8s.io/api/autoscaling/v2beta2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	autoscalingv2client "k8s.io/client-go/kubernetes/typed/autoscaling/v2"
	autoscalingv2beta2client "k8s.io/client-go/kubernetes/typed/autoscaling/v2beta2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha2"
	clientset "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	hpaV2      = "autoscaling/v2"
	hpaV2beta2 = "autoscaling/v2beta2"
)

type HPAManager interface {
	Get(ctx context.Context, namespace, name string, opts metav1.GetOptions) (*autoscalingv2.HorizontalPodAutoscaler, error)
	Create(ctx context.Context, new *autoscalingv2.HorizontalPodAutoscaler, opts metav1.CreateOptions) (*autoscalingv2.HorizontalPodAutoscaler, error)
	Patch(ctx context.Context, old, new *autoscalingv2.HorizontalPodAutoscaler, opts metav1.PatchOptions) (*autoscalingv2.HorizontalPodAutoscaler, error)
}

type hpaAdapter interface {
	Get(ctx context.Context, namespace, name string, opts metav1.GetOptions) (*autoscalingv2.HorizontalPodAutoscaler, error)
	Create(ctx context.Context, new *autoscalingv2.HorizontalPodAutoscaler, opts metav1.CreateOptions) (*autoscalingv2.HorizontalPodAutoscaler, error)
	Patch(ctx context.Context, old, new *autoscalingv2.HorizontalPodAutoscaler, opts metav1.PatchOptions) (*autoscalingv2.HorizontalPodAutoscaler, error)
}

type hpaManagerImpl struct {
	adapter hpaAdapter
}

func NewHPAManager(
	client kubernetes.Interface,
	discoveryClient discovery.DiscoveryInterface,
) (HPAManager, error) {
	groupVersion, err := native.FindSupportedGroupVersion(
		discoveryClient,
		"horizontalpodautoscalers",
		[]string{"get", "create", "patch"},
		hpaV2,
		hpaV2beta2,
	)
	if err != nil {
		return nil, fmt.Errorf("discover HorizontalPodAutoscaler API: %w", err)
	}

	manager := &hpaManagerImpl{}
	switch groupVersion {
	case hpaV2:
		manager.adapter = &hpaV2Adapter{client: client.AutoscalingV2()}
	case hpaV2beta2:
		manager.adapter = &hpaV2beta2Adapter{client: client.AutoscalingV2beta2()}
	default:
		return nil, fmt.Errorf("unsupported HorizontalPodAutoscaler API %q", groupVersion)
	}
	return manager, nil
}

func (h *hpaManagerImpl) Get(
	ctx context.Context,
	namespace, name string,
	opts metav1.GetOptions,
) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	return h.adapter.Get(ctx, namespace, name, opts)
}

func (h *hpaManagerImpl) Create(
	ctx context.Context,
	new *autoscalingv2.HorizontalPodAutoscaler,
	opts metav1.CreateOptions,
) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	if new == nil {
		return nil, fmt.Errorf("can't create a nil HPA")
	}
	return h.adapter.Create(ctx, new, opts)
}

func (h *hpaManagerImpl) Patch(
	ctx context.Context,
	old, new *autoscalingv2.HorizontalPodAutoscaler,
	opts metav1.PatchOptions,
) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	if old == nil || new == nil {
		return nil, fmt.Errorf("can't patch a nil HPA")
	}
	return h.adapter.Patch(ctx, old, new, opts)
}

type hpaV2Adapter struct {
	client autoscalingv2client.AutoscalingV2Interface
}

func (h *hpaV2Adapter) Get(
	ctx context.Context,
	namespace, name string,
	opts metav1.GetOptions,
) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	return h.client.HorizontalPodAutoscalers(namespace).Get(ctx, name, opts)
}

func (h *hpaV2Adapter) Create(
	ctx context.Context,
	new *autoscalingv2.HorizontalPodAutoscaler,
	opts metav1.CreateOptions,
) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	return h.client.HorizontalPodAutoscalers(new.Namespace).Create(ctx, new, opts)
}

func (h *hpaV2Adapter) Patch(
	ctx context.Context,
	old, new *autoscalingv2.HorizontalPodAutoscaler,
	opts metav1.PatchOptions,
) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	patchBytes, err := createHPAPatch(old, new, &autoscalingv2.HorizontalPodAutoscaler{})
	if err != nil || general.JsonPathEmpty(patchBytes) {
		return nil, err
	}
	return h.client.HorizontalPodAutoscalers(old.Namespace).Patch(
		ctx, old.Name, types.StrategicMergePatchType, patchBytes, opts)
}

type hpaV2beta2Adapter struct {
	client autoscalingv2beta2client.AutoscalingV2beta2Interface
}

func (h *hpaV2beta2Adapter) Get(
	ctx context.Context,
	namespace, name string,
	opts metav1.GetOptions,
) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	external, err := h.client.HorizontalPodAutoscalers(namespace).Get(ctx, name, opts)
	if err != nil {
		return nil, err
	}
	result := &autoscalingv2.HorizontalPodAutoscaler{}
	if err := convertHPA(external, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (h *hpaV2beta2Adapter) Create(
	ctx context.Context,
	new *autoscalingv2.HorizontalPodAutoscaler,
	opts metav1.CreateOptions,
) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	external := &autoscalingv2beta2.HorizontalPodAutoscaler{}
	if err := convertHPA(new, external); err != nil {
		return nil, err
	}
	created, err := h.client.HorizontalPodAutoscalers(external.Namespace).Create(ctx, external, opts)
	if err != nil {
		return nil, err
	}
	result := &autoscalingv2.HorizontalPodAutoscaler{}
	if err := convertHPA(created, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (h *hpaV2beta2Adapter) Patch(
	ctx context.Context,
	old, new *autoscalingv2.HorizontalPodAutoscaler,
	opts metav1.PatchOptions,
) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	oldExternal := &autoscalingv2beta2.HorizontalPodAutoscaler{}
	if err := convertHPA(old, oldExternal); err != nil {
		return nil, err
	}
	newExternal := &autoscalingv2beta2.HorizontalPodAutoscaler{}
	if err := convertHPA(new, newExternal); err != nil {
		return nil, err
	}
	patchBytes, err := createHPAPatch(
		oldExternal, newExternal, &autoscalingv2beta2.HorizontalPodAutoscaler{})
	if err != nil || general.JsonPathEmpty(patchBytes) {
		return nil, err
	}
	patched, err := h.client.HorizontalPodAutoscalers(old.Namespace).Patch(
		ctx, old.Name, types.StrategicMergePatchType, patchBytes, opts)
	if err != nil {
		return nil, err
	}
	result := &autoscalingv2.HorizontalPodAutoscaler{}
	if err := convertHPA(patched, result); err != nil {
		return nil, err
	}
	return result, nil
}

func convertHPA(from, to interface{}) error {
	data, err := json.Marshal(from)
	if err != nil {
		return fmt.Errorf("marshal HPA for conversion: %w", err)
	}
	if err := json.Unmarshal(data, to); err != nil {
		return fmt.Errorf("unmarshal converted HPA: %w", err)
	}
	return nil
}

func createHPAPatch(old, new, dataStruct interface{}) ([]byte, error) {
	oldData, err := json.Marshal(old)
	if err != nil {
		return nil, err
	}
	newData, err := json.Marshal(new)
	if err != nil {
		return nil, err
	}
	patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, dataStruct)
	if err != nil {
		return nil, fmt.Errorf("create HPA strategic merge patch: %w", err)
	}
	return patchBytes, nil
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
