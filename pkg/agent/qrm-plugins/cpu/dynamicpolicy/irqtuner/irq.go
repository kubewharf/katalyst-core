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

package irqtuner

import (
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type ContainerInfo struct {
	*commonstate.AllocationMeta

	ContainerID string
	// CgroupPath is the relative cgroup path, e.g. /kubepods/burstable/pod<uuid>/<container id>.
	CgroupPath string
	// RuntimeClassName refers to pod spec runtime class.
	RuntimeClassName string
	// ActualCPUSet represents the actual cpuset, which is real-time data, with the numa id as the map key.
	ActualCPUSet map[int]machine.CPUSet

	StartedAt v1.Time
}

type StateAdapter interface {
	// ListContainers only return running containers, not include daemonset, because katalyst has no knowledge about daemonset pod creation,
	// and usually doesn't fail.
	ListContainers() ([]ContainerInfo, error)

	// GetIRQForbiddenCores get irq forbidden cores from qrm state manager.
	GetIRQForbiddenCores() (machine.CPUSet, error)

	// GetStepExpandableCPUsMax indicates the maximum number of CPUs that can be expanded for the interrupt exclusive core in a single step.
	GetStepExpandableCPUsMax() int

	// GetExclusiveIRQCPUSet get exclusive cores from qrm state manager.
	GetExclusiveIRQCPUSet() (machine.CPUSet, error)

	// SetExclusiveIRQCPUSet irq tuning controller only set exclusive irq cores to qrm-state manager, irq affinity tuning operation performed by irq-tuning
	// controller is transparent to qrm-stat manager.
	SetExclusiveIRQCPUSet(machine.CPUSet) error
}

type Tuner interface {
	Run(stopCh <-chan struct{})
	Stop()
}
