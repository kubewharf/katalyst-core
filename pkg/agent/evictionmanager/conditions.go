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
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	controllerutil "k8s.io/kubernetes/pkg/controller/util/node"
	taintutils "k8s.io/kubernetes/pkg/util/taints"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var validNodeTaintEffects = sets.NewString(
	string(v1.TaintEffectNoSchedule),
	string(v1.TaintEffectPreferNoSchedule),
	string(v1.TaintEffectNoExecute),
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
