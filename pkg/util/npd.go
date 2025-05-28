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

package util

import (
	"encoding/json"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	NPDKind = "NodeProfileDescriptor"
)

var NPDGVR = metav1.GroupVersionResource(v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.ResourceNameKatalystNPD))

func InsertNPDScopedNodeMetrics(
	status *v1alpha1.NodeProfileDescriptorStatus,
	scopedNodeMetrics *v1alpha1.ScopedNodeMetrics,
) {
	if status == nil || scopedNodeMetrics == nil {
		return
	}

	if status.NodeMetrics == nil {
		status.NodeMetrics = []v1alpha1.ScopedNodeMetrics{}
	}

	for i := range status.NodeMetrics {
		if status.NodeMetrics[i].Scope == scopedNodeMetrics.Scope {
			status.NodeMetrics[i].Metrics = scopedNodeMetrics.Metrics
			return
		}
	}

	status.NodeMetrics = append(status.NodeMetrics, *scopedNodeMetrics)
}

func InsertNPDScopedPodMetrics(
	status *v1alpha1.NodeProfileDescriptorStatus,
	scopedPodMetrics *v1alpha1.ScopedPodMetrics,
) {
	if status == nil || scopedPodMetrics == nil {
		return
	}

	if status.PodMetrics == nil {
		status.PodMetrics = []v1alpha1.ScopedPodMetrics{}
	}

	for i := range status.PodMetrics {
		if status.PodMetrics[i].Scope == scopedPodMetrics.Scope {
			status.PodMetrics[i].PodMetrics = scopedPodMetrics.PodMetrics
			return
		}
	}

	status.PodMetrics = append(status.PodMetrics, *scopedPodMetrics)
}

// KCCTargetResourceNPD is an implementation of KCCTargetResource for NPD
type KCCTargetResourceNPD struct {
	*unstructured.Unstructured
}

var _ KCCTargetResource = &KCCTargetResourceNPD{}

func NewKCCTargetResourceNPD(obj *unstructured.Unstructured) *KCCTargetResourceNPD {
	return &KCCTargetResourceNPD{
		obj,
	}
}

func (n *KCCTargetResourceNPD) GetLabelSelector() string { return "" }

func (n *KCCTargetResourceNPD) GetPriority() int32 { return 0 }

func (n *KCCTargetResourceNPD) GetNodeNames() []string { return nil }

func (n *KCCTargetResourceNPD) GetCanary() *intstr.IntOrString { return nil }

func (n *KCCTargetResourceNPD) GetPaused() bool { return false }

func (n *KCCTargetResourceNPD) GetLastDuration() *time.Duration { return nil }

func (n *KCCTargetResourceNPD) GetRevisionHistoryLimit() int64 { return 0 }

func (n *KCCTargetResourceNPD) GetGenericStatus() configv1alpha1.GenericConfigStatus {
	return configv1alpha1.GenericConfigStatus{}
}

func (n *KCCTargetResourceNPD) SetGenericStatus(status configv1alpha1.GenericConfigStatus) {}

func (n *KCCTargetResourceNPD) GetObservedGeneration() int64 { return 0 }

func (n *KCCTargetResourceNPD) SetObservedGeneration(generation int64) {}

func (n *KCCTargetResourceNPD) GetCollisionCount() *int32 { return nil }

func (n *KCCTargetResourceNPD) SetCollisionCount(count *int32) {}

func (n *KCCTargetResourceNPD) CheckValid() bool { return true }

func (n *KCCTargetResourceNPD) CheckExpired(now time.Time) bool { return false }

func (n *KCCTargetResourceNPD) IsUpdated() bool { return false }

func (n *KCCTargetResourceNPD) NeedValidateKCC() bool { return false }

func (n *KCCTargetResourceNPD) IsPerNode() bool { return true }

func (n *KCCTargetResourceNPD) GetUnstructured() *unstructured.Unstructured { return n.Unstructured }

func (n *KCCTargetResourceNPD) Unmarshal(conf interface{}) error {
	if n.Unstructured == nil || n.Object == nil {
		return nil
	}
	return runtime.DefaultUnstructuredConverter.FromUnstructured(n.Object, conf)
}

func (n *KCCTargetResourceNPD) DeepCopy() KCCTargetResource {
	return &KCCTargetResourceNPD{
		n.Unstructured.DeepCopy(),
	}
}

func (n *KCCTargetResourceNPD) GenerateConfigHash() (string, error) {
	const npdConfigHashLength = 12
	val, _, err := unstructured.NestedFieldCopy(n.Object, consts.ObjectFieldNameStatus)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(val)
	if err != nil {
		return "", err
	}

	return general.GenerateHash(data, npdConfigHashLength), nil
}
