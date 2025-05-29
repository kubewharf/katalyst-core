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
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	kccConfigHashLength = 12
)

type KCCTargetResource interface {
	metav1.Object
	GetUnstructured() *unstructured.Unstructured
	GetLabelSelector() string
	GetPriority() int32
	GetNodeNames() []string
	GetCanary() *intstr.IntOrString
	GetPaused() bool
	GetLastDuration() *time.Duration
	GetRevisionHistoryLimit() int64
	Unmarshal(conf interface{}) error
	GetGenericStatus() v1alpha1.GenericConfigStatus
	SetGenericStatus(status v1alpha1.GenericConfigStatus)
	GetObservedGeneration() int64
	SetObservedGeneration(generation int64)
	GetCollisionCount() *int32
	SetCollisionCount(count *int32)
	GenerateConfigHash() (string, error)
	DeepCopy() KCCTargetResource
	CheckValid() bool
	CheckExpired(now time.Time) bool
	IsUpdated() bool
	NeedValidateKCC() bool
	IsPerNode() bool
}

// KCCTargetResourceGeneral is used to provide util function to get detailed information
// about KCC Target fields.
type KCCTargetResourceGeneral struct {
	*unstructured.Unstructured
}

func ToKCCTargetResource(obj *unstructured.Unstructured) KCCTargetResource {
	switch obj.GetKind() {
	case NPDKind:
		return &KCCTargetResourceNPD{
			obj,
		}
	default:
		return &KCCTargetResourceGeneral{
			obj,
		}
	}
}

func (g KCCTargetResourceGeneral) GetUnstructured() *unstructured.Unstructured {
	return g.Unstructured
}

func (g KCCTargetResourceGeneral) GetLabelSelector() string {
	labelSelector, _, _ := unstructured.NestedString(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldNameLabelSelector)
	return labelSelector
}

func (g KCCTargetResourceGeneral) GetPriority() int32 {
	priority, _, _ := unstructured.NestedInt64(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldNamePriority)
	return int32(priority)
}

func (g KCCTargetResourceGeneral) GetNodeNames() []string {
	nodeNames, _, _ := unstructured.NestedStringSlice(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldEphemeralSelector, consts.KCCTargetConfFieldNameNodeNames)
	return nodeNames
}

func (g KCCTargetResourceGeneral) GetCanary() *intstr.IntOrString {
	canary, exists, _ := unstructured.NestedFieldCopy(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldNameUpdateStrategy, consts.KCCTargetConfFieldNameRollingUpdate, consts.KCCTargetConfFieldNameCanary)
	if !exists {
		return nil
	}
	ret := intstr.Parse(fmt.Sprint(canary))
	return &ret
}

func (g KCCTargetResourceGeneral) GetPaused() bool {
	canary, _, _ := unstructured.NestedBool(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldNamePaused)
	return canary
}

func (g KCCTargetResourceGeneral) GetLastDuration() *time.Duration {
	val, _, _ := unstructured.NestedFieldCopy(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldEphemeralSelector, consts.KCCTargetConfFieldNameLastDuration)
	if val == nil {
		return nil
	}
	d, _ := time.ParseDuration(val.(string))
	return &d
}

func (g KCCTargetResourceGeneral) GetRevisionHistoryLimit() int64 {
	val, _, _ := unstructured.NestedInt64(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldNameRevisionHistoryLimit)
	return val
}

func (g KCCTargetResourceGeneral) Unmarshal(conf interface{}) error {
	if g.Unstructured == nil || g.Object == nil {
		return nil
	}

	err := runtime.DefaultUnstructuredConverter.FromUnstructured(g.Object, conf)
	if err != nil {
		return err
	}

	return nil
}

func (g KCCTargetResourceGeneral) GetGenericStatus() v1alpha1.GenericConfigStatus {
	val, _, _ := unstructured.NestedFieldCopy(g.Object, consts.ObjectFieldNameStatus)
	if val == nil {
		return v1alpha1.GenericConfigStatus{}
	}

	status := &v1alpha1.GenericConfigStatus{}
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(val.(map[string]interface{}), status)

	return *status
}

