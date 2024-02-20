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

// todo: move APIServer update/patch/create actions to client package

package podkiller

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	policy "k8s.io/api/policy/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/events"
	cri "k8s.io/cri-api/pkg/apis"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/container"
	"k8s.io/kubernetes/pkg/kubelet/cri/remote"

	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	MetricsNameKillPod       = "kill_pod"
	MetricsNameKillContainer = "kill_container"
)

type InitFunc func(conf *config.Configuration, client kubernetes.Interface, recorder events.EventRecorder, emitter metrics.MetricEmitter) (Killer, error)

// Killer implements pod eviction logic.
type Killer interface {
	// Name returns name as identifier for a specific Killer.
	Name() string

	// Evict for given pods and corresponding graceful period seconds.
	Evict(ctx context.Context, pod *v1.Pod, gracePeriodSeconds int64, reason, plugin string) error
}

// DummyKiller is a stub implementation for Killer interface.
type DummyKiller struct{}

func (d DummyKiller) Name() string                                                   { return consts.KillerNameFakeKiller }
func (d DummyKiller) Evict(_ context.Context, _ *v1.Pod, _ int64, _, _ string) error { return nil }

var _ Killer = DummyKiller{}

// EvictionAPIKiller implements Killer interface it evict those given pods by
// eviction API, and wait until pods have actually been deleted.
type EvictionAPIKiller struct {
	emitter  metrics.MetricEmitter
	client   kubernetes.Interface
	recorder events.EventRecorder
}

// NewEvictionAPIKiller returns a new updater Object.
func NewEvictionAPIKiller(_ *config.Configuration, client kubernetes.Interface, recorder events.EventRecorder, emitter metrics.MetricEmitter) (Killer, error) {
	return &EvictionAPIKiller{
		emitter:  emitter,
		client:   client,
		recorder: recorder,
	}, nil
}

func (e *EvictionAPIKiller) Name() string { return consts.KillerNameEvictionKiller }

func (e *EvictionAPIKiller) Evict(_ context.Context, pod *v1.Pod, gracePeriodSeconds int64, reason, plugin string) error {
	const (
		policyGroupVersion = "policy/v1beta1"
		evictionKind       = "Eviction"
	)

	evictPod := func(pod *v1.Pod, gracePeriodOverride int64) error {
		klog.Infof("[eviction-killer] send request for pod %v/%v", pod.Namespace, pod.Name)

		deleteOptions := &metav1.DeleteOptions{GracePeriodSeconds: &gracePeriodOverride}
		eviction := &policy.Eviction{
			TypeMeta: metav1.TypeMeta{
				APIVersion: policyGroupVersion,
				Kind:       evictionKind,
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      pod.Name,
				Namespace: pod.Namespace,
			},
			DeleteOptions: deleteOptions,
		}
		return e.client.PolicyV1beta1().Evictions(eviction.Namespace).Evict(context.Background(), eviction)
	}

	return evict(e.client, e.recorder, e.emitter, pod, gracePeriodSeconds, reason, plugin, evictPod)
}

// DeletionAPIKiller implements Killer interface it evict those
// given pods by calling pod deletion API.
type DeletionAPIKiller struct {
	emitter  metrics.MetricEmitter
	client   kubernetes.Interface
	recorder events.EventRecorder
}

func NewDeletionAPIKiller(_ *config.Configuration, client kubernetes.Interface, recorder events.EventRecorder, emitter metrics.MetricEmitter) (Killer, error) {
	return &DeletionAPIKiller{
		emitter:  emitter,
		client:   client,
		recorder: recorder,
	}, nil
}

func (d *DeletionAPIKiller) Name() string { return consts.KillerNameDeletionKiller }

func (d *DeletionAPIKiller) Evict(ctx context.Context, pod *v1.Pod, gracePeriodSeconds int64, reason, plugin string) error {
	evictPod := func(pod *v1.Pod, gracePeriodOverride int64) error {
		klog.Infof("[deletion-killer] send request for pod %v/%v", pod.Namespace, pod.Name)

		deleteOptions := metav1.DeleteOptions{GracePeriodSeconds: &gracePeriodOverride}
		return d.client.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, deleteOptions)
	}

	return evict(d.client, d.recorder, d.emitter, pod, gracePeriodSeconds, reason, plugin, evictPod)
}

