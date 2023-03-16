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

package provisionpolicy

import (
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

// ProvisionPolicy generates raw cpu resource provision based on corresponding algorithm
type ProvisionPolicy interface {
	// Name returns policy name
	Name() types.CPUProvisionPolicyName

	// SetContainerSet overwrites policy's container record, which is organized as
	// map[podUID][containerName]
	SetContainerSet(map[string]sets.String)

	// SetControlKnobValue updates current control knob value
	SetControlKnobValue(types.ControlKnob)

	// SetIndicatorValue updates current indicator metric value
	SetIndicatorValue(types.Indicator)

	// SetIndicatorTarget updates indicator target value
	SetIndicatorTarget(types.Indicator)

	// Update triggers an epoch of algorithm update
	Update() error

	// GetControlKnobAdjusted returns the latest adjusted control knob value
	GetControlKnobAdjusted() types.ControlKnob
}

type InitFunc func(name types.CPUProvisionPolicyName, metaCache *metacache.MetaCache) ProvisionPolicy

var initializers sync.Map

func RegisterInitializer(name types.CPUProvisionPolicyName, initFunc InitFunc) {
	initializers.Store(name, initFunc)
}

func GetRegisteredInitializers() map[types.CPUProvisionPolicyName]InitFunc {
	res := make(map[types.CPUProvisionPolicyName]InitFunc)
	initializers.Range(func(key, value interface{}) bool {
		res[key.(types.CPUProvisionPolicyName)] = value.(InitFunc)
		return true
	})
	return res
}
