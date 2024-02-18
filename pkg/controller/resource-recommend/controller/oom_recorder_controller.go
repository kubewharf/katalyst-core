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

package controller

import (
	"context"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/oom"
)

// PodOOMRecorderController reconciles a PodOOMRecorder object
type PodOOMRecorderController struct {
	*oom.PodOOMRecorder
}

//+kubebuilder:rbac:groups=recommendation.katalyst.kubewharf.io,resources=podOOMRecorder,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=recommendation.katalyst.kubewharf.io,resources=podOOMRecorder/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=recommendation.katalyst.kubewharf.io,resources=podOOMRecorder/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// reconcile takes an pod resource.It records information when a Pod experiences an OOM (Out of Memory) state.
// the ResourceRecommend object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *PodOOMRecorderController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.V(5).InfoS("Get pods", "NamespacedName", req.NamespacedName)
	pod := &v1.Pod{}
	err := r.Client.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.RestartCount > 0 &&
			containerStatus.LastTerminationState.Terminated != nil &&
			containerStatus.LastTerminationState.Terminated.Reason == "OOMKilled" {
			if container := GetContainer(pod, containerStatus.Name); container != nil {
				if memory, ok := container.Resources.Requests[v1.ResourceMemory]; ok {
					r.Queue.Add(oom.OOMRecord{
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
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodOOMRecorderController) SetupWithManager(mgr ctrl.Manager) error {
	r.Queue = workqueue.New()
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			RecoverPanic: true,
		}).
		For(&v1.Pod{}).
		Complete(r)
}

func GetContainer(pod *v1.Pod, containerName string) *v1.Container {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == containerName {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}
