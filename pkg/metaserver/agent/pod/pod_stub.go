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
	"sync"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type PodFetcherStub struct {
	mutex   sync.Mutex
	PodList []*v1.Pod
}

var _ PodFetcher = &PodFetcherStub{}

func (p *PodFetcherStub) GetPodList(_ context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	pods := make([]*v1.Pod, 0, len(p.PodList))
	for _, pod := range p.PodList {
		if podFilter != nil && !podFilter(pod) {
			continue
		}
		pods = append(pods, pod)
	}
	return pods, nil
}

func (p *PodFetcherStub) GetPod(_ context.Context, podUID string) (*v1.Pod, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	for _, pod := range p.PodList {
		if string(pod.UID) == podUID {
			return pod, nil
		}
	}
	return nil, fmt.Errorf("failed to find pod by uid %v", podUID)
}

func (p *PodFetcherStub) Run(_ context.Context) {}

func (p *PodFetcherStub) GetContainerID(podUID, containerName string) (string, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	for _, pod := range p.PodList {
		if string(pod.UID) == podUID {
			return native.GetContainerID(pod, containerName)
		}
	}

	return "", fmt.Errorf("container: %s isn't found in pod: %s statues", containerName, podUID)
}

func (p *PodFetcherStub) GetContainerSpec(podUID, containerName string) (*v1.Container, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	for _, pod := range p.PodList {
		if string(pod.UID) == podUID {
			for _, c := range pod.Spec.Containers {
				if c.Name == containerName {
					return c.DeepCopy(), nil
				}
			}
		}
	}

	return nil, fmt.Errorf("container: %s isn't found in pod: %s spec", containerName, podUID)
}
