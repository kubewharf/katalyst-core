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

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// HeadroomPolicy generates resource headroom estimation based on configured algorithm
type HeadroomPolicy interface {
	// SetPodSet overwrites policy's pod/container record
	SetPodSet(types.PodSet)
	// SetEssentials updates essential values for policy update
	SetEssentials(essentials types.ResourceEssentials)

	// Update triggers an episode of headroom update
	Update() error
	// GetHeadroom returns the latest legal headroom estimation
	GetHeadroom() (float64, error)
}

type InitFunc func(regionName string, conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) HeadroomPolicy

var initializers sync.Map

func RegisterInitializer(name types.CPUHeadroomPolicyName, initFunc InitFunc) {
	initializers.Store(name, initFunc)
}

func GetRegisteredInitializers() map[types.CPUHeadroomPolicyName]InitFunc {
	res := make(map[types.CPUHeadroomPolicyName]InitFunc)
	initializers.Range(func(key, value interface{}) bool {
		res[key.(types.CPUHeadroomPolicyName)] = value.(InitFunc)
		return true
	})
	return res
}
