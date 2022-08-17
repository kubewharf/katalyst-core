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

package native

import (
	"strings"

	v1 "k8s.io/api/core/v1"
)

const (
	defaultDockerContainerIDPrefix     = "docker://"
	defaultContainerdContainerIDPrefix = "containerd://"
)

const ContainerMetricPortName = "metrics"
const ContainerMetricStorePortName = "store"

// containerNotRunning returns whether the given containers are all not-running, ie.
// if anyone falls to not-running state, returns false
func containerNotRunning(statuses []v1.ContainerStatus) bool {
	for _, status := range statuses {
		if status.State.Terminated == nil && status.State.Waiting == nil {
			return false
		}
	}
	return true
}

// TrimContainerIDPrefix is used to parse the specific containerID
// out of the whole containerID info
func TrimContainerIDPrefix(id string) string {
	return strings.TrimPrefix(strings.TrimPrefix(id, defaultDockerContainerIDPrefix), defaultContainerdContainerIDPrefix)
}

// ParseHostPortsForContainer gets host port from container spec
func ParseHostPortsForContainer(container *v1.Container, portName string) []int32 {
	var res []int32
	for _, port := range container.Ports {
		if port.Name == portName && port.HostPort > 0 {
			res = append(res, port.HostPort)
		}
	}
	return res
}