func (g KCCTargetResourceGeneral) SetGenericStatus(status v1alpha1.GenericConfigStatus) {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&status)
	if err != nil {
		return
	}

	oldStatus, ok, _ := unstructured.NestedFieldNoCopy(g.Object, consts.ObjectFieldNameStatus)
	if !ok {
		_ = unstructured.SetNestedField(g.Object, obj, consts.ObjectFieldNameStatus)
		return
	}

	for key, val := range obj {
		oldStatus.(map[string]interface{})[key] = val
	}
}

func (g KCCTargetResourceGeneral) GetObservedGeneration() int64 {
	val, _, _ := unstructured.NestedInt64(g.Object, consts.ObjectFieldNameStatus, consts.KCCTargetConfFieldNameObservedGeneration)
	return val
}

func (g KCCTargetResourceGeneral) SetObservedGeneration(generation int64) {
	_ = unstructured.SetNestedField(g.Object, generation, consts.ObjectFieldNameStatus, consts.KCCTargetConfFieldNameObservedGeneration)
}

func (g KCCTargetResourceGeneral) GetCollisionCount() *int32 {
	val, _, _ := unstructured.NestedFieldCopy(g.Object, consts.ObjectFieldNameStatus, consts.KCCTargetConfFieldNameCollisionCount)
	if val == nil {
		return nil
	}
	ret := int32(val.(int64))
	return &ret
}

func (g KCCTargetResourceGeneral) SetCollisionCount(count *int32) {
	if count == nil {
		unstructured.RemoveNestedField(g.Object, consts.ObjectFieldNameStatus, consts.KCCTargetConfFieldNameCollisionCount)
	} else {
		_ = unstructured.SetNestedField(g.Object, int64(*count), consts.ObjectFieldNameStatus, consts.KCCTargetConfFieldNameCollisionCount)
	}
}

func (g KCCTargetResourceGeneral) GenerateConfigHash() (string, error) {
	val, _, err := unstructured.NestedFieldCopy(g.Object, consts.ObjectFieldNameSpec, consts.KCCTargetConfFieldNameConfig)
	if err != nil {
		return "", err
	}

	if val == nil {
		val = make(map[string]interface{})
	}

	genericStatus := g.GetGenericStatus()
	genericStatusObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&genericStatus)
	if err != nil {
		return "", err
	}

	status, ok, _ := unstructured.NestedFieldNoCopy(g.Object, consts.ObjectFieldNameStatus)
	if ok {
		for k := range genericStatusObj {
			delete(status.(map[string]interface{}), k)
		}

		// add status field to consider
		for k, v := range status.(map[string]interface{}) {
			val.(map[string]interface{})[fmt.Sprintf("%s/%s", consts.ObjectFieldNameStatus, k)] = v
		}
	}

	data, err := json.Marshal(val)
	if err != nil {
		return "", err
	}

	return general.GenerateHash(data, kccConfigHashLength), nil
}

func (g KCCTargetResourceGeneral) DeepCopy() KCCTargetResource {
	return &KCCTargetResourceGeneral{
		g.Unstructured.DeepCopy(),
	}
}

func (g KCCTargetResourceGeneral) CheckValid() bool {
	status := g.GetGenericStatus()
	for _, c := range status.Conditions {
		if c.Type == v1alpha1.ConfigConditionTypeValid && c.Status == v1.ConditionTrue {
			return true
		}
	}

	return false
}

func (g KCCTargetResourceGeneral) CheckExpired(now time.Time) bool {
	lastDuration := g.GetLastDuration()
	if lastDuration != nil && g.GetCreationTimestamp().Add(*lastDuration).Before(now) {
		return true
	}

	return false
}

func (g KCCTargetResourceGeneral) IsUpdated() bool {
	if g.GetGeneration() == g.GetObservedGeneration() {
		return true
	}
	return false
}

func (g KCCTargetResourceGeneral) NeedValidateKCC() bool {
	return true
}

func (g KCCTargetResourceGeneral) IsPerNode() bool {
	return false
}
