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

package indicator_plugin

import (
	"sync"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

const (
	indicatorStatusQueueLen = 1000
)

// IndicatorUpdater is used by IndicatorPlugin as a unified implementation
// to trigger indicator updating logic.
type IndicatorUpdater interface {
	UpdateNodeMetrics(name string, scopedNodeMetrics []v1alpha1.ScopedNodeMetrics)
	UpdatePodMetrics(nodeName string, scopedPodMetrics []v1alpha1.ScopedPodMetrics)
}

// IndicatorGetter is used by npd controller as indicator notifier to trigger
// update real npd.
type IndicatorGetter interface {
	GetNodeProfileStatusChan() chan string
	GetNodeProfileStatus(name string) *v1alpha1.NodeProfileDescriptorStatus
}

type IndicatorManager struct {
	sync.Mutex

	statusQueue chan string
	statusMap   map[string]*v1alpha1.NodeProfileDescriptorStatus
}

var (
	_ IndicatorUpdater = &IndicatorManager{}
	_ IndicatorGetter  = &IndicatorManager{}
)

func NewIndicatorManager() *IndicatorManager {
	return &IndicatorManager{
		statusMap:   make(map[string]*v1alpha1.NodeProfileDescriptorStatus),
		statusQueue: make(chan string, indicatorStatusQueueLen),
	}
}

func (im *IndicatorManager) UpdateNodeMetrics(name string, scopedNodeMetrics []v1alpha1.ScopedNodeMetrics) {
	im.Lock()

	insert := false
	if _, ok := im.statusMap[name]; !ok {
		insert = true
		im.statusMap[name] = initNodeProfileDescriptorStatus()
	}
	for _, scopedNodeMetric := range scopedNodeMetrics {
		util.InsertNPDScopedNodeMetrics(im.statusMap[name], &scopedNodeMetric)
	}

	im.Unlock()

	if insert {
		im.statusQueue <- name
	}
}

func (im *IndicatorManager) UpdatePodMetrics(nodeName string, scopedPodMetrics []v1alpha1.ScopedPodMetrics) {
	im.Lock()

	insert := false
	if _, ok := im.statusMap[nodeName]; !ok {
		insert = true
		im.statusMap[nodeName] = initNodeProfileDescriptorStatus()
	}
	for _, scopedPodMetric := range scopedPodMetrics {
		util.InsertNPDScopedPodMetrics(im.statusMap[nodeName], &scopedPodMetric)
	}

	im.Unlock()

	if insert {
		im.statusQueue <- nodeName
	}
}

func (im *IndicatorManager) GetNodeProfileStatusChan() chan string {
	return im.statusQueue
}

func (im *IndicatorManager) GetNodeProfileStatus(name string) *v1alpha1.NodeProfileDescriptorStatus {
	im.Lock()
	defer func() {
		delete(im.statusMap, name)
		im.Unlock()
	}()

	status, ok := im.statusMap[name]
	if !ok {
		klog.Warningf("npd status doesn't exist for node: %v", name)
		return nil
	}
	return status
}

func initNodeProfileDescriptorStatus() *v1alpha1.NodeProfileDescriptorStatus {
	return &v1alpha1.NodeProfileDescriptorStatus{
		NodeMetrics: []v1alpha1.ScopedNodeMetrics{},
		PodMetrics:  []v1alpha1.ScopedPodMetrics{},
	}
}
