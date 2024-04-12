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

package native

import (
	"fmt"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	v1 "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

type PodLabelIndexer string

const nodeNameKeyIndex = "spec.nodeName"

// IndexFunc is used to construct informer index for labels in pod
func (p PodLabelIndexer) IndexFunc(obj interface{}) ([]string, error) {
	pod, ok := obj.(*core.Pod)
	if !ok {
		return nil, fmt.Errorf("failed to reflect a obj to pod")
	}

	key := string(p)
	value := pod.Labels[key]

	return []string{value}, nil
}

// getPodListForWorkloadWithIndex is used to get pod list that belongs to the given workload
func getPodListForWorkloadWithIndex(selector labels.Selector, podIndexer cache.Indexer, podIndexerKey []string) ([]*core.Pod, error) {
	podMap := make(map[types.UID]*core.Pod)
	for _, key := range podIndexerKey {
		value, ok := selector.RequiresExactMatch(key)
		if !ok {
			klog.Warningf("key %v without value for selector %v", key, selector.String())
			continue
		}

		objs, err := podIndexer.ByIndex(key, value)
		if err != nil {
			klog.Warningf("pods for index %s/%s err: %v", key, value, err)
			continue
		}

		for _, obj := range objs {
			if pod, exist := obj.(*core.Pod); !exist {
				continue
			} else if _, exist = podMap[pod.UID]; !exist && selector.Matches(labels.Set(pod.Labels)) {
				podMap[pod.UID] = pod
			}
		}
	}

	if len(podMap) == 0 {
		return nil, fmt.Errorf("no pod matched with selector %v", selector.String())
	}

	var pods []*core.Pod
	for _, pod := range podMap {
		pods = append(pods, pod)
	}
	return pods, nil
}

// GetPodListForWorkload returns pod list that belong to the given workload
// we will use label selector to find pods, and this may require that the given
// workload is limited to several selected objects.
func GetPodListForWorkload(workloadObj runtime.Object, podIndexer cache.Indexer, labelKeyList []string, podLister corelisters.PodLister) ([]*core.Pod, error) {
	workload := workloadObj.(*unstructured.Unstructured)

	selector, err := GetUnstructuredSelector(workload)
	if err != nil || selector == nil {
		klog.Errorf("failed to get workload selector %v: %v", workloadObj, err)
		return nil, err
	}

	if podIndexer != nil {
		if podList, err := getPodListForWorkloadWithIndex(selector, podIndexer, labelKeyList); err == nil {
			klog.V(3).Infof("get pods with index successfully, selector: %v", selector)
			return podList, nil
		}
	}

	pods, err := podLister.List(selector)
	if err != nil {
		return nil, err
	}
	return pods, nil
}

// GetPodsAssignedToNode returns pods that belong to this node by indexer
func GetPodsAssignedToNode(nodeName string, podIndexer cache.Indexer) ([]*core.Pod, error) {
	objs, err := podIndexer.ByIndex(nodeNameKeyIndex, nodeName)
	if err != nil {
		return nil, err
	}

	pods := make([]*core.Pod, 0, len(objs))
	for _, obj := range objs {
		pod, ok := obj.(*core.Pod)
		if !ok {
			continue
		}
		pods = append(pods, pod)
	}
	return pods, nil
}

// AddNodeNameIndexerForPod add node name index for pod informer
func AddNodeNameIndexerForPod(podInformer v1.PodInformer) error {
	if _, ok := podInformer.Informer().GetIndexer().GetIndexers()[nodeNameKeyIndex]; !ok {
		return podInformer.Informer().GetIndexer().AddIndexers(cache.Indexers{
			nodeNameKeyIndex: func(obj interface{}) ([]string, error) {
				pod, ok := obj.(*core.Pod)
				if !ok {
					return []string{}, nil
				}
				if len(pod.Spec.NodeName) == 0 {
					return []string{}, nil
				}
				return []string{pod.Spec.NodeName}, nil
			},
		})
	}
	return nil
}
