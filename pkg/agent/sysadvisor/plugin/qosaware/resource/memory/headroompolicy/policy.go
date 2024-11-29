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

package headroompolicy

import (
	"sync"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// HeadroomPolicy generates resource headroom estimation based on configured algorithm
type HeadroomPolicy interface {
	Name() types.MemoryHeadroomPolicyName
	// SetPodSet overwrites policy's pod/container record
	SetPodSet(types.PodSet)

	// SetEssentials updates necessary region settings for policy update
	// Available resource value = total - reservedForAllocate
	// todo: subtract reserve pool size
	SetEssentials(essentials types.ResourceEssentials)

	// Update triggers an epoch of algorithm update
	Update() error

	// GetHeadroom returns the latest headroom estimation
	GetHeadroom() (resource.Quantity, map[int]resource.Quantity, error)
}

type InitFunc func(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) HeadroomPolicy

var initializers sync.Map

func RegisterInitializer(name types.MemoryHeadroomPolicyName, initFunc InitFunc) {
	initializers.Store(name, initFunc)
}

func GetRegisteredInitializers() map[types.MemoryHeadroomPolicyName]InitFunc {
	res := make(map[types.MemoryHeadroomPolicyName]InitFunc)
	initializers.Range(func(key, value interface{}) bool {
		res[key.(types.MemoryHeadroomPolicyName)] = value.(InitFunc)
		return true
	})
	return res
}
