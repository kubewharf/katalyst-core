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

package npd

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

func (nc *NPDController) onNodeAdd(obj interface{}) {
	node, ok := obj.(*v1.Node)
	if !ok {
		klog.Errorf("[npd] cannot convert obj to *v1.node")
		return
	}

	nc.enqueueNode(node)
}

func (nc *NPDController) onNodeUpdate(_, newObj interface{}) {
	node, ok := newObj.(*v1.Node)
	if !ok {
		klog.Errorf("[npd] cannot convert obj to *v1.node")
		return
	}

	nc.enqueueNode(node)
}

func (nc *NPDController) onNodeDelete(obj interface{}) {
	node, ok := obj.(*v1.Node)
	if !ok {
		klog.Errorf("[npd] cannot convert obj to *v1.node")
		return
	}

	err := nc.deleteNPD(node.Name)
	if err != nil {
		klog.Errorf("delete node %v fail: %v", node.Name, err)
		return
	}
	nc.metricsManager.DeleteNodeProfileStatus(node.Name)
}

func (nc *NPDController) enqueueNode(node *v1.Node) {
	if node == nil {
		klog.Warningf("[npd] enqueue a nil node")
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(node)
	if err != nil {
		klog.Errorf("[npd] couldn't get key for node: %v, err: %v", node.Name, err)
		return
	}

	nc.nodeQueue.Add(key)
}
