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

package podkiller

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/rule"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

// PodKiller implements the killing actions for given pods.
type PodKiller interface {
	// Name returns name as identifier for a specific Killer.
	Name() string

	// Start pod killer logic, prepare to receive on-killing pods.
	Start(ctx context.Context)

	// EvictPods send on-killing pods to pod killer.
	EvictPods(rpList rule.RuledEvictPodList) error

	// EvictPod a pod with the specified grace period.
	EvictPod(rp *rule.RuledEvictPod) error
}

// DummyPodKiller is a stub implementation for Killer interface.
type DummyPodKiller struct{}

func (d DummyPodKiller) Name() string                           { return "dummy-pod-killer" }
func (d DummyPodKiller) Start(_ context.Context)                {}
func (d DummyPodKiller) EvictPods(rule.RuledEvictPodList) error { return nil }
func (d DummyPodKiller) EvictPod(*rule.RuledEvictPod) error     { return nil }

var _ PodKiller = DummyPodKiller{}

// SynchronizedPodKiller trigger killing actions immediately after
// receiving killing requests; only returns true if all pods are
// successfully evicted.
type SynchronizedPodKiller struct {
	killer Killer
}

func NewSynchronizedPodKiller(killer Killer) PodKiller {
	return &SynchronizedPodKiller{
		killer: killer,
	}
}

func (s *SynchronizedPodKiller) Name() string { return "synchronized-pod-killer" }

func (s *SynchronizedPodKiller) Start(_ context.Context) {
	klog.Infof("[synchronized] pod-killer run with killer %v", s.killer.Name())
	defer klog.Infof("[synchronized] pod-killer started")
}

func (s *SynchronizedPodKiller) EvictPod(rp *rule.RuledEvictPod) error {
	if rp == nil || rp.Pod == nil {
		return fmt.Errorf("EvictPod got nil pod")
	}

	gracePeriod, err := getGracefulDeletionPeriod(rp.Pod, rp.DeletionOptions)
	if err != nil {
		return fmt.Errorf("getGracefulDeletionPeriod for pod: %s/%s failed with error: %v", rp.Pod.Namespace, rp.Pod.Name, err)
	}

	err = s.killer.Evict(context.Background(), rp.Pod, gracePeriod, rp.Reason, rp.EvictionPluginName)
	if err != nil {
		return fmt.Errorf("evict pod: %s/%s failed with error: %v", rp.Pod.Namespace, rp.Pod.Name, err)
	}

	return nil
}

func (s *SynchronizedPodKiller) EvictPods(rpList rule.RuledEvictPodList) error {
	var errList []error
	var mtx sync.Mutex

	klog.Infof("[synchronized] pod-killer evict %d totally", len(rpList))
	syncNodeUtilizationAndAdjust := func(i int) {
		err := s.EvictPod(rpList[i])

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

// AsynchronizedPodKiller pushed killing actions into a queue and
// returns true directly, another go routine will be responsible
// to perform killing actions instead.
type AsynchronizedPodKiller struct {
	killer Killer

	client kubernetes.Interface

	// use map to act as a limited queue
	queue workqueue.RateLimitingInterface

	// processingPods is used to store pods that are being evicted
	// the map is constructed as podName -> gracefulPeriod -> evictPodInfo
	processingPods map[string]map[int64]*evictPodInfo

	sync.RWMutex
}

type evictPodInfo struct {
	Pod    *v1.Pod
	Reason string
	Plugin string
}

func getEvictPodInfo(rp *rule.RuledEvictPod) *evictPodInfo {
	return &evictPodInfo{
		Pod:    rp.Pod.DeepCopy(),
		Reason: rp.Reason,
		Plugin: rp.EvictionPluginName,
	}
}

func NewAsynchronizedPodKiller(killer Killer, client kubernetes.Interface) PodKiller {
	a := &AsynchronizedPodKiller{
		killer:         killer,
		client:         client,
		processingPods: make(map[string]map[int64]*evictPodInfo),
	}
	a.queue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), a.Name())
	return a
}

func (a *AsynchronizedPodKiller) Name() string { return "asynchronous-pod-killer" }

func (a *AsynchronizedPodKiller) Start(ctx context.Context) {
	klog.Infof("[asynchronous] pod-killer run with killer %v", a.killer.Name())
	defer klog.Infof("[asynchronous] pod-killer started")

	for i := 0; i < 10; i++ {
		go wait.Until(a.run, time.Second, ctx.Done())
	}
}

func (a *AsynchronizedPodKiller) EvictPods(rpList rule.RuledEvictPodList) error {
	klog.Infof("[asynchronous] pod-killer evict %d totally", len(rpList))

	errList := make([]error, 0, len(rpList))
	for _, rp := range rpList {
		err := a.EvictPod(rp)
		if err != nil {
			errList = append(errList, err)
		}
	}

	klog.Infof("[asynchronous] successfully add %d pods to eviction queue", len(rpList)-len(errList))
	return errors.NewAggregate(errList)
}

