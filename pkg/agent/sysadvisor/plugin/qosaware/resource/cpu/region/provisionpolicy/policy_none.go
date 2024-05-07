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
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type PolicyNone struct{}

func NewPolicyNone(_ string, _ types.QoSRegionType, _ string,
	_ *config.Configuration, _ interface{}, _ metacache.MetaReader,
	_ *metaserver.MetaServer, _ metrics.MetricEmitter,
) ProvisionPolicy {
	return &PolicyNone{}
}

func (p *PolicyNone) SetEssentials(types.ResourceEssentials, types.ControlEssentials) {}
func (p *PolicyNone) SetPodSet(types.PodSet)                                          {}
func (p *PolicyNone) SetBindingNumas(machine.CPUSet)                                  {}
func (p *PolicyNone) Update() error                                                   { return nil }
func (p *PolicyNone) GetControlKnobAdjusted() (types.ControlKnob, error) {
	return types.InvalidControlKnob, nil
}
