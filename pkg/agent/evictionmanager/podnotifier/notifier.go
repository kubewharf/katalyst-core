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
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	MetricsNameNotifyPod = "notify_pod"

	HostPathNotifyFileName      = "soft_eviction_notify"
	HostPathNotifyFileSeparator = "@@__@@"
)

// Notifier implements pod notify logic.三条腿6
type Notifier interface {
	// Name returns name as identifier for a specific Notifier.
	Name() string

	// Run
	Run(ctx context.Context)

	// Notify a pod that eviction maybe happened.
	Notify(ctx context.Context, pod *v1.Pod, reason, plugin string) error
}

// HostPathPodNotifier implements Notifier interface by hostpath.
type HostPathPodNotifier struct {
	rootPath   string
	client     kubernetes.Interface
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
	recorder   events.EventRecorder
}

// NewHostPathPodNotifier returns a new updater Object.
func NewHostPathPodNotifier(cfg *config.Configuration, client kubernetes.Interface, metaServer *metaserver.MetaServer, recorder events.EventRecorder, emitter metrics.MetricEmitter) (Notifier, error) {
	if cfg.HostPathNotifierRootPath == "" {
		return nil, fmt.Errorf("HostPathNotifierRootPath must be set")
	}

	return &HostPathPodNotifier{
		client:     client,
		metaServer: metaServer,
		emitter:    emitter,
		recorder:   recorder,
		rootPath:   cfg.HostPathNotifierRootPath,
	}, nil
}

func (n *HostPathPodNotifier) Name() string { return consts.NotifierNameHostPath }

func (n *HostPathPodNotifier) Run(ctx context.Context) {
	go wait.Until(n.cleanHostPath, time.Minute, ctx.Done())
	go wait.Until(n.createHostPath, time.Minute, ctx.Done())
}

func (n *HostPathPodNotifier) genPodNotifyPath(pod *v1.Pod) string {
	if pod == nil {
		return ""
	}

	return filepath.Join(n.rootPath, pod.Name+HostPathNotifyFileSeparator+pod.Namespace)
}

func (n *HostPathPodNotifier) createHostPath() {
	podFilter := func(pod *v1.Pod) bool {
		if pod == nil {
			return false
		}

		if !native.PodIsActive(pod) {
			return false
		}

		_, ok := pod.Annotations[apiconsts.PodAnnotationSoftEvictNotificationKey]
		return ok
	}

	notifyPods, err := n.metaServer.GetPodList(context.TODO(), podFilter)
	if err != nil {
		klog.Errorf("get notify pods failed: %s", err)
		return
	}

	for _, pod := range notifyPods {
		podPath := n.genPodNotifyPath(pod)
		_, err := os.Stat(podPath)
		if err == nil {
			continue
		} else if os.IsNotExist(err) {
			err := os.MkdirAll(podPath, os.ModePerm)
			if err != nil {
				klog.Errorf("create pod path failed: %s, %s", podPath, err)
				continue
			}
		}
	}
}

func (n *HostPathPodNotifier) cleanHostPath() {
	dirs, err := os.ReadDir(n.rootPath)
	if err != nil {
		klog.Errorf("failed to read dir %s: %v", n.rootPath, err)
		return
	}

	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}

		dirName := dir.Name()
		if !strings.Contains(dirName, HostPathNotifyFileSeparator) {
			continue
		}

		items := strings.Split(dirName, HostPathNotifyFileSeparator)
		if len(items) != 2 {
			continue
		}

		podName := items[0]
		namespace := items[1]

		_, err := n.client.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			err = os.RemoveAll(filepath.Join(n.rootPath, dirName))
			klog.Infof("remove non-exists pod dir %s: %v", filepath.Join(n.rootPath, dirName), err)
		}
	}
}

func (n *HostPathPodNotifier) Notify(_ context.Context, pod *v1.Pod, reason, plugin string) error {
	notifyPod := func(pod *v1.Pod) error {
		klog.Infof("[host-path-notifier] send notify to pod %v/%v", pod.Namespace, pod.Name)

		notifyPath := filepath.Join(n.genPodNotifyPath(pod), HostPathNotifyFileName)
		f, err := os.OpenFile(notifyPath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("[host-path-notifier] failed to open notify file: %s, %v", notifyPath, err)
		}

		_, err = f.WriteString(time.Now().String())
		if err != nil {
			return fmt.Errorf("[host-path-notifier] failed to write notify file: %s, %v", notifyPath, err)
		}

		return nil
	}

	return notify(n.recorder, n.emitter, pod, reason, plugin, notifyPod)
}

func notify(recorder events.EventRecorder, emitter metrics.MetricEmitter, pod *v1.Pod,
	reason, plugin string, notifyPod func(_ *v1.Pod) error,
) error {
	klog.Infof("[notifier] notify pod %v/%v", pod.Namespace, pod.Name)

	if err := notifyPod(pod); err != nil {
		recorder.Eventf(pod, nil, v1.EventTypeNormal, consts.EventReasonNotifyFailed, consts.EventActionNotifying,
			fmt.Sprintf("notify failed: %s", err))
		_ = emitter.StoreInt64(MetricsNameNotifyPod, 1, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "state", Val: "failed"},
			metrics.MetricTag{Key: "pod_ns", Val: pod.Namespace},
			metrics.MetricTag{Key: "pod_name", Val: pod.Name},
			metrics.MetricTag{Key: "plugin_name", Val: plugin})

		return fmt.Errorf("[notifier] notify pod failed: %s, %v", pod.Name, err)
	}

	recorder.Eventf(pod, nil, v1.EventTypeNormal, consts.EventReasonNotifySuccess, consts.EventActionNotifying,
		"notify pod successfully; reason: %s", reason)
	_ = emitter.StoreInt64(MetricsNameNotifyPod, 1, metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "state", Val: "succeeded"},
		metrics.MetricTag{Key: "pod_ns", Val: pod.Namespace},
		metrics.MetricTag{Key: "pod_name", Val: pod.Name},
		metrics.MetricTag{Key: "plugin_name", Val: plugin})
	klog.Infof("[notifier] successfully notify pod %v/%v", pod.Namespace, pod.Name)

	return nil
}