func (a *AsynchronizedPodKiller) EvictPod(rp *rule.RuledEvictPod) error {
	if rp == nil || rp.Pod == nil {
		return fmt.Errorf("evictPod got nil pod")
	}

	gracePeriod, err := getGracefulDeletionPeriod(rp.Pod, rp.DeletionOptions)
	if err != nil {
		return fmt.Errorf("getGracefulDeletionPeriod for pod: %s/%s failed with error: %v", rp.Pod.Namespace, rp.Pod.Name, err)
	}
	podKey := podKeyFunc(rp.Pod.Namespace, rp.Pod.Name)

	a.Lock()
	if a.processingPods[podKey] != nil {
		var minOne int64 = math.MaxInt64
		for recordedGracePeriod := range a.processingPods[podKey] {
			if recordedGracePeriod < minOne {
				minOne = recordedGracePeriod
			}
		}

		if gracePeriod >= minOne {
			a.Unlock()
			klog.Infof("[asynchronous] pod: %s/%s is being processed with smaller grace period, skip it", rp.Pod.Namespace, rp.Pod.Name)
			return nil
		}
	}

	if a.processingPods[podKey] == nil {
		a.processingPods[podKey] = make(map[int64]*evictPodInfo)
	}

	a.processingPods[podKey][gracePeriod] = getEvictPodInfo(rp)
	a.Unlock()

	a.queue.AddRateLimited(evictionKeyFunc(podKey, gracePeriod))
	return nil
}

// run is a long-running function that will continually call the
// processNextItem function in order to read and process a message on the queue.
func (a *AsynchronizedPodKiller) run() {
	for a.processNextItem() {
	}
}

// processNextItem will read a single work item off the queue and
// attempt to process it, by calling the sync function.
func (a *AsynchronizedPodKiller) processNextItem() bool {
	obj, shutdown := a.queue.Get()
	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer a.queue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			a.queue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// ExecDeploy resource to be synced.
		if err, requeue := a.sync(key); err != nil {
			// Put the item back on the workqueue to handle any transient errors.
			klog.Warningf("[asynchronous] error syncing '%s': %s, requeuing", key, err.Error())

			if requeue {
				a.queue.AddRateLimited(key)
			} else {
				a.queue.Forget(obj)
			}

			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		a.queue.Forget(obj)
		return nil
	}(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

func (a *AsynchronizedPodKiller) sync(key string) (retError error, requeue bool) {
	namespace, name, gracePeriodSeconds, err := splitEvictionKey(key)
	if err != nil {
		return fmt.Errorf("[asynchronous] invalid resource key: %s got error: %v", key, err), false
	}

	podKey := podKeyFunc(namespace, name)
	defer func() {
		if !requeue {
			a.Lock()
			delete(a.processingPods[podKey], gracePeriodSeconds)

			if len(a.processingPods[podKey]) == 0 {
				delete(a.processingPods, podKey)
			}
			a.Unlock()
		}
	}()

	// todo: actually, this function is safe enough without comparing with pod uid
	//  if the same pod is created just after the last one exists
	//  handle with more filters in the future
	pod, err := a.client.CoreV1().Pods(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("[asynchronous] %s/%s has already been deleted, skip", namespace, name)
			return nil, false
		}
		return err, true
	}

	var reason, plugin string
	a.RLock()
	if a.processingPods[podKey][gracePeriodSeconds] == nil {
		a.RUnlock()
		return fmt.Errorf("[asynchronous] evict pod can't be found by podKey: %s and gracePeriodSeconds: %d", podKey, gracePeriodSeconds), false
	}
	reason = a.processingPods[podKey][gracePeriodSeconds].Reason
	plugin = a.processingPods[podKey][gracePeriodSeconds].Plugin
	a.RUnlock()

	err = a.killer.Evict(context.Background(), pod, gracePeriodSeconds, reason, plugin)
	if err != nil {
		return err, true
	} else {
		return nil, false
	}
}

func podKeyFunc(podNamespace, podName string) string {
	return strings.Join([]string{podNamespace, podName}, consts.KeySeparator)
}

func evictionKeyFunc(podKey string, gracePeriodSeconds int64) string {
	return strings.Join([]string{podKey, fmt.Sprintf("%d", gracePeriodSeconds)}, consts.KeySeparator)
}

func splitEvictionKey(key string) (string, string, int64, error) {
	parts := strings.Split(key, consts.KeySeparator)

	if len(parts) != 3 {
		return "", "", 0, fmt.Errorf("unexpected key format: %s", key)
	}

	gracePeriodSeconds, err := strconv.ParseInt(parts[2], 10, 64)

	if err != nil {
		return "", "", 0, fmt.Errorf("unexpected gracePeriodSeconds: %s", parts[2])
	}

	return parts[0], parts[1], gracePeriodSeconds, nil
}

func getGracefulDeletionPeriod(pod *v1.Pod, options *pluginapi.DeletionOptions) (int64, error) {
	if pod == nil {
		return 0, fmt.Errorf("getGracefulDeletionPeriod got nil pod")
	}

	// determine the grace period to use when killing the pod
	gracePeriod := int64(0)
	if options != nil {
		if options.GracePeriodSeconds < 0 {
			return 0, fmt.Errorf("deletion options with negative grace period seconds")
		}
		gracePeriod = options.GracePeriodSeconds
	} else if pod.Spec.TerminationGracePeriodSeconds != nil {
		gracePeriod = *pod.Spec.TerminationGracePeriodSeconds
	}

	return gracePeriod, nil
}
