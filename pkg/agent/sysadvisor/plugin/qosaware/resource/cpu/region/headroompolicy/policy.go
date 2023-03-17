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
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
)

// HeadroomPolicy generates raw resource headroom value based on corresponding algorithm
type HeadroomPolicy interface {
	// SetContainerSet overwrites policy's container record, which is organized as
	// map[podUID][containerName]
	SetContainerSet(map[string]sets.String)

	// Update triggers an epoch of algorithm update
	Update() error

	// GetHeadroom returns the latest headroom result
	GetHeadroom() (resource.Quantity, error)

	SetCPULimit(limit int64)

	SetCPUReserved(reserved int64)
}

type InitFunc func(metaCache *metacache.MetaCache, metaServer *metaserver.MetaServer) HeadroomPolicy

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
