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

package manager

import (
	"context"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	hmadvisor "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

var headroomManagerInitializers sync.Map

// ResourceManager provides a general interface for managing resources
type ResourceManager interface {
	// GetAllocatable returns the allocatable resource of this manager
	GetAllocatable() (resource.Quantity, error)
	// GetCapacity returns the total capacity of this manager
	GetCapacity() (resource.Quantity, error)
}

// NumaResourceManager provides an interface for managing NUMA-specific resources
type NumaResourceManager interface {
	// GetNumaAllocatable returns the allocatable resource for each NUMA node
	GetNumaAllocatable() (map[int]resource.Quantity, error)
	// GetNumaCapacity returns the capacity for each NUMA node
	GetNumaCapacity() (map[int]resource.Quantity, error)
}

// HeadroomManager manages both global and NUMA-specific resource headroom reporting
type HeadroomManager interface {
	ResourceManager     // Inherits global resource management methods
	NumaResourceManager // Inherits NUMA-specific resource management methods

	// Run starts the resource manager
	Run(ctx context.Context)
}

// InitFunc is used to init headroom manager
type InitFunc func(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, headroomAdvisor hmadvisor.ResourceAdvisor) (HeadroomManager, error)

// RegisterHeadroomManagerInitializer is used to register user-defined headroom manager init functions
func RegisterHeadroomManagerInitializer(name v1.ResourceName, initFunc InitFunc) {
	headroomManagerInitializers.Store(name, initFunc)
}

// GetRegisteredManagerInitializers is used to get registered user-defined headroom manager init functions
func GetRegisteredManagerInitializers() map[v1.ResourceName]InitFunc {
	headroomManagers := make(map[v1.ResourceName]InitFunc)
	headroomManagerInitializers.Range(func(key, value interface{}) bool {
		headroomManagers[key.(v1.ResourceName)] = value.(InitFunc)
		return true
	})
	return headroomManagers
}
