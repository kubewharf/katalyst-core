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

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// ProvisionPolicy generates resource provision result based on configured algorithm
type ProvisionPolicy interface {
	// SetEssentials set essentials for policy update
	SetEssentials(resourceEssentials types.ResourceEssentials, controlEssentials types.ControlEssentials)
	// SetPodSet overwrites policy's pod/container record
	SetPodSet(types.PodSet)
	// SetCPUAffinityNUMAs overwrites the numa ids this policy interested in.
	// Notice: SetCPUAffinityNUMAs is the same as SetBindingNUMAs, only to keep
	// compatibility with old code.
	SetCPUAffinityNUMAs(numas machine.CPUSet, isNUMAAffinity bool)

	// Update triggers an episode of algorithm update
	Update() error
	// GetControlKnobAdjusted returns the latest legal control knob value
	GetControlKnobAdjusted() (types.ControlKnob, error)

	GetMetaInfo() string
}

type InitFunc func(regionName string, regionType configapi.QoSRegionType, ownerPoolName string,
	conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) ProvisionPolicy

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
