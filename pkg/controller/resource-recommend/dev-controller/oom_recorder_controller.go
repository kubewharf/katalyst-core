/*
Copyright 2024 The Katalyst Authors.

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

package controller

import (
	"context"
	"fmt"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/oom"
	"github.com/pkg/errors"
	core "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const OOMRecorderControllerName = "oomRecorder"

// PodOOMRecorderController controls pod oom events recorder
type PodOOMRecorderController struct {
	ctx        context.Context
	syncedFunc []cache.InformerSynced
	Recorder   *PodOOMRecorder
}

// NewPodOOMRecorderController
// todo: genericConf 参考了其他的几个 Controller 都是要传入的，但是我们只有 OOMRecorder，似乎是不需要用到的
func NewPodOOMRecorderController(ctx context.Context,
	controlCtx *katalystbase.GenericContext,
	genericConf *generic.GenericConfiguration,
	_ *controller.GenericControllerConfiguration,
	recConf *controller.ResourceRecommenderConfig,
) (*PodOOMRecorderController, error) {
	if controlCtx == nil {
		return nil, fmt.Errorf("controlCtx is invalid")
	}

	podInformer := controlCtx.KubeInformerFactory.Core().V1().Pods()

	podOOMRecorderController := &PodOOMRecorderController{
		ctx: ctx,
		syncedFunc: []cache.InformerSynced{
			podInformer.Informer().HasSynced,
		},
	}

	// 先启动协程再添加监听，client 从使用方式来看应该是 KubeClient，但是提示类型不适配?
	// 感觉过去要改 recorder，recorder 用的也是对 ConfigMap 做修改
	// 其他都能替代，但是没有找到 IgnoreNotFound 的替代，看了一下也是封装，应该可以用封装里面那个函数判断
	// 240728: 使用修改后的 oom_recorder
	podOOMRecorderController.Recorder = &PodOOMRecorder{
		Client:             controlCtx.Client.KubeClient,
		OOMRecordMaxNumber: recConf.OOMRecordMaxNumber,
		Queue:              workqueue.New(),
	}

	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    podOOMRecorderController.addPod,
		UpdateFunc: podOOMRecorderController.updatePod,
		// todo: Delete 事件应该不需要
	})

	return podOOMRecorderController, nil
}

func (oc *PodOOMRecorderController) Run() {
	defer utilruntime.HandleCrash()
	defer klog.Infof("[resource-recommend] shutting down %s controller", OOMRecorderControllerName)

	if !cache.WaitForCacheSync(oc.ctx.Done(), oc.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", OOMRecorderControllerName))
		return
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				klog.Error(errors.Errorf("run oom recorder panic: %v", r.(error)))
			}
		}()
		if err := oc.Recorder.Run(oc.ctx.Done()); err != nil {
			klog.Warningf("run oom recorder failed: %v", err)
		}
	}()

	<-oc.ctx.Done()
}

func (oc *PodOOMRecorderController) addPod(obj interface{}) {
	v, ok := obj.(*core.Pod)
	if !ok {
		klog.Errorf("cannot convert obj to *core.Pod: %v", obj)
	}
	oc.ProcessContainer(v)
}

func (oc *PodOOMRecorderController) updatePod(oldObj, _ interface{}) {
	v, ok := oldObj.(*core.Pod)
	if !ok {
		klog.Errorf("cannot convert obj to *core.Pod: %v", oldObj)
	}
	oc.ProcessContainer(v)
}

// ProcessContainer checks for OOM kills in pod containers and enqueues them for processing.
func (oc *PodOOMRecorderController) ProcessContainer(pod *core.Pod) {
	// todo: 需要将这个处理过程也包一层工作队列吗？]
	// 240727: 从 vpa 的调试情况来看 Informer 会直接给函数传递这个 pod 信息
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.RestartCount > 0 &&
			containerStatus.LastTerminationState.Terminated != nil &&
			containerStatus.LastTerminationState.Terminated.Reason == "OOMKilled" {
			if container := GetContainer(pod, containerStatus.Name); container != nil {
				if memory, ok := container.Resources.Requests[core.ResourceMemory]; ok {
					// 添加工作队列
					oc.Recorder.Queue.Add(oom.OOMRecord{
						Namespace: pod.Namespace,
						Pod:       pod.Name,
						Container: containerStatus.Name,
						Memory:    memory,
						OOMAt:     containerStatus.LastTerminationState.Terminated.FinishedAt.Time,
					})
					klog.V(2).InfoS("Last termination state of the pod is oom", "namespace", pod.Namespace,
						"pod", pod.Name, "container", containerStatus.Name, "MemoryRequest", memory)
				}
			}
		}
	}
}

// GetContainer get container info from pod
// todo: 感觉可以放在 utils 里
func GetContainer(pod *core.Pod, containerName string) *core.Container {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == containerName {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}
