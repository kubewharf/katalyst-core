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

package node

import (
	"fmt"
	"reflect"

	v1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/overcommit/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type nodeOvercommitEvent struct {
	nodeKey   string
	configKey string
	eventType
}

type eventType string

const (
	nodeEvent   eventType = "node"
	configEvent eventType = "config"
)

func (nc *NodeOvercommitController) addNodeOvercommitConfig(obj interface{}) {
	noc, ok := obj.(*v1alpha1.NodeOvercommitConfig)
	if !ok {
		klog.Errorf("cannot convert obj to *v1alpha1.NodeOvercommitConfig: %v", obj)
		return
	}

	klog.V(4).Infof("[noc] notice addition of NodeOvercommitConfig %s", native.GenerateUniqObjectNameKey(noc))
	nc.enqueueNodeOvercommitConfig(noc, configEvent)
}

func (nc *NodeOvercommitController) updateNodeOvercommitConfig(old, new interface{}) {
	noc, ok := new.(*v1alpha1.NodeOvercommitConfig)
	if !ok {
		klog.Errorf("cannot convert obj to *v1alpha1.NodeOvercommitConfig: %v", noc)
		return
	}

	klog.V(4).Infof("[noc] notice update of NodeOvercommitConfig %s", native.GenerateUniqObjectNameKey(noc))
	nc.enqueueNodeOvercommitConfig(noc, configEvent)
}

func (nc *NodeOvercommitController) deleteNodeOvercommitConfig(obj interface{}) {
	noc, ok := obj.(*v1alpha1.NodeOvercommitConfig)
	if !ok {
		klog.Errorf("cannot convert obj to *v1alpha1.NodeOvercommitConfig: %v", obj)
		return
	}
	klog.V(4).Infof("[noc] notice deletion of NodeOvercommitConfig %s", native.GenerateUniqObjectNameKey(noc))

	nc.enqueueNodeOvercommitConfig(noc, configEvent)
}

func (nc *NodeOvercommitController) addNode(obj interface{}) {
	node, ok := obj.(*v1.Node)
	if !ok {
		klog.Errorf("cannot convert obj to *v1.Node: %v", obj)
		return
	}

	klog.V(4).Infof("[noc] notice addition of Node %s", node.Name)
	nc.enqueueNode(node)
}

func (nc *NodeOvercommitController) updateNode(old, new interface{}) {
	oldNode, ok := old.(*v1.Node)
	if !ok {
		klog.Errorf("cannot convert obj to *v1.Node: %v", old)
		return
	}
	newNode, ok := new.(*v1.Node)
	if !ok {
		klog.Errorf("cannot convert obj to *v1.Node: %v", new)
		return
	}
	var (
		oldLabel, newLabel string
	)
	if len(oldNode.Labels) != 0 {
		oldLabel = oldNode.Labels[consts.NodeOvercommitSelectorKey]
	}
	if len(newNode.Labels) != 0 {
		newLabel = newNode.Labels[consts.NodeOvercommitSelectorKey]
	}
	if oldLabel == newLabel {
		return
	}

	klog.V(4).Infof("[noc] notice update of Node %s", newNode.Name)
	nc.enqueueNode(newNode)
}

func (nc *NodeOvercommitController) addCNR(obj interface{}) {
	cnr, ok := obj.(*nodev1alpha1.CustomNodeResource)
	if !ok {
		klog.Errorf("cannot convert obj to *CustomNodeResource: %v", obj)
		return
	}

	if len(cnr.Annotations) == 0 {
		klog.V(6).Infof("cnr without annotation, skip")
		return
	}
	_, cpuOk := cnr.Annotations[consts.NodeAnnotationCPUOvercommitRatioKey]
	_, memOk := cnr.Annotations[consts.NodeAnnotationMemoryOvercommitRatioKey]
	if !cpuOk && !memOk {
		klog.V(6).Infof("cnr without overcommit ratio, skip")
		return
	}

	klog.V(4).Infof("[noc] notice addition of CNR %s", cnr.Name)
	nc.enqueueCNR(cnr)
}

func (nc *NodeOvercommitController) updateCNR(old, new interface{}) {
	oldCNR, ok := old.(*nodev1alpha1.CustomNodeResource)
	if !ok {
		klog.Errorf("cannot convert obj to CustomNodeResource: %v", old)
		return
	}

	newCNR, ok := new.(*nodev1alpha1.CustomNodeResource)
	if !ok {
		klog.Errorf("cannot convert obj to CustomNodeResource: %v", new)
		return
	}

	if reflect.DeepEqual(newCNR.Annotations, oldCNR.Annotations) {
		return
	}

	nc.enqueueCNR(newCNR)
	klog.V(4).Infof("[noc] notice update of CNR %s", newCNR.Name)
}

func (nc *NodeOvercommitController) enqueueNodeOvercommitConfig(noc *v1alpha1.NodeOvercommitConfig, eventType eventType) {
	if noc == nil {
		klog.Warning("[noc] trying to enqueue a nil config")
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(noc)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", noc, err))
		return
	}

	nc.nocSyncQueue.Add(nodeOvercommitEvent{
		configKey: key,
		eventType: eventType,
	})
}

func (nc *NodeOvercommitController) enqueueNode(node *v1.Node) {
	if node == nil {
		klog.Warning("[noc] trying to enqueue a nil node")
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(node)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", node, err))
		return
	}

	nc.nocSyncQueue.Add(nodeOvercommitEvent{
		nodeKey:   key,
		eventType: nodeEvent,
	})
}

func (nc *NodeOvercommitController) enqueueCNR(cnr *nodev1alpha1.CustomNodeResource) {
	if cnr == nil {
		klog.Warning("[noc] trying to enqueue a nil cnr")
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cnr)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", cnr, err))
		return
	}

	nc.cnrSyncQueue.Add(key)
}
