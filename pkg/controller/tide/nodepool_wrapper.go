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

package tide

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/tide/v1alpha1"
)

var (
	labelPrefix = apis.SchemeGroupVersion.Group

	AnnotationNodePoolUpdate = labelPrefix + "/" + "update"

	LabelPodTypeKey     = labelPrefix + "/" + "pod-type"
	LabelOnlinePodValue = "online"

	LabelNodePoolKey = labelPrefix + "/" + "node-pool"

	LabelNodeTypeKey      = labelPrefix + "/" + "node-type"
	LabelOnlineNodeValue  = "online"
	LabelOfflineNodeValue = "offline"
	LabelTideNode         = labelPrefix + "/" + "tide"
	LabelReverseNode      = labelPrefix + "/" + "reverse"

	TaintEvictOnlinePodKey  = labelPrefix + "/" + "online-not-used"
	TaintEvictOfflinePodKey = labelPrefix + "/" + "offline-not-used"

	// NodePoolFinalizer is the finalizer name for LogRule operator
	NodePoolFinalizer = labelPrefix + "/" + "finalizer"
)

type NodePoolWrapper interface {
	GetOnlineReverseNodeSelector() labels.Selector
	GetOfflineReverseNodeSelector() labels.Selector
	GetOnlineTideNodeSelector() labels.Selector
	GetOfflineTideNodeSelector() labels.Selector
	GetTideNodeSelector() labels.Selector
	GetNodePoolSelector() *labels.Requirement

	SetNodeToOnlineReverse(node *corev1.Node)
	SetNodeToOfflineReverse(node *corev1.Node)
	SetNodeToTide(node *corev1.Node)
	SetNodeToTideOnline(node *corev1.Node)

	GetEvictOnlinePodTaint() apis.TaintOption
	GetEvictOfflinePodTaint() apis.TaintOption
	GetOnlineLabel() apis.LabelOption
	GetOfflineLabel() apis.LabelOption
	GetTideLabel() apis.LabelOption
	GetNodeSelector() map[string]string

	metav1.Object
}

type nodePoolWrapperImpl struct {
	*apis.TideNodePool
}

func (n nodePoolWrapperImpl) GetNodeSelector() map[string]string {
	return n.Spec.NodeConfigs.NodeSelector
}

func (n nodePoolWrapperImpl) GetOnlineReverseNodeSelector() labels.Selector {
	return labels.SelectorFromSet(map[string]string{
		n.GetOnlineLabel().Key: n.GetOnlineLabel().Value,
		LabelNodePoolKey:       n.GetName(),
		LabelReverseNode:       "true",
	})
}

func (n nodePoolWrapperImpl) GetOfflineReverseNodeSelector() labels.Selector {
	return labels.SelectorFromSet(map[string]string{
		n.GetOfflineLabel().Key: n.GetOfflineLabel().Value,
		LabelNodePoolKey:        n.GetName(),
		LabelReverseNode:        "true",
	})
}

func (n nodePoolWrapperImpl) GetOnlineTideNodeSelector() labels.Selector {
	return labels.SelectorFromSet(map[string]string{
		LabelNodePoolKey:       n.GetName(),
		n.GetOnlineLabel().Key: n.GetOnlineLabel().Value,
		n.GetTideLabel().Key:   n.GetTideLabel().Value,
	})
}

func (n nodePoolWrapperImpl) GetOfflineTideNodeSelector() labels.Selector {
	return labels.SelectorFromSet(map[string]string{
		LabelNodePoolKey:        n.GetName(),
		n.GetOfflineLabel().Key: n.GetOfflineLabel().Value,
		n.GetTideLabel().Key:    n.GetTideLabel().Value,
	})
}

func (n nodePoolWrapperImpl) GetTideNodeSelector() labels.Selector {
	return labels.SelectorFromSet(map[string]string{
		LabelNodePoolKey:     n.GetName(),
		n.GetTideLabel().Key: n.GetTideLabel().Value,
	})
}

