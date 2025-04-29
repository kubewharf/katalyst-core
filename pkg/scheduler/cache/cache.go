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

package cache

import (
	"sync"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
)

type extendedCache struct {
	// This mutex guards all fields within this extendedCache struct.
	mu    sync.RWMutex
	nodes map[string]*NodeInfo
}

var cache *extendedCache

func init() {
	cache = &extendedCache{
		nodes: make(map[string]*NodeInfo),
	}
}

func GetCache() *extendedCache {
	return cache
}

func (cache *extendedCache) AddPod(pod *v1.Pod) error {
	key, err := framework.GetPodKey(pod)
	if err != nil {
		return err
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	n, ok := cache.nodes[pod.Spec.NodeName]
	if !ok {
		n = NewNodeInfo()
		cache.nodes[pod.Spec.NodeName] = n
	}
	n.AddPod(key, pod)

	return nil
}

func (cache *extendedCache) RemovePod(pod *v1.Pod) error {
	key, err := framework.GetPodKey(pod)
	if err != nil {
		return err
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	n, ok := cache.nodes[pod.Spec.NodeName]
	if !ok {
		klog.ErrorS(nil, "Node not found when trying to remove pod", "node", klog.KRef("", pod.Spec.NodeName), "pod", klog.KObj(pod))
	} else {
		n.RemovePod(key, pod)
		n.DeleteAssumedPod(pod)
	}

	return nil
}

func (cache *extendedCache) AddOrUpdateCNR(cnr *apis.CustomNodeResource) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	n, ok := cache.nodes[cnr.Name]
	if !ok {
		n = NewNodeInfo()
		cache.nodes[cnr.Name] = n
	}
	n.UpdateNodeInfo(cnr)
}

// RemoveCNR removes a Node from the cache.
func (cache *extendedCache) RemoveCNR(cnr *apis.CustomNodeResource) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	delete(cache.nodes, cnr.Name)
}

// GetNodeInfo returns the NodeInfo.
func (cache *extendedCache) GetNodeInfo(name string) (*NodeInfo, error) {
	cache.mu.RLock()
	defer cache.mu.RUnlock()

	nodeInfo, ok := cache.nodes[name]
	if !ok {
		return nil, errors.New("node not found in the extended cache")
	}

	return nodeInfo, nil
}

func (cache *extendedCache) ReserveNodeResource(nodeName string, pod *v1.Pod) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	nodeInfo, ok := cache.nodes[nodeName]
	if !ok {
		nodeInfo = NewNodeInfo()
	}

	nodeInfo.AddAssumedPod(pod)
	cache.nodes[nodeName] = nodeInfo
}

func (cache *extendedCache) UnreserveNodeResource(nodeName string, pod *v1.Pod) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	nodeInfo, ok := cache.nodes[nodeName]
	if !ok {
		klog.Warningf("UnreserveNodeResource fail, node %v not exist in extendedCache", nodeName)
		return
	}

	nodeInfo.DeleteAssumedPod(pod)
}

// GetNodeResourceTopology assumedPodResource will be added to nodeResourceTopology
func (cache *extendedCache) GetNodeResourceTopology(nodeName string, filterFn podFilter) *ResourceTopology {
	cache.mu.RLock()
	defer cache.mu.RUnlock()

	nodeInfo, ok := cache.nodes[nodeName]
	if !ok {
		return nil
	}
	return nodeInfo.GetResourceTopologyCopy(filterFn)
}
