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

package evictionmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	controllerutil "k8s.io/kubernetes/pkg/controller/util/node"
	taintutils "k8s.io/kubernetes/pkg/util/taints"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	reporterpluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/utils"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	metricsNameCNRTaint = "cnr_taint"
)

var validNodeTaintEffects = sets.NewString(
	string(v1.TaintEffectNoSchedule),
	string(v1.TaintEffectPreferNoSchedule),
	string(v1.TaintEffectNoExecute),
)

var validCNRTaintEffectQoSPrefixes = sets.NewString(
	string(apiconsts.QoSLevelReclaimedCores),
)

func getTaintKeyFromConditionName(conditionName string) string {
	return fmt.Sprintf("%s/%s", consts.KatalystNodeDomainPrefix, conditionName)
}

func isEvictionManagerTaint(t *v1.Taint) bool {
	if t == nil {
		return false
	}

	return strings.HasPrefix(t.Key, consts.KatalystNodeDomainPrefix)
}

func (m *EvictionManger) getNodeTaintsFromConditions() []v1.Taint {
	m.conditionLock.RLock()
	defer m.conditionLock.RUnlock()

	taints := make([]v1.Taint, 0, len(m.conditions))

	for conditionName, condition := range m.conditions {
		if condition == nil {
			continue
		} else if condition.ConditionType != pluginapi.ConditionType_NODE_CONDITION {
			continue
		}

		vis := make(map[string]bool)
		taintKey := getTaintKeyFromConditionName(conditionName)
		for _, effect := range condition.Effects {
			if !validNodeTaintEffects.Has(effect) {
				klog.Errorf("[eviction manager] invalid node taint effect: %s for condition: %s", effect, conditionName)
				continue
			} else if vis[effect] {
				klog.Warningf("[eviction manager] found repeated node taint effect: %s in condition: %s", effect, conditionName)
				continue
			}

			vis[effect] = true

			taints = append(taints, v1.Taint{
				Key:    taintKey,
				Effect: v1.TaintEffect(effect),
			})
		}
	}

	return taints
}

func (m *EvictionManger) getCNRTaintsFromConditions() []v1alpha1.Taint {
	m.conditionLock.RLock()
	defer m.conditionLock.RUnlock()

	taints := make([]v1alpha1.Taint, 0, len(m.conditions))

	for conditionName, condition := range m.conditions {
		if condition == nil {
			continue
		} else if condition.ConditionType != pluginapi.ConditionType_CNR_CONDITION {
			continue
		}

		vis := make(map[v1.TaintEffect]bool)
		taintKey := getTaintKeyFromConditionName(conditionName)
		for _, conditionEffect := range condition.Effects {
			qosLevel, effect, err := utils.ParseConditionEffect(conditionEffect)
			if err != nil {
				klog.Errorf("[eviction manager] parse condition effect %s failed with error: %v", conditionEffect, err)
				continue
			}

			if !validCNRTaintEffectQoSPrefixes.Has(string(qosLevel)) {
				klog.Errorf("[eviction manager] invalid cnr taint qos level: %s for condition: %s", qosLevel, conditionName)
				continue
			}

			if !validNodeTaintEffects.Has(string(effect)) {
				klog.Errorf("[eviction manager] invalid node taint effect: %s for condition: %s", effect, conditionName)
				continue
			} else if vis[effect] {
				klog.Warningf("[eviction manager] found repeated node taint effect: %s in condition: %s", effect, conditionName)
				continue
			}

			vis[effect] = true

			taints = append(taints, v1alpha1.Taint{
				QoSLevel: qosLevel,
				Taint: v1.Taint{
					Key:    taintKey,
					Effect: effect,
				},
			})
		}
	}

	return taints
}

