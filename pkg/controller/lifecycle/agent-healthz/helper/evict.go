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

package helper

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/nodelifecycle/scheduler"

	listers "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const metricsNameEvictedReclaimedPodCount = "evicted_reclaimed_pod_count"

// EvictItem records the detailed item to perform pod eviction
type EvictItem struct {
	// PodKeys maps from agent-name to pod-keys (that should be evicted because of the Agents)
	PodKeys map[string][]string
}

type EvictHelper struct {
	ctx        context.Context
	emitter    metrics.MetricEmitter
	podControl control.PodEjector

	checker *HealthzHelper
	queue   *scheduler.RateLimitedTimedQueue

	podLister  corelisters.PodLister
	nodeLister corelisters.NodeLister
	cnrLister  listers.CustomNodeResourceLister
}

// NewEvictHelper todo add logic here
func NewEvictHelper(ctx context.Context, emitter metrics.MetricEmitter,
	podControl control.PodEjector, nodeLister corelisters.NodeLister, cnrLister listers.CustomNodeResourceLister,
	queue *scheduler.RateLimitedTimedQueue, checker *HealthzHelper,
) *EvictHelper {
	return &EvictHelper{
		ctx:        ctx,
		emitter:    emitter,
		podControl: podControl,

		queue:   queue,
		checker: checker,

		nodeLister: nodeLister,
		cnrLister:  cnrLister,
	}
}

func (e *EvictHelper) Run() {
	go wait.Until(e.doEviction, scheduler.NodeEvictionPeriod, e.ctx.Done())
}

// doEviction is used to pop nodes from to-be-evicted queue,
// and then trigger the taint actions
func (e *EvictHelper) doEviction() {
	e.queue.Try(func(value scheduler.TimedValue) (bool, time.Duration) {
		node, err := e.nodeLister.Get(value.Value)
		if errors.IsNotFound(err) {
			klog.Warningf("Node %v no longer present in nodeLister", value.Value)
			return true, 0
		} else if err != nil {
			klog.Warningf("Failed to get Node %v from the nodeLister: %v", value.Value, err)
			// retry in 50 millisecond
			return false, 50 * time.Millisecond
		}

		// second confirm that we should evict reclaimed pods
		keys := sets.NewString()
		item := value.UID.(*EvictItem)
		for agent, names := range item.PodKeys {
			if !e.checker.CheckAgentReady(node.Name, agent) {
				keys.Insert(names...)
			}
		}

		if err := e.evictPods(node, keys.List()); err != nil {
			klog.Warningf("failed to evict pods for cnr %v: %v", value.Value, err)
			return true, 5 * time.Second
		}
		return true, 0
	})
}

// evictPods must filter out those pods that should be managed
// todo evict pods in with concurrency if necessary
func (e *EvictHelper) evictPods(node *corev1.Node, keys []string) error {
	var errList []error
	for _, key := range keys {
		namespace, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			klog.Errorf("failed to split namespace and name from key %s", key)
			continue
		}

		delErr := e.podControl.DeletePod(e.ctx, namespace, name, metav1.DeleteOptions{})
		if delErr != nil {
			_ = e.emitter.StoreInt64(metricsNameEvictedReclaimedPodCount, 1, metrics.MetricTypeNameCount,
				[]metrics.MetricTag{
					{Key: "status", Val: "failed"},
					{Key: "name", Val: node.Name},
				}...)
			errList = append(errList, delErr)
			continue
		}

		_ = e.emitter.StoreInt64(metricsNameEvictedReclaimedPodCount, 1, metrics.MetricTypeNameCount,
			[]metrics.MetricTag{
				{Key: "status", Val: "success"},
				{Key: "name", Val: node.Name},
			}...)
	}
	if len(errList) > 0 {
		return utilerrors.NewAggregate(errList)
	}

	return nil
}
