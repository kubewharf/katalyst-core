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
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

// KubeletPodFetcher is used to get K8S kubelet pod information.
type KubeletPodFetcher interface {
	// GetPodList returns those latest pods, and podFilter is a function to filter a pod,
	// if pod passed return true else return false, if podFilter is nil return all pods
	GetPodList(ctx context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error)

	// GetPod returns Pod by UID
	GetPod(ctx context.Context, podUID string) (*v1.Pod, error)
}

// kubeletPodFetcherImpl use kubelet 10255 pods interface to get pod directly without cache.
type kubeletPodFetcherImpl struct{}

func getPodsByKubeletAPI() (v1.PodList, error) {
	const podsApi = "http://localhost:10255/pods"

	var podList v1.PodList
	err := process.GetAndUnmarshal(podsApi, &podList)
	if err != nil {
		return podList, fmt.Errorf("failed to get pod list, error: %v", err)
	} else if len(podList.Items) == 0 {
		// kubelet should at least contain current pod as an item
		return podList, fmt.Errorf("kubelet returns empty pod list")
	}
	return podList, nil
}

// GetPodList get pods from kubelet 10255/pods api, and the returned slice does not
// contain pods that don't pass `podFilter`
func (k *kubeletPodFetcherImpl) GetPodList(_ context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error) {
	podList, err := getPodsByKubeletAPI()
	if err != nil {
		return nil, err
	}

	var pods []*v1.Pod
	for i := range podList.Items {
		if podFilter != nil && !podFilter(&podList.Items[i]) {
			continue
		}
		pods = append(pods, &podList.Items[i])
	}
	return pods, nil
}

func (k *kubeletPodFetcherImpl) GetPod(_ context.Context, podUID string) (*v1.Pod, error) {
	podList, err := getPodsByKubeletAPI()
	if err != nil {
		return nil, err
	}
	for _, pod := range podList.Items {
		if string(pod.UID) == podUID {
			return &pod, nil
		}
	}
	return nil, fmt.Errorf("failed to find pod by uid %v", podUID)
}

func NewKubeletPodFetcher() KubeletPodFetcher {
	return &kubeletPodFetcherImpl{}
}