func (m *EvictionManger) reportConditionsAsNodeTaints(ctx context.Context) {
	var err error
	defer func() {
		_ = general.UpdateHealthzStateByError(reportTaintHealthCheckName, err)
	}()
	node, err := m.metaGetter.GetNode(ctx)
	if err != nil {
		klog.Errorf("[eviction manager] get node failed with error: %v", err)
		return
	}

	taints := m.getNodeTaintsFromConditions()

	// Get exist taints of node.
	nodeTaints := taintutils.TaintSetFilter(node.Spec.Taints, isEvictionManagerTaint)
	taintsToAdd, taintsToDel := taintutils.TaintSetDiff(taints, nodeTaints)
	// If nothing to add not delete, return true directly.
	if len(taintsToAdd) == 0 && len(taintsToDel) == 0 {
		klog.Infof("[eviction manager] there is no node taint to deal with")
		return
	}

	klog.Infof("[eviction manager] node taintsToAdd: %s\n taintsToDel: %s", nodeTaintsLog(taintsToAdd), nodeTaintsLog(taintsToDel))

	if !controllerutil.SwapNodeControllerTaint(ctx, m.genericClient.KubeClient, taintsToAdd, taintsToDel, node) {
		klog.Errorf("failed to swap taints")
		err = fmt.Errorf("failed to swap taints")
	}

	return
}

func (m *EvictionManger) reportConditionsAsCNRTaints(ctx context.Context) {
	var err error
	defer func() {
		_ = general.UpdateHealthzStateByError(reportCNRTaintHealthCheckName, err)
	}()

	cnr, err := m.metaGetter.CNRFetcher.GetCNR(ctx)
	if err != nil {
		klog.Errorf("[eviction manager] get cnr failed with error: %v", err)
		return
	}

	taints := m.getCNRTaintsFromConditions()

	// Get exist taints of cnr.
	// We should not delete taints that are not managed by eviction manager.
	currentEvictionManagerCNRTaints := util.CNRTaintSetFilter(cnr.Spec.Taints, util.IsManagedByReporterCNRTaint)
	taintsToAdd, taintsToDel := util.CNRTaintSetDiff(taints, currentEvictionManagerCNRTaints)
	if len(taintsToAdd) != 0 || len(taintsToDel) != 0 {
		klog.Infof("[eviction manager] node taintsToAdd: %s\n taintsToDel: %s", cnrTaintsLog(taintsToAdd), cnrTaintsLog(taintsToDel))
	}

	// Add timeAdded to taintsToAdd.
	for _, taintToAdd := range taintsToAdd {
		now := metav1.Now()
		taintToAdd.TimeAdded = &now
	}

	m.emmitCNRTaintMetrics(taints)

	err = m.reportCNRTaints(ctx, taints)
	if err != nil {
		klog.Errorf("[eviction manager] failed to report cnr taints: %v", err)
		return
	}
}

func (m *EvictionManger) reportCNRTaints(ctx context.Context, taints []v1alpha1.Taint) error {
	valueTaints, err := json.Marshal(&taints)
	if err != nil {
		return errors.Wrap(err, "marshal topology policy failed")
	}

	contents := []*reporterpluginapi.ReportContent{
		{
			GroupVersionKind: &util.CNRGroupVersionKind,
			Field: []*reporterpluginapi.ReportField{
				{
					FieldType: reporterpluginapi.FieldType_Spec,
					FieldName: util.CNRFieldNameTaints,
					Value:     valueTaints,
				},
			},
		},
	}

	return m.cnrTaintReporter.ReportContents(ctx, contents, true)
}

func (m *EvictionManger) emmitCNRTaintMetrics(taints []v1alpha1.Taint) {
	for _, taint := range taints {
		_ = m.emitter.StoreInt64(metricsNameCNRTaint, 1, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "qos_level", Val: string(taint.QoSLevel)},
			metrics.MetricTag{Key: "key", Val: taint.Key},
			metrics.MetricTag{Key: "effect", Val: string(taint.Effect)})
	}
}

func nodeTaintsLog(taints []*v1.Taint) string {
	var buf bytes.Buffer

	for i, taint := range taints {

		if taint == nil {
			continue
		}

		infoStr := fmt.Sprintf("key: %s, effect: %s", taint.Key, taint.Effect)
		if i == 0 {
			buf.WriteString(infoStr)
		} else {
			buf.WriteString("; " + infoStr)
		}
	}

	return buf.String()
}

func cnrTaintsLog(taints []*v1alpha1.Taint) string {
	var buf bytes.Buffer

	for i, taint := range taints {
		if taint == nil {
			continue
		}

		infoStr := fmt.Sprintf("key: %s, effect: %s, qos: %s", taint.Key, taint.Effect, taint.QoSLevel)
		if i == 0 {
			buf.WriteString(infoStr)
		} else {
			buf.WriteString("; " + infoStr)
		}
	}

	return buf.String()
}
