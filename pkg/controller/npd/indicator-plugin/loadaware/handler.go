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

package loadaware

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func (p *Plugin) OnNodeAdd(obj interface{}) {
	node, ok := obj.(*v1.Node)
	if !ok {
		klog.Error("transfer to v1.Node error")
	}
	klog.V(5).Infof("OnNodeAdd node %v add event", node.Name)
	p.Lock()
	defer p.Unlock()
	bucketID := p.getBucketID(node.Name)
	if pool, ok := p.nodePoolMap[bucketID]; ok {
		pool.Insert(node.Name)
	} else {
		pool = sets.NewString(node.Name)
		p.nodePoolMap[bucketID] = pool
	}
	metricData, exist := p.nodeStatDataMap[node.Name]
	if !exist {
		metricData = &NodeMetricData{}
		p.nodeStatDataMap[node.Name] = metricData
	}
	metricData.TotalRes = node.Status.Allocatable.DeepCopy()
}

func (p *Plugin) OnNodeUpdate(_, obj interface{}) {
	node, ok := obj.(*v1.Node)
	if !ok {
		klog.Error("transfer to v1.Node error")
	}
	klog.V(5).Infof("OnNodeUpdate node %v update event", node.Name)
	p.Lock()
	defer p.Unlock()
	metricData, exist := p.nodeStatDataMap[node.Name]
	if !exist {
		metricData = &NodeMetricData{}
		p.nodeStatDataMap[node.Name] = metricData
	}
	metricData.TotalRes = node.Status.Allocatable.DeepCopy()
}

func (p *Plugin) OnNodeDelete(obj interface{}) {
	node, ok := obj.(*v1.Node)
	if !ok {
		klog.Error("transfer to v1.Node error")
	}
	klog.V(5).Infof("OnNodeDelete node %v delete event", node.Name)
	p.Lock()
	bucketID := p.getBucketID(node.Name)
	if pool, ok := p.nodePoolMap[bucketID]; ok {
		pool.Delete(node.Name)
	}
	delete(p.nodeStatDataMap, node.Name)
	p.Unlock()
}

func (p *Plugin) OnPodAdd(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		klog.Error("transfer to v1.Pod error")
	}
	klog.V(5).Infof("OnPodAdd pod %v add event", pod.Name)
	p.Lock()
	defer p.Unlock()
	if p.podUsageSelectorKey != "" {
		if value, exist := pod.Labels[p.podUsageSelectorKey]; exist &&
			value == p.podUsageSelectorVal &&
			p.podUsageSelectorNamespace == pod.Namespace {
			klog.Info("start sync pod usage to nodeMonitor")
			p.enableSyncPodUsage = true
		}
	}

	if len(pod.Spec.NodeName) == 0 {
		return
	}
	podName := native.GenerateNamespaceNameKey(pod.Namespace, pod.Name)
	if existPods, ok := p.nodeToPodsMap[pod.Spec.NodeName]; ok {
		existPods[podName] = struct{}{}
	} else {
		existPods = make(map[string]struct{})
		existPods[podName] = struct{}{}
		p.nodeToPodsMap[pod.Spec.NodeName] = existPods
	}
}

func (p *Plugin) OnPodUpdate(_, newObj interface{}) {
	var pod *v1.Pod
	switch t := newObj.(type) {
	case *v1.Pod:
		pod = t
	case cache.DeletedFinalStateUnknown:
		var ok bool
		pod, ok = t.Obj.(*v1.Pod)
		if !ok {
			klog.Errorf("cannot convert to *v1.Pod: %v", t.Obj)
			return
		}
	default:
		klog.Errorf("cannot convert to *v1.Pod: %v", t)
		return
	}

	klog.V(5).Infof("OnPodUpdate node %v update event", pod.Name)
	if len(pod.Spec.NodeName) == 0 {
		return
	}
	p.Lock()
	defer p.Unlock()
	podName := native.GenerateNamespaceNameKey(pod.Namespace, pod.Name)
	if existPods, ok := p.nodeToPodsMap[pod.Spec.NodeName]; ok {
		existPods[podName] = struct{}{}
	} else {
		existPods = make(map[string]struct{})
		existPods[podName] = struct{}{}
		p.nodeToPodsMap[pod.Spec.NodeName] = existPods
	}
}

func (p *Plugin) OnPodDelete(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		klog.Error("transfer to v1.Pod error")
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			klog.Error("couldn't get object from tombstone %#v", obj)
			return
		}
		pod, ok = tombstone.Obj.(*v1.Pod)
		if !ok {
			klog.Error("tombstone contained object that is not a pod %#v", obj)
			return
		}
	}
	klog.V(5).Infof("OnPodDelete node %v delete event", pod.Name)
	p.Lock()
	defer p.Unlock()
	if p.podUsageSelectorVal != "" {
		if value, exist := pod.Labels[p.podUsageSelectorKey]; exist &&
			value == p.podUsageSelectorVal &&
			p.podUsageSelectorNamespace == pod.Namespace {
			klog.Info("stop sync pod usage to nodeMonitor")
			p.enableSyncPodUsage = false
		}
	}
	podName := native.GenerateNamespaceNameKey(pod.Namespace, pod.Name)
	delete(p.podStatDataMap, podName)
	if len(pod.Spec.NodeName) == 0 {
		return
	}
	if existPods, ok := p.nodeToPodsMap[pod.Spec.NodeName]; ok {
		delete(existPods, podName)
	}
}
