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

import "fmt"

// runtimePodFetcherStub is used as testing implementation of RuntimePodFetcher
type runtimePodFetcherStub struct {
	pods              []*RuntimePod
	containerIdToInfo map[string]map[string]string
}

func (r *runtimePodFetcherStub) GetPods(all bool) ([]*RuntimePod, error) {
	return r.pods, nil
}

func (r *runtimePodFetcherStub) GetContainerInfo(containerId string) (map[string]string, error) {
	if _, ok := r.containerIdToInfo[containerId]; !ok {
		return nil, fmt.Errorf("containerId %s not found in pods", containerId)
	}
	return r.containerIdToInfo[containerId], nil
}