// getWaitingPeriod get waiting period from graceful period.
func getWaitingPeriod(gracePeriod int64) time.Duration {
	// the default timeout is relative to the grace period;
	// settle on 10s to wait for kubelet->runtime traffic to complete in sigkill
	timeout := gracePeriod + gracePeriod/2
	minTimeout := int64(10)
	if timeout < minTimeout {
		timeout = minTimeout
	}
	return time.Duration(timeout) * time.Second
}

// waitForDeleted wait util pods have been physically deleted from APIServer.
func waitForDeleted(client kubernetes.Interface, pods []*v1.Pod, timeout time.Duration) ([]*v1.Pod, error) {
	const interval = time.Second * 5
	err := wait.PollImmediate(interval, timeout, func() (bool, error) {
		var pendingPods []*v1.Pod
		for i, pod := range pods {
			// todo: refer through ETCD to make sure pods are physically deleted (is it reasonable?)
			p, err := client.CoreV1().Pods(pod.Namespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) || (p != nil && p.ObjectMeta.UID != pod.ObjectMeta.UID) {
				continue
			} else if err != nil {
				return false, err
			} else {
				pendingPods = append(pendingPods, pods[i])
			}
		}
		pods = pendingPods
		if len(pendingPods) > 0 {
			return false, nil
		}
		return true, nil
	})
	return pods, err
}

// deleteWithRetry keeping calling deletion func until it checks pods
// have been deleted timeout and return an error if it doesn't get a
// callback within a reasonable time.
func deleteWithRetry(pod *v1.Pod, gracePeriod int64, timeoutDuration time.Duration,
	evictPod func(_ *v1.Pod, gracePeriod int64) error,
) error {
	timeoutTick := time.NewTimer(timeoutDuration)
	for {
		success := false
		select {
		case <-timeoutTick.C:
			return errors.Errorf("eviction request did not complete within %v", timeoutDuration)
		default:
			err := evictPod(pod, gracePeriod)
			if err == nil {
				success = true
				break
			} else if apierrors.IsNotFound(err) {
				success = true
				break
			} else if apierrors.IsTooManyRequests(err) {
				delay, retry := apierrors.SuggestsClientDelay(err)
				if !retry {
					delay = 5
				}
				time.Sleep(time.Duration(delay) * time.Second)
			} else {
				return errors.Errorf("error when evicting pod %q: %v", pod.Name, err)
			}
		}

		if success {
			break
		}
	}

	return nil
}

// evict all killer implementations will perform evict actions.
func evict(client kubernetes.Interface, recorder events.EventRecorder, emitter metrics.MetricEmitter, pod *v1.Pod,
	gracePeriodSeconds int64, reason, plugin string, evictPod func(_ *v1.Pod, gracePeriod int64) error) error {
	timeoutDuration := getWaitingPeriod(gracePeriodSeconds)
	klog.Infof("[killer] evict pod %v/%v with graceful seconds %v", pod.Namespace, pod.Name, gracePeriodSeconds)

	if err := deleteWithRetry(pod, gracePeriodSeconds, timeoutDuration, evictPod); err != nil {
		recorder.Eventf(pod, nil, v1.EventTypeWarning, consts.EventReasonEvictFailed, consts.EventActionEvicting,
			fmt.Sprintf("Evict failed: %s", err))
		_ = emitter.StoreInt64(MetricsNameKillPod, 1, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "state", Val: "failed"},
			metrics.MetricTag{Key: "pod_ns", Val: pod.Namespace},
			metrics.MetricTag{Key: "pod_name", Val: pod.Name},
			metrics.MetricTag{Key: "plugin_name", Val: plugin})

		return fmt.Errorf("evict failed %v", err)
	}

	recorder.Eventf(pod, nil, v1.EventTypeNormal, consts.EventReasonEvictCreated, consts.EventActionEvicting,
		"Successfully create eviction; reason: %s", reason)
	_ = emitter.StoreInt64(MetricsNameKillPod, 1, metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "state", Val: "succeeded"},
		metrics.MetricTag{Key: "pod_ns", Val: pod.Namespace},
		metrics.MetricTag{Key: "pod_name", Val: pod.Name},
		metrics.MetricTag{Key: "plugin_name", Val: plugin})
	klog.Infof("[killer] successfully create eviction for pod %v/%v", pod.Namespace, pod.Name)

	podArray := []*v1.Pod{pod}
	_, err := waitForDeleted(client, podArray, timeoutDuration)
	if err != nil {
		recorder.Eventf(pod, nil, v1.EventTypeWarning, consts.EventReasonEvictExceededGracePeriod, consts.EventActionEvicting,
			"Container runtime did not kill the pod within specified grace period")

		return fmt.Errorf("container deletion did not complete within %v", timeoutDuration)
	}

	recorder.Eventf(pod, nil, v1.EventTypeNormal, consts.EventReasonEvictSucceeded, consts.EventActionEvicting,
		"Evicted pod has been deleted physically; reason: %s", reason)
	klog.Infof("[killer] pod %s/%s has been deleted physically", pod.Namespace, pod.Name)

	return nil
}

