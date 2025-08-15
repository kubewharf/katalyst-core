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

package podnotifier

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/rule"
)

// PodNotifier implements the notify soft eviction actions for given pods.
type PodNotifier interface {
	// Name returns name as identifier for a specific notifier.
	Name() string

	// Start pod notifier logic.
	Start(ctx context.Context)

	// NotifyPods notify a list pods.
	NotifyPods(rpList rule.RuledEvictPodList) error

	// NotifyPod notify a pod.
	NotifyPod(rp *rule.RuledEvictPod) error
}

// SynchronizedPodNotifier trigger notify actions immediately after
// receiving notify requests; only returns true if all pods are
// successfully notified.
type SynchronizedPodNotifier struct {
	notifier Notifier
}

func NewSynchronizedPodNotifier(notifier Notifier) PodNotifier {
	return &SynchronizedPodNotifier{
		notifier: notifier,
	}
}

func (s *SynchronizedPodNotifier) Name() string { return "synchronized-pod-notifier" }

func (s *SynchronizedPodNotifier) Start(ctx context.Context) {
	klog.Infof("[synchronized] pod-notifier run with notifier %v", s.notifier.Name())
	s.notifier.Run(ctx)
	defer klog.Infof("[synchronized] pod-notifier started")
}

func (s *SynchronizedPodNotifier) NotifyPod(rp *rule.RuledEvictPod) error {
	if rp == nil || rp.Pod == nil {
		return fmt.Errorf("NotifyPod got nil pod")
	}

	err := s.notifier.Notify(context.Background(), rp.Pod, rp.Reason, rp.EvictionPluginName)
	if err != nil {
		return fmt.Errorf("notify pod: %s/%s failed with error: %v", rp.Pod.Namespace, rp.Pod.Name, err)
	}

	return nil
}

func (s *SynchronizedPodNotifier) NotifyPods(rpList rule.RuledEvictPodList) error {
	var errList []error
	var mtx sync.Mutex

	klog.Infof("[synchronized] pod-notifier evict %d totally", len(rpList))
	syncNodeUtilizationAndAdjust := func(i int) {
		err := s.NotifyPod(rpList[i])

		mtx.Lock()
		if err != nil {
			errList = append(errList, err)
		}
		mtx.Unlock()
	}
	workqueue.ParallelizeUntil(context.Background(), 3, len(rpList), syncNodeUtilizationAndAdjust)

	klog.Infof("[synchronized] successfully evict %d totally", len(rpList)-len(errList))
	return errors.NewAggregate(errList)
}
