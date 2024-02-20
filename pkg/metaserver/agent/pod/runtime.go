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

package pod

import (
	"fmt"
	"time"

	kubetypes "k8s.io/apimachinery/pkg/types"
	cri "k8s.io/cri-api/pkg/apis"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/cri/remote"
	"k8s.io/kubernetes/pkg/kubelet/types"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// RuntimePod is a group of containers.
type RuntimePod struct {
	// The UID of the pod, which can be used to retrieve a particular pod
	// from the pod list returned by GetPods().
	UID kubetypes.UID
	// The name and namespace of the pod, which is readable by human.
	Name      string
	Namespace string
	// List of containers that belongs to this pod. It may contain only
	// running containers, or mixed with dead ones (when GetPods(true)).
	Containers []*runtimeapi.Container
	// List of sandboxes associated with this pod. This is only populated
	// by kuberuntime.
	Sandboxes []*runtimeapi.PodSandbox
}

type labeledContainerInfo struct {
	ContainerName string
	PodName       string
	PodNamespace  string
	runtimeName   string
	PodUID        kubetypes.UID
}

type RuntimePodFetcher interface {
	GetPods(all bool) ([]*RuntimePod, error)
}

type runtimePodFetcherImpl struct {
	runtimeService cri.RuntimeService
}

func NewRuntimePodFetcher(baseConf *global.BaseConfiguration) (RuntimePodFetcher, error) {
	runtimeService, err := remote.NewRemoteRuntimeService(baseConf.RuntimeEndpoint, 2*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("create remote runtime service failed %s", err)
	}

	return &runtimePodFetcherImpl{runtimeService: runtimeService}, nil
}

// GetPods returns a list of containers grouped by pods. The boolean parameter
// specifies whether the runtime returns all containers including those already
// exited and dead containers (used for garbage collection).
func (r *runtimePodFetcherImpl) GetPods(all bool) ([]*RuntimePod, error) {
	pods := make(map[kubetypes.UID]*RuntimePod)
	sandboxes, err := r.getKubeletSandboxes(all)
	if err != nil {
		return nil, err
	}
	for i := range sandboxes {
		s := sandboxes[i]
		if s.Metadata == nil {
			klog.V(4).InfoS("Sandbox does not have metadata", "sandbox", s)
			continue
		}
		podUID := kubetypes.UID(s.Metadata.Uid)
		if _, ok := pods[podUID]; !ok {
			pods[podUID] = &RuntimePod{
				UID:       podUID,
				Name:      s.Metadata.Name,
				Namespace: s.Metadata.Namespace,
			}
		}
		p := pods[podUID]
		p.Sandboxes = append(p.Sandboxes, s)
	}

	containers, err := r.getKubeletContainers(all)
	if err != nil {
		return nil, err
	}
	for i := range containers {
		c := containers[i]
		if c.Metadata == nil {
			klog.V(4).InfoS("Container does not have metadata", "container", c)
			continue
		}

		labelledInfo := getContainerInfoFromLabels(c.Labels)
		pod, found := pods[labelledInfo.PodUID]
		if !found {
			pod = &RuntimePod{
				UID:       labelledInfo.PodUID,
				Name:      labelledInfo.PodName,
				Namespace: labelledInfo.PodNamespace,
			}
			pods[labelledInfo.PodUID] = pod
		}
		pod.Containers = append(pod.Containers, c)
	}

	// Convert map to list.
	var result []*RuntimePod
	for _, pod := range pods {
		result = append(result, pod)
	}

	return result, nil
}

// getKubeletSandboxes lists all (or just the running) sandboxes managed by kubelet.
func (r *runtimePodFetcherImpl) getKubeletSandboxes(all bool) ([]*runtimeapi.PodSandbox, error) {
	var filter *runtimeapi.PodSandboxFilter
	if !all {
		readyState := runtimeapi.PodSandboxState_SANDBOX_READY
		filter = &runtimeapi.PodSandboxFilter{
			State: &runtimeapi.PodSandboxStateValue{
				State: readyState,
			},
		}
	}

	resp, err := r.runtimeService.ListPodSandbox(filter)
	if err != nil {
		klog.ErrorS(err, "Failed to list pod sandboxes")
		return nil, err
	}

	return resp, nil
}

// getKubeletContainers lists containers managed by kubelet.
// The boolean parameter specifies whether returns all containers including
// those already exited and dead containers (used for garbage collection).
func (r *runtimePodFetcherImpl) getKubeletContainers(allContainers bool) ([]*runtimeapi.Container, error) {
	filter := &runtimeapi.ContainerFilter{}
	if !allContainers {
		filter.State = &runtimeapi.ContainerStateValue{
			State: runtimeapi.ContainerState_CONTAINER_RUNNING,
		}
	}

	containers, err := r.runtimeService.ListContainers(filter)
	if err != nil {
		klog.ErrorS(err, "ListContainers failed")
		return nil, err
	}

	return containers, nil
}

// getContainerInfoFromLabels gets labeledContainerInfo from labels.
func getContainerInfoFromLabels(labels map[string]string) *labeledContainerInfo {
	return &labeledContainerInfo{
		PodName:       general.GetStringValueFromMap(labels, types.KubernetesPodNameLabel),
		PodNamespace:  general.GetStringValueFromMap(labels, types.KubernetesPodNamespaceLabel),
		PodUID:        kubetypes.UID(general.GetStringValueFromMap(labels, types.KubernetesPodUIDLabel)),
		ContainerName: general.GetStringValueFromMap(labels, types.KubernetesContainerNameLabel),
	}
}
