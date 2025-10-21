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

package allocate

import (
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// AllocationContext contains all the information needed for GPU allocation
type AllocationContext struct {
	ResourceReq        *pluginapi.ResourceRequest
	DeviceReq          *pluginapi.DeviceRequest
	GPUTopology        *machine.DeviceTopology
	GPUQRMPluginConfig *qrm.GPUQRMPluginConfig
	Emitter            metrics.MetricEmitter
	MetaServer         *metaserver.MetaServer
	MachineState       state.AllocationResourcesMap
	QoSLevel           string
	HintNodes          machine.CPUSet
}

// AllocationResult contains the result of GPU allocation
type AllocationResult struct {
	AllocatedDevices []string
	Success          bool
	ErrorMessage     string
}

// FilteringStrategy defines the interface for filtering GPU devices
type FilteringStrategy interface {
	// Name returns the name of the filtering strategy
	Name() string

	// Filter filters the available GPU devices based on the allocation context
	// Returns a list of filtered device IDs
	Filter(ctx *AllocationContext, allAvailableDevices []string) ([]string, error)
}

// SortingStrategy defines the interface for sorting GPU devices
type SortingStrategy interface {
	// Name returns the name of the sorting strategy
	Name() string

	// Sort sorts the filtered GPU devices based on the allocation context
	// Returns a prioritized list of device IDs
	Sort(ctx *AllocationContext, filteredDevices []string) ([]string, error)
}

// BindingStrategy defines the interface for binding GPU devices
type BindingStrategy interface {
	// Name returns the name of the binding strategy
	Name() string

	// Bind binds the sorted GPU devices to the allocation context
	// Returns the final allocation result
	Bind(ctx *AllocationContext, sortedDevices []string) (*AllocationResult, error)
}

type AllocationStrategy interface {
	// Name returns the name of the allocation strategy
	Name() string

	// Allocate performs the allocation using the combined strategies
	Allocate(ctx *AllocationContext) (*AllocationResult, error)
}
