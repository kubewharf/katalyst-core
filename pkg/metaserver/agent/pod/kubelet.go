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

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const podsApi = "http://localhost:%v/%v"

// KubeletPodFetcher is used to get K8S kubelet pod information.
type KubeletPodFetcher interface {
	// GetPodList returns those latest pods, and podFilter is a function to filter a pod,
	// if pod passed return true else return false, if podFilter is nil return all pods
	GetPodList(ctx context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error)
}

// kubeletPodFetcherImpl use kubelet 10255 pods interface to get pod directly without cache.
type kubeletPodFetcherImpl struct {
	baseConf *global.BaseConfiguration
}

// GetPodList get pods from kubelet 10255/pods api, and the returned slice does not
// contain pods that don't pass `podFilter`
func (k *kubeletPodFetcherImpl) GetPodList(ctx context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error) {
	var podList v1.PodList

	if k.baseConf.KubeletSecurePortEnabled {
		if err := native.GetAndUnmarshalForHttps(ctx, k.baseConf.KubeletSecurePort, k.baseConf.NodeAddress,
			k.baseConf.KubeletPodsEndpoint, k.baseConf.APIAuthTokenFile, &podList); err != nil {
			return []*v1.Pod{}, fmt.Errorf("failed to get kubelet config, error: %v", err)
		}
	} else {
		url := fmt.Sprintf(podsApi, k.baseConf.KubeletReadOnlyPort, k.baseConf.KubeletPodsEndpoint)
		if err := process.GetAndUnmarshal(url, &podList); err != nil {
			return []*v1.Pod{}, fmt.Errorf("failed to get pod list, error: %v", err)
		}
	}

	if len(podList.Items) == 0 {
		// kubelet should at least contain current pod as an item
		return []*v1.Pod{}, fmt.Errorf("kubelet returns empty pod list")
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

func NewKubeletPodFetcher(baseConf *global.BaseConfiguration) KubeletPodFetcher {
	return &kubeletPodFetcherImpl{
		baseConf: baseConf,
	}
}