func (n nodePoolWrapperImpl) GetNodePoolSelector() *labels.Requirement {
	nodePoolSelector, _ := labels.NewRequirement(LabelNodePoolKey, selection.Exists, nil)
	return nodePoolSelector
}

func (n nodePoolWrapperImpl) SetNodeToOnlineReverse(node *corev1.Node) {
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	node.Labels[LabelNodePoolKey] = n.GetName()
	node.Labels[n.GetOnlineLabel().Key] = n.GetOnlineLabel().Value
	node.Labels[LabelReverseNode] = "true"
	delete(node.Labels, n.GetTideLabel().Key)
}

func (n nodePoolWrapperImpl) SetNodeToOfflineReverse(node *corev1.Node) {
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	node.Labels[LabelNodePoolKey] = n.GetName()
	node.Labels[n.GetOfflineLabel().Key] = n.GetOfflineLabel().Value
	node.Labels[LabelReverseNode] = "true"
	delete(node.Labels, n.GetTideLabel().Key)
}

func (n nodePoolWrapperImpl) SetNodeToTide(node *corev1.Node) {
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	node.Labels[LabelNodePoolKey] = n.GetName()
	node.Labels[LabelReverseNode] = "false"
	node.Labels[n.GetTideLabel().Key] = n.GetTideLabel().Value
}

func (n nodePoolWrapperImpl) SetNodeToTideOnline(node *corev1.Node) {
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	node.Labels[LabelReverseNode] = "false"
	node.Labels[LabelNodePoolKey] = n.GetName()
	node.Labels[n.GetOnlineLabel().Key] = n.GetOnlineLabel().Value
	node.Labels[n.GetTideLabel().Key] = n.GetTideLabel().Value
}

func (n nodePoolWrapperImpl) GetEvictOnlinePodTaint() apis.TaintOption {
	if n.Spec.EvictStrategy.WaterFlow.EvictOnlinePodTaint == nil {
		return apis.TaintOption{
			Key:    TaintEvictOnlinePodKey,
			Value:  "true",
			Effect: string(corev1.TaintEffectNoExecute),
		}
	}
	return *n.Spec.EvictStrategy.WaterFlow.EvictOnlinePodTaint
}

func (n nodePoolWrapperImpl) GetEvictOfflinePodTaint() apis.TaintOption {
	if n.Spec.EvictStrategy.WaterFlow.EvictOfflinePodTaint == nil {
		return apis.TaintOption{
			Key:    TaintEvictOfflinePodKey,
			Value:  "true",
			Effect: string(corev1.TaintEffectNoExecute),
		}
	}
	return *n.Spec.EvictStrategy.WaterFlow.EvictOfflinePodTaint
}

func (n nodePoolWrapperImpl) GetOnlineLabel() apis.LabelOption {
	if n.Spec.NodeConfigs.OnlineLabel == nil {
		return apis.LabelOption{
			Key:   LabelNodeTypeKey,
			Value: LabelOnlineNodeValue,
		}
	}
	return *n.Spec.NodeConfigs.OnlineLabel
}

func (n nodePoolWrapperImpl) GetOfflineLabel() apis.LabelOption {
	if n.Spec.NodeConfigs.OfflineLabel == nil {
		return apis.LabelOption{
			Key:   LabelNodeTypeKey,
			Value: LabelOfflineNodeValue,
		}
	}
	return *n.Spec.NodeConfigs.OfflineLabel
}

func (n nodePoolWrapperImpl) GetTideLabel() apis.LabelOption {
	if n.Spec.NodeConfigs.TideLabel == nil {
		return apis.LabelOption{
			Key:   LabelTideNode,
			Value: "true",
		}
	}
	return *n.Spec.NodeConfigs.TideLabel
}

func NewNodePoolWrapper(nodePool *apis.TideNodePool) NodePoolWrapper {
	return &nodePoolWrapperImpl{TideNodePool: nodePool}
}