// ContainerKiller implements Killer interface it actually does not evict pod but
// stop containers in given pod directly.
type ContainerKiller struct {
	containerManager cri.ContainerManager
	recorder         events.EventRecorder
	emitter          metrics.MetricEmitter
}

func NewContainerKiller(conf *config.Configuration, _ kubernetes.Interface, recorder events.EventRecorder, emitter metrics.MetricEmitter) (Killer, error) {
	remoteRuntimeService, err := remote.NewRemoteRuntimeService(conf.RuntimeEndpoint, 2*time.Minute)
	if err != nil {
		return nil, err
	}

	return &ContainerKiller{
		containerManager: remoteRuntimeService,
		recorder:         recorder,
		emitter:          emitter,
	}, nil
}

func (c *ContainerKiller) Name() string { return consts.KillerNameContainerKiller }

func (c *ContainerKiller) Evict(_ context.Context, pod *v1.Pod, gracePeriodSeconds int64, reason, plugin string) error {
	if pod == nil {
		return fmt.Errorf("pod is nil")
	}

	for _, containerStatus := range pod.Status.ContainerStatuses {
		containerID := container.ParseContainerID(containerStatus.ContainerID)
		err := c.containerManager.StopContainer(containerID.ID, gracePeriodSeconds)
		if err != nil {
			c.recorder.Eventf(pod, nil, v1.EventTypeNormal, consts.EventReasonContainerStopped, consts.EventActionContainerStopping,
				"Failed to kill container %v; reason: %s", containerStatus.Name, reason)
			_ = c.emitter.StoreInt64(MetricsNameKillContainer, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "state", Val: "failed"},
				metrics.MetricTag{Key: "pod_ns", Val: pod.Namespace},
				metrics.MetricTag{Key: "pod_name", Val: pod.Name},
				metrics.MetricTag{Key: "container_name", Val: containerStatus.Name},
				metrics.MetricTag{Key: "plugin_name", Val: plugin})
			klog.Infof("[killer] failed to kill container %v(containerID: %v) for pod %v/%v, error:%v", containerStatus.Name, containerID, pod.Namespace, pod.Name, err)
			return fmt.Errorf("ContainerKiller stop container %v failed with error: %v", containerStatus.ContainerID, err)
		}
		c.recorder.Eventf(pod, nil, v1.EventTypeNormal, consts.EventReasonContainerStopped, consts.EventActionContainerStopping,
			"Successfully kill container %v; reason: %s", containerStatus.Name, reason)
		_ = c.emitter.StoreInt64(MetricsNameKillContainer, 1, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "state", Val: "succeeded"},
			metrics.MetricTag{Key: "pod_ns", Val: pod.Namespace},
			metrics.MetricTag{Key: "pod_name", Val: pod.Name},
			metrics.MetricTag{Key: "container_name", Val: containerStatus.Name},
			metrics.MetricTag{Key: "plugin_name", Val: plugin})
		klog.Infof("[killer] successfully kill container %v/%v for pod %v/%v", containerStatus.Name, containerStatus.ContainerID, pod.Namespace, pod.Name)
	}
	// TODO: do we have to wait for container being completely killed?

	return nil
}
