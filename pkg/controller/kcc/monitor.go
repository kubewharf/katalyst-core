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

package kcc

import (
	configapis "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	kccutil "github.com/kubewharf/katalyst-core/pkg/controller/kcc/target"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

const (
	metricsNameSyncCNCCost       = "sync_cnc_cost"
	metricsNameSyncKCCCost       = "sync_kcc_cost"
	metricsNameSyncKCCTargetCost = "sync_kcc_target_cost"

	metricsNameKCCCount              = "kcc_count"
	metricsNameKCCInvalid            = "kcc_invalid"
	metricsNameKCCGeneration         = "kcc_generation"
	metricsNameKCCObservedGeneration = "kcc_observed_generation"

	metricsNameKCCTargetCount = "kcc_target_count"
)

func (k *KatalystCustomConfigController) monitor() {
	kccList, err := k.katalystCustomConfigLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("list kcc failed: %s", err)
		return
	}

	for _, kcc := range kccList {
		var isValid, reason string
		ok, valid := getKCCCondition(kcc, configapis.KatalystCustomConfigConditionTypeValid)
		if !ok {
			isValid = "unknown"
			reason = "unknown"
		} else {
			isValid = string(valid.Status)
			reason = valid.Reason
		}

		baseTag := metrics.ConvertMapToTags(map[string]string{
			"name":      kcc.Name,
			"namespace": kcc.Namespace,
			"group":     kcc.Spec.TargetType.Group,
			"version":   kcc.Spec.TargetType.Version,
			"resource":  kcc.Spec.TargetType.Resource,
			"isValid":   isValid,
			"reason":    reason,
		})

		_ = k.metricsEmitter.StoreInt64(metricsNameKCCCount, 1, metrics.MetricTypeNameRaw, baseTag...)
		_ = k.metricsEmitter.StoreInt64(metricsNameKCCInvalid, int64(len(kcc.Status.InvalidTargetConfigList)), metrics.MetricTypeNameRaw, baseTag...)
		_ = k.metricsEmitter.StoreInt64(metricsNameKCCGeneration, kcc.Generation, metrics.MetricTypeNameRaw, baseTag...)
		_ = k.metricsEmitter.StoreInt64(metricsNameKCCObservedGeneration, kcc.Status.ObservedGeneration, metrics.MetricTypeNameRaw, baseTag...)
	}
}

func (k *KatalystCustomConfigTargetController) monitor() {
	now := time.Now()
	k.targetHandler.RangeGVRTargetAccessor(func(gvr metav1.GroupVersionResource, accessor kccutil.KatalystCustomConfigTargetAccessor) bool {
		objList, err := accessor.List(labels.Everything())
		if err != nil {
			return false
		}

		for _, obj := range objList {
			kccTarget := util.ToKCCTargetResource(obj)
			baseTag := metrics.ConvertMapToTags(map[string]string{
				"name":      kccTarget.GetName(),
				"namespace": kccTarget.GetNamespace(),
				"group":     gvr.Group,
				"version":   gvr.Version,
				"resource":  gvr.Resource,
				"isExpired": strconv.FormatBool(kccTarget.CheckExpired(now)),
				"isValid":   strconv.FormatBool(kccTarget.CheckValid()),
				"level":     getKCCTargetLevel(kccTarget),
			})

			_ = k.metricsEmitter.StoreInt64(metricsNameKCCTargetCount, 1, metrics.MetricTypeNameRaw, baseTag...)
		}

		return true
	})
}

func getKCCTargetLevel(target util.KCCTargetResource) string {
	if len(target.GetNodeNames()) > 0 {
		return "node"
	} else if len(target.GetLabelSelector()) > 0 {
		return "label"
	} else {
		return "global"
	}
}

func getKCCCondition(kcc *configapis.KatalystCustomConfig,
	conditionType configapis.KatalystCustomConfigConditionType) (bool, *configapis.KatalystCustomConfigCondition) {
	for _, condition := range kcc.Status.Conditions {
		if condition.Type == conditionType {
			return true, &condition
		}
	}
	return false, nil
}
