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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	kccConfigHashLength = 12
)

// KCCTargetResource is used to provide util function to get detailed information
// about KCC Target fields.
type KCCTargetResource struct {
	*unstructured.Unstructured
}

func ToKCCTargetResource(obj *unstructured.Unstructured) KCCTargetResource {
	return KCCTargetResource{
		obj,
	}
}

func (g KCCTargetResource) GetHash() string {
	annotations := g.GetAnnotations()
	if hash, ok := annotations[consts.KatalystCustomConfigAnnotationKeyConfigHash]; ok {
		return hash
	}
	return ""
}

func (g KCCTargetResource) SetHash(hash string) {
	annotations := g.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[consts.KatalystCustomConfigAnnotationKeyConfigHash] = hash
	g.SetAnnotations(annotations)
}

func (g KCCTargetResource) GetLabelSelector() string {
	labelSelector, _, _ := unstructured.NestedString(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldNameLabelSelector)
	return labelSelector
}

func (g KCCTargetResource) GetPriority() int32 {
	priority, _, _ := unstructured.NestedInt64(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldNamePriority)
	return int32(priority)
}

func (g KCCTargetResource) GetNodeNames() []string {
	nodeNames, _, _ := unstructured.NestedStringSlice(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldEphemeralSelector, consts.KCCTargetConfFieldNameNodeNames)
	return nodeNames
}

func (g KCCTargetResource) GetLastDuration() *time.Duration {
	val, _, _ := unstructured.NestedFieldCopy(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldEphemeralSelector, consts.KCCTargetConfFieldNameLastDuration)
	if val == nil {
		return nil
	}
	d, _ := time.ParseDuration(val.(string))
	return &d
}

func (g KCCTargetResource) GetRevisionHistoryLimit() int64 {
	val, _, _ := unstructured.NestedInt64(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldNameRevisionHistoryLimit)
	return val
}

func (g KCCTargetResource) Unmarshal(conf interface{}) error {
	if g.Unstructured == nil || g.Object == nil {
		return nil
	}

	err := runtime.DefaultUnstructuredConverter.FromUnstructured(g.Object, conf)
	if err != nil {
		return err
	}

	return nil
}

func (g KCCTargetResource) GetGenericStatus() v1alpha1.GenericConfigStatus {
	val, _, _ := unstructured.NestedFieldCopy(g.Object, consts.ObjectFieldNameStatus)
	if val == nil {
		return v1alpha1.GenericConfigStatus{}
	}

	status := &v1alpha1.GenericConfigStatus{}
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(val.(map[string]interface{}), status)

	return *status
}

func (g KCCTargetResource) SetGenericStatus(status v1alpha1.GenericConfigStatus) {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&status)
	if err != nil {
		return
	}
	_ = unstructured.SetNestedField(g.Object, obj, consts.ObjectFieldNameStatus)
}

func (g KCCTargetResource) GetObservedGeneration() int64 {
	val, _, _ := unstructured.NestedInt64(g.Object, consts.ObjectFieldNameStatus, consts.KCCTargetConfFieldNameObservedGeneration)
	return val
}

func (g KCCTargetResource) SetObservedGeneration(generation int64) {
	_ = unstructured.SetNestedField(g.Object, generation, consts.ObjectFieldNameStatus, consts.KCCTargetConfFieldNameObservedGeneration)
}

func (g KCCTargetResource) GetCollisionCount() *int32 {
	val, _, _ := unstructured.NestedFieldCopy(g.Object, consts.ObjectFieldNameStatus, consts.KCCTargetConfFieldNameCollisionCount)
	if val == nil {
		return nil
	}
	ret := int32(val.(int64))
	return &ret
}

func (g KCCTargetResource) SetCollisionCount(count *int32) {
	if count == nil {
		unstructured.RemoveNestedField(g.Object, consts.ObjectFieldNameStatus, consts.KCCTargetConfFieldNameCollisionCount)
	} else {
		_ = unstructured.SetNestedField(g.Object, int64(*count), consts.ObjectFieldNameStatus, consts.KCCTargetConfFieldNameCollisionCount)
	}
}

func (g KCCTargetResource) GenerateConfigHash() (string, error) {
	val, _, err := unstructured.NestedFieldCopy(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldNameConfig)
	if err != nil {
		return "", err
	}

	data, err := json.Marshal(val)
	if err != nil {
		return "", err
	}

	return general.GenerateHash(data, kccConfigHashLength), nil
}

func (g KCCTargetResource) DeepCopy() KCCTargetResource {
	return KCCTargetResource{
		g.Unstructured.DeepCopy(),
	}
}

func (g KCCTargetResource) CheckValid() bool {
	status := g.GetGenericStatus()
	for _, c := range status.Conditions {
		if c.Type == v1alpha1.ConfigConditionTypeValid && c.Status == v1.ConditionTrue {
			return true
		}
	}

	return false
}

func (g KCCTargetResource) CheckExpired(now time.Time) bool {
	lastDuration := g.GetLastDuration()
	if lastDuration != nil && g.GetCreationTimestamp().Add(*lastDuration).Before(now) {
		return true
	}

	return false
}

func (g KCCTargetResource) IsUpdated() bool {
	if g.GetGeneration() == g.GetObservedGeneration() {
		return true
	}
	return false
}
